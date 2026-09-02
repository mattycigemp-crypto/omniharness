import assert from 'node:assert/strict';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';

import {
  commandToSkill,
  discoverPlugins,
  loadFrom,
  loadPlugin,
  parseFrontmatter,
  renderCommandBody,
} from '../src/plugins.js';
import { skillKind } from '../src/skills.js';
import { createMastraEngine } from '../src/agent/mastraEngine.js';
import http from 'node:http';

function toSSE(payload: Record<string, unknown>): string {
  const message = (payload.message as Record<string, unknown> | undefined) ?? {};
  const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : undefined;
  const chunks: string[] = [];
  if (toolCalls) chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { tool_calls: toolCalls }, finish_reason: null }] })}`);
  const content = typeof message.content === 'string' && message.content !== '' ? message.content : undefined;
  if (content) chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { content }, finish_reason: null }] })}`);
  chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: {}, finish_reason: payload.finish_reason ?? 'stop' }] })}`);
  chunks.push('data: [DONE]');
  return chunks.join('\n');
}

type WireBody = { model: string; messages: Array<{ role: string; content: string }>; tools: unknown };

function server(handler: () => unknown) {
  const calls: WireBody[] = [];
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    calls.push(JSON.parse(Buffer.concat(chunks).toString()) as WireBody);
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.end(toSSE(handler() as Record<string, unknown>));
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('server did not bind');
  return { calls, url: `http://localhost:${address.port}`, close: () => instance.close() };
}

/** Write a plugin in the on-disk layout the format actually uses. */
async function writePlugin(root: string, name: string, commands: Record<string, string>): Promise<string> {
  const dir = path.join(root, name);
  await mkdir(path.join(dir, '.claude-plugin'), { recursive: true });
  await writeFile(
    path.join(dir, '.claude-plugin', 'plugin.json'),
    JSON.stringify({ name, description: `${name} plugin`, version: '1.2.3', author: { name: 'A Person' } }),
  );
  await mkdir(path.join(dir, 'commands'), { recursive: true });
  for (const [file, body] of Object.entries(commands)) {
    await writeFile(path.join(dir, 'commands', file), body);
  }
  return dir;
}

test('parseFrontmatter splits metadata from the prompt body', () => {
  const { fields, body } = parseFrontmatter(
    '---\ndescription: Code review a pull request\nallowed-tools: Bash(gh pr view:*), Bash(gh pr diff:*), mcp__x__y\n---\n\nReview the PR.\n\nBe thorough.',
  );
  assert.equal(fields.description, 'Code review a pull request');
  assert.equal(body, 'Review the PR.\n\nBe thorough.');
});

test('a body with no frontmatter is a prompt, not an error', () => {
  const { fields, body } = parseFrontmatter('Just do the thing.');
  assert.deepEqual(fields, {});
  assert.equal(body, 'Just do the thing.');
});

