import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Writable } from 'node:stream';
import React from 'react';
import { render } from 'ink';

import { Hero } from '../src/ui/terminalInterface.js';

class FakeStdout extends Writable {
  columns = 100;
  rows = 40;
  output = '';
  _write(chunk: Buffer | string, _encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    this.output += chunk.toString();
    callback();
  }
}

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

/** Visible width, ignoring the SGR escapes Ink writes for colour. */
const visible = (line: string): number => [...line.replace(/\[[0-9;]*m/g, '')].length;

const SESSIONS = [
  { name: 'plugin-loader-work', savedAt: new Date(Date.now() - 90_000).toISOString() },
  { name: 'a-really-quite-long-session-name-that-keeps-going', savedAt: new Date(Date.now() - 9 * 86_400_000).toISOString() },
];

interface Options {
  fresh?: boolean;
  workspace?: string;
  model?: string;
}

async function renderHome(width: number, options: Options = {}): Promise<string[]> {
  const { fresh = false } = options;
  const stdout = new FakeStdout();
  stdout.columns = width;
  const app = render(
    React.createElement(Hero, {
      width,
      endpoint: 'http://localhost:20128',
      model: options.model ?? 'auto/best-coding',
      mode: 'build' as const,
      perm: 'ask' as const,
      workspace: options.workspace ?? '/Users/someone/code/work/omniharness',
      sessions: fresh ? [] : SESSIONS,
      skills: fresh ? 0 : 63,
      plugins: fresh ? 0 : 41,
      mcpTools: fresh ? 0 : 7,
    }),
    { stdout: stdout as unknown as NodeJS.WriteStream, patchConsole: false },
  );
  await sleep(40);
  app.unmount();
  return stdout.output.split('\n');
}

for (const width of [48, 60, 76, 80, 100, 120]) {
  test(`the home screen fits ${width} columns`, async () => {
    for (const line of await renderHome(width)) {
      assert.ok(
        visible(line) <= width,
        `a line ran to ${visible(line)} columns in a ${width}-column terminal:\n${JSON.stringify(line)}`,
      );
    }
  });
}

// The bug this exists for: a value sized to the whole box rather than to the
// room left beside its label does not overflow the terminal — Ink wraps it
// inside the box instead. So the width check above passes and the screen is
// still wrong: the block grows a line, the labels stop lining up, and how tall
// the home screen is starts depending on how long your path happens to be.
//
// The invariant that actually catches it is that the height does not move.
for (const width of [48, 80, 100]) {
  test(`long values are shortened, not wrapped, at ${width} columns`, async () => {
    const short = await renderHome(width, { workspace: '/w', model: 'm' });
    const long = await renderHome(width, {
      workspace: '/Users/someone/very/deeply/nested/projects/omniharness/and/further/still',
      model: 'some-provider/a-model-with-a-really-long-identifier-v2',
    });
    assert.equal(
      long.length,
      short.length,
      `a long path made the home screen ${long.length - short.length} line(s) taller, so it wrapped instead of being clipped:\n${long.join('\n')}`,
    );
  });
}

test('a fresh install shows no empty blocks', async () => {
  const out = (await renderHome(100, { fresh: true })).join('\n');
  // Nothing saved and nothing loaded, so those blocks are absent rather than
  // present and empty: a "recent" heading with no rows under it, or "0 skills",
  // both read as something being broken.
  assert.ok(!out.includes('recent'), 'the recent block must be absent with no sessions');
  assert.ok(!out.includes('loaded'), 'the loaded line must be absent with nothing loaded');
  assert.ok(out.includes('keys'), 'the key hints are useful even on a first run');
  assert.ok(out.includes('omniharness'));
});

test('the home screen shows what this session actually is', async () => {
  const out = (await renderHome(100)).join('\n');
  for (const want of ['omniharness', 'workspace', 'gateway', 'auto/best-coding', 'build', 'manual', 'recent', 'keys']) {
    assert.ok(out.includes(want), `missing ${want} from the home screen`);
  }
  // Real counts, not placeholder copy.
  assert.ok(out.includes('63 skills from 41 plugins'), 'the capability line should report what is loaded');
  assert.ok(out.includes('plugin-loader-work'), 'a recent session should be listed');
});