test('allowed-tools splits on commas outside the parentheses', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'oh-plug-tools-'));
  try {
    await writePlugin(root, 'p', {
      'r.md': '---\ndescription: r\nallowed-tools: Bash(gh pr view:*), Bash(gh issue list:*), mcp__github__comment\n---\nbody',
    });
    const plugin = await loadPlugin(path.join(root, 'p'));
    assert.deepEqual(plugin?.commands[0]?.allowedTools, [
      'Bash(gh pr view:*)',
      'Bash(gh issue list:*)',
      'mcp__github__comment',
    ]);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('loadPlugin reads the manifest and every command', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'oh-plug-'));
  try {
    const dir = await writePlugin(root, 'code-review', {
      'review.md': '---\ndescription: Review a PR\n---\nReview $ARGUMENTS carefully.',
      'triage.md': '---\ndescription: Triage\n---\nTriage it.',
      'README.txt': 'not a command',
    });
    const plugin = await loadPlugin(dir);
    assert.ok(plugin);
    assert.equal(plugin.name, 'code-review');
    assert.equal(plugin.version, '1.2.3');
    assert.equal(plugin.author, 'A Person');
    assert.deepEqual(plugin.commands.map((c) => c.name), ['review', 'triage']);
    assert.equal(plugin.commands[0]?.description, 'Review a PR');
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('a directory without a manifest is not a plugin', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'oh-plug-none-'));
  try {
    await mkdir(path.join(root, 'commands'), { recursive: true });
    await writeFile(path.join(root, 'commands', 'x.md'), 'body');
    assert.equal(await loadPlugin(root), null);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('a marketplace directory yields the plugins it lists', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'oh-market-'));
  try {
    const plugins = path.join(root, 'plugins');
    await writePlugin(plugins, 'alpha', { 'a.md': '---\ndescription: a\n---\nA' });
    await writePlugin(plugins, 'beta', { 'b.md': '---\ndescription: b\n---\nB' });
    await mkdir(path.join(root, '.claude-plugin'), { recursive: true });
    await writeFile(path.join(root, '.claude-plugin', 'marketplace.json'), JSON.stringify({
      name: 'mine',
      plugins: [
        { name: 'alpha', source: './plugins/alpha' },
        { name: 'beta', source: './plugins/beta' },
        // A source pointing outside the marketplace must be ignored, not followed.
        { name: 'escape', source: '../../../etc' },
      ],
    }));

    const found = await loadFrom(root);
    assert.deepEqual(found.map((p) => p.name), ['alpha', 'beta']);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('a plain directory of plugin directories also works', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'oh-plaindir-'));
  try {
    await writePlugin(root, 'one', { 'x.md': '---\ndescription: x\n---\nX' });
    await writePlugin(root, 'two', { 'y.md': '---\ndescription: y\n---\nY' });
    assert.deepEqual((await loadFrom(root)).map((p) => p.name), ['one', 'two']);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('discovery prefers the workspace copy over the user one', async () => {
  const base = await mkdtemp(path.join(os.tmpdir(), 'oh-precedence-'));
  try {
    const workspace = path.join(base, 'ws');
    const user = path.join(base, 'user');
    await writePlugin(workspace, 'shared', { 'c.md': '---\ndescription: workspace copy\n---\nW' });
    await writePlugin(user, 'shared', { 'c.md': '---\ndescription: user copy\n---\nU' });

    const found = await discoverPlugins(base, [workspace, user]);
    assert.equal(found.length, 1, 'the same plugin name must not load twice');
    assert.equal(found[0]?.commands[0]?.description, 'workspace copy');
  } finally { await rm(base, { recursive: true, force: true }); }
});

test('a missing plugin directory is not an error', async () => {
  const found = await discoverPlugins('/nope', [path.join(os.tmpdir(), 'definitely-not-here-9f3a')]);
  assert.deepEqual(found, []);
});

test('renderCommandBody substitutes the argument tokens', () => {
  assert.equal(renderCommandBody('Review $ARGUMENTS now', '  PR 42  '), 'Review PR 42 now');
  assert.equal(renderCommandBody('first=$1 second=$2', 'alpha beta'), 'first=alpha second=beta');
  // A token with nothing to fill it becomes empty rather than staying literal,
  // so the model never sees "$2" and treats it as instruction.
  assert.equal(renderCommandBody('first=$1 second=$2', 'alpha'), 'first=alpha second=');
  assert.equal(renderCommandBody('no tokens here', 'ignored'), 'no tokens here');
});

test('a command becomes a prompt skill, namespaced by its plugin', () => {
  const skill = commandToSkill({
    name: 'review', description: 'Review a PR', body: 'Review $ARGUMENTS.',
    allowedTools: ['Bash(gh pr view:*)'], plugin: 'code-review', path: '/x/review.md',
  });
  assert.equal(skill.name, 'code-review:review');
  assert.equal(skillKind(skill), 'prompt');
  assert.equal(skill.prompt, 'Review $ARGUMENTS.');
  // A prompt skill runs nothing itself, so it carries no shell command.
  assert.equal(skill.command, '');
  assert.deepEqual(skill.parameters.map((p) => p.name), ['arguments']);
});

test('an OMNIHARNESS.md skill is still a shell skill', () => {
  assert.equal(skillKind({ name: 's', description: 'd', command: 'echo hi', parameters: [] }), 'shell');
});

test('a marketplace cloned into the plugins directory is found', async () => {
  // The ordinary way to get plugins: clone a plugin repo into
  // .claude/plugins. That puts a marketplace one level below the search root,
  // and looking only for plugin.json in the immediate children found nothing —
  // 41 real plugins read as 0.
  const base = await mkdtemp(path.join(os.tmpdir(), 'oh-nested-'));
  try {
    const repo = path.join(base, 'some-repo');
    await writePlugin(path.join(repo, 'plugins'), 'alpha', { 'a.md': '---\ndescription: a\n---\nA' });
    await mkdir(path.join(repo, '.claude-plugin'), { recursive: true });
    await writeFile(path.join(repo, '.claude-plugin', 'marketplace.json'), JSON.stringify({
      name: 'repo', plugins: [{ name: 'alpha', source: './plugins/alpha' }],
    }));

    const found = await discoverPlugins(base, [base]);
    assert.deepEqual(found.map((p) => p.name), ['alpha']);
  } finally { await rm(base, { recursive: true, force: true }); }
});

test('discovery does not walk an arbitrarily deep tree', async () => {
  const base = await mkdtemp(path.join(os.tmpdir(), 'oh-deep-'));
  try {
    // Four levels down is past the bound: a plugins directory holding a source
    // tree must not be crawled to the bottom on every start.
    await writePlugin(path.join(base, 'a', 'b', 'c'), 'buried', { 'x.md': '---\ndescription: x\n---\nX' });
    assert.deepEqual(await discoverPlugins(base, [base]), []);
  } finally { await rm(base, { recursive: true, force: true }); }
});

test('OMNIHARNESS_PLUGIN_PATH pins where plugins come from', async () => {
  const base = await mkdtemp(path.join(os.tmpdir(), 'oh-path-'));
  const before = process.env.OMNIHARNESS_PLUGIN_PATH;
  try {
    await writePlugin(path.join(base, 'dir'), 'pinned', { 'p.md': '---\ndescription: p\n---\nP' });

    process.env.OMNIHARNESS_PLUGIN_PATH = path.join(base, 'dir');
    assert.deepEqual((await discoverPlugins(base)).map((p) => p.name), ['pinned']);

    // Empty means no discovery at all, which is what makes a run reproducible
    // on a machine that has plugins installed in the home directory.
    process.env.OMNIHARNESS_PLUGIN_PATH = '';
    assert.deepEqual(await discoverPlugins(base), []);
  } finally {
    if (before === undefined) delete process.env.OMNIHARNESS_PLUGIN_PATH;
    else process.env.OMNIHARNESS_PLUGIN_PATH = before;
    await rm(base, { recursive: true, force: true });
  }
});

test('the engine offers a plugin command to the model and returns its prompt', async () => {
  const base = await mkdtemp(path.join(os.tmpdir(), 'oh-engine-plugin-'));
  const before = process.env.OMNIHARNESS_PLUGIN_PATH;
  try {
    await writePlugin(path.join(base, 'plugins'), 'review-kit', {
      'pr.md': '---\ndescription: Review a pull request\n---\nReview PR $ARGUMENTS against the checklist.',
    });
    process.env.OMNIHARNESS_PLUGIN_PATH = path.join(base, 'plugins');

    const responses: Array<Record<string, unknown>> = [
      { finish_reason: 'tool_calls', message: { role: 'assistant', content: '', tool_calls: [
        { index: 0, id: 'c1', type: 'function', function: { name: 'review-kit:pr', arguments: JSON.stringify({ arguments: '42' }) } },
      ] } },
      { finish_reason: 'stop', message: { role: 'assistant', content: 'reviewed' } },
    ];
    const live = server(() => responses.shift());
    try {
      const engine = await createMastraEngine({ workspaceRoot: base, endpoint: live.url });

      // Offered to the model alongside the built-ins.
      const skill = engine.skills.find((s) => s.name === 'review-kit:pr');
      assert.ok(skill, `plugin skill missing from ${engine.skills.map((s) => s.name).join(', ')}`);
      assert.equal(skillKind(skill), 'prompt');

      const result = await engine.run('review it');
      assert.equal(result.content, 'reviewed');

      // Invoking it hands the model the command body with $ARGUMENTS filled,
      // which is the whole point: the plugin instructs, it does not shell out.
      const shown = live.calls.flatMap((c) => c.messages.filter((m) => m.role === 'tool').map((m) => m.content));
      assert.ok(
        shown.some((text) => text.includes('Review PR 42 against the checklist.')),
        `the rendered prompt never reached the model: ${JSON.stringify(shown)}`,
      );
    } finally { live.close(); }
  } finally {
    if (before === undefined) delete process.env.OMNIHARNESS_PLUGIN_PATH;
    else process.env.OMNIHARNESS_PLUGIN_PATH = before;
    await rm(base, { recursive: true, force: true });
  }
});
