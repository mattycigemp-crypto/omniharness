import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import type { Skill, SkillParam } from './skills.js';

/**
 * Loads plugins written in the Claude Code plugin layout, so an existing
 * plugin directory works here without being rewritten.
 *
 * The layout is a published convention, implemented here from its public
 * shape rather than copied from anywhere:
 *
 *   <plugin>/.claude-plugin/plugin.json   { name, description, version, author }
 *   <plugin>/commands/*.md                frontmatter + a markdown prompt body
 *   <plugin>/agents/*.md                  same shape, described as an agent
 *
 * A directory holding .claude-plugin/marketplace.json is a collection: its
 * `plugins` entries point at the individual plugin directories.
 *
 * The execution models differ and the difference matters. An OMNIHARNESS.md
 * skill is a shell command. A command here is a prompt — invoking it puts its
 * body in front of the model as instructions. Both arrive as Skills, told
 * apart by `kind`, so nothing about the existing format changes.
 */

export interface PluginCommand {
  /** Invocation name, from the file name: commands/review.md -> "review". */
  name: string;
  description: string;
  /** The markdown body, with $ARGUMENTS still in place. */
  body: string;
  /** allowed-tools from the frontmatter, verbatim and unparsed. */
  allowedTools: readonly string[];
  /** Where it came from, for display and for name collisions. */
  plugin: string;
  path: string;
}

export interface Plugin {
  name: string;
  description: string;
  version: string;
  author: string;
  root: string;
  commands: readonly PluginCommand[];
}

interface Frontmatter {
  fields: Record<string, string>;
  body: string;
}

/**
 * Split leading `---` delimited frontmatter from a markdown body.
 *
 * Deliberately a small key/value reader rather than a YAML parser: command
 * frontmatter in practice is flat scalars, and a dependency that can evaluate
 * arbitrary YAML is a poor thing to point at files the user downloaded. A file
 * with no frontmatter is not an error — it is a prompt with no metadata.
 */
export function parseFrontmatter(text: string): Frontmatter {
  const normalized = text.replace(/^﻿/, '');
  const match = /^---[ \t]*\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)/.exec(normalized);
  if (!match) return { fields: {}, body: normalized.trim() };

  const fields: Record<string, string> = {};
  for (const line of match[1]!.split(/\r?\n/)) {
    const kv = /^([A-Za-z0-9_-]+)[ \t]*:[ \t]*(.*)$/.exec(line);
    if (!kv) continue;
    let value = kv[2]!.trim();
    // Strip one layer of matching quotes, the only quoting seen in practice.
    if ((value.startsWith('"') && value.endsWith('"') && value.length > 1)
      || (value.startsWith("'") && value.endsWith("'") && value.length > 1)) {
      value = value.slice(1, -1);
    }
    fields[kv[1]!.toLowerCase()] = value;
  }
  return { fields, body: normalized.slice(match[0].length).trim() };
}

function splitList(value: string | undefined): string[] {
  if (!value) return [];
  const inner = value.trim().replace(/^\[/, '').replace(/\]$/, '');
  // Split on commas that are not inside the parentheses of Bash(gh pr view:*).
  const out: string[] = [];
  let depth = 0;
  let current = '';
  for (const ch of inner) {
    if (ch === '(') depth += 1;
    if (ch === ')') depth = Math.max(0, depth - 1);
    if (ch === ',' && depth === 0) { out.push(current); current = ''; continue; }
    current += ch;
  }
  out.push(current);
  return out.map((entry) => entry.trim().replace(/^["']|["']$/g, '')).filter((entry) => entry !== '');
}

async function readJSON(file: string): Promise<Record<string, unknown> | null> {
  try {
    return JSON.parse(await fs.readFile(file, 'utf8')) as Record<string, unknown>;
  } catch {
    return null;
  }
}

async function readCommands(root: string, pluginName: string, dir: string): Promise<PluginCommand[]> {
  const base = path.join(root, dir);
  let entries: string[];
  try {
    entries = await fs.readdir(base);
  } catch {
    return [];
  }
  const commands: PluginCommand[] = [];
  for (const entry of entries.sort()) {
    if (!entry.toLowerCase().endsWith('.md')) continue;
    const file = path.join(base, entry);
    let raw: string;
    try {
      raw = await fs.readFile(file, 'utf8');
    } catch {
      continue;
    }
    const { fields, body } = parseFrontmatter(raw);
    if (body === '') continue; // nothing to instruct with
    commands.push({
      name: entry.replace(/\.md$/i, ''),
      description: fields.description ?? `${dir.replace(/s$/, '')} from ${pluginName}`,
      body,
      allowedTools: splitList(fields['allowed-tools']),
      plugin: pluginName,
      path: file,
    });
  }
  return commands;
}

/** Read one plugin directory. Returns null if it is not a plugin. */
export async function loadPlugin(root: string): Promise<Plugin | null> {
  const manifest = await readJSON(path.join(root, '.claude-plugin', 'plugin.json'));
  if (!manifest || typeof manifest.name !== 'string' || manifest.name === '') return null;

  const name = manifest.name;
  const author = manifest.author as { name?: string } | string | undefined;
  const commands = [
    ...await readCommands(root, name, 'commands'),
    ...await readCommands(root, name, 'agents'),
  ];
  return {
    name,
    description: typeof manifest.description === 'string' ? manifest.description : '',
    version: typeof manifest.version === 'string' ? manifest.version : '0.0.0',
    author: typeof author === 'string' ? author : (author?.name ?? ''),
    root,
    commands,
  };
}

async function subdirectories(dir: string): Promise<string[]> {
  try {
    const entries = await fs.readdir(dir, { withFileTypes: true });
    return entries.filter((e) => e.isDirectory()).map((e) => path.join(dir, e.name)).sort();
  } catch {
    return [];
  }
}

/**
 * Read a directory as either a single plugin or a marketplace of them. A
 * marketplace's `source` entries are relative to the marketplace root; a
 * source pointing outside it is ignored rather than followed.
 */
export async function loadFrom(dir: string, depth = 2): Promise<Plugin[]> {
  const single = await loadPlugin(dir);
  if (single) return [single];

  const market = await readJSON(path.join(dir, '.claude-plugin', 'marketplace.json'));
  const out: Plugin[] = [];
  if (market && Array.isArray(market.plugins)) {
    for (const entry of market.plugins as Array<Record<string, unknown>>) {
      const source = typeof entry.source === 'string' ? entry.source : '';
      if (source === '') continue;
      const resolved = path.resolve(dir, source);
      const rel = path.relative(dir, resolved);
      if (rel === '' || rel.startsWith('..') || path.isAbsolute(rel)) continue;
      const plugin = await loadPlugin(resolved);
      if (plugin) out.push(plugin);
    }
    return out;
  }

  // A directory of plugin directories — and a child may itself be a
  // marketplace rather than a plugin, which is what cloning a plugin
  // repository into the plugins directory gives you. Descending only one
  // level found nothing in that very ordinary case. Bounded, so a plugins
  // directory holding a deep source tree is not walked to the bottom.
  if (depth <= 0) return out;
  for (const child of await subdirectories(dir)) {
    out.push(...await loadFrom(child, depth - 1));
  }
  return out;
}

/**
 * The directories searched for plugins, nearest first.
 *
 * OMNIHARNESS_PLUGIN_PATH replaces the list entirely, delimited like PATH.
 * Setting it empty disables discovery. It exists because the default reaches
 * into the user's home directory: without an override, what the agent can do
 * depends on what happens to be installed on the machine, which makes a test
 * or a CI run read differently from one box to the next.
 */
export function pluginSearchPath(workspaceRoot: string): string[] {
  const override = process.env.OMNIHARNESS_PLUGIN_PATH;
  if (override !== undefined) {
    return override.split(path.delimiter).map((entry) => entry.trim()).filter((entry) => entry !== '');
  }
  const home = os.homedir();
  return [
    path.join(workspaceRoot, '.claude', 'plugins'),
    path.join(workspaceRoot, '.omniharness', 'plugins'),
    ...(home ? [path.join(home, '.claude', 'plugins')] : []),
  ];
}

/**
 * Discover every plugin on the search path. A workspace plugin wins a name
 * collision with a user-level one, because the nearer definition is the more
 * specific.
 */
export async function discoverPlugins(workspaceRoot: string, roots = pluginSearchPath(workspaceRoot)): Promise<Plugin[]> {
  const seen = new Set<string>();
  const out: Plugin[] = [];
  for (const root of roots) {
    for (const plugin of await loadFrom(root)) {
      if (seen.has(plugin.name)) continue;
      seen.add(plugin.name);
      out.push(plugin);
    }
  }
  return out;
}

/** Substitute the argument tokens a command body may use. */
export function renderCommandBody(body: string, args: string): string {
  const words = args.trim() === '' ? [] : args.trim().split(/\s+/);
  let out = body.split('$ARGUMENTS').join(args.trim());
  // $1..$9 take one word each, matching the shell convention the format borrows.
  out = out.replace(/\$([1-9])/g, (_, digit: string) => words[Number(digit) - 1] ?? '');
  return out;
}

const ARGUMENTS_PARAM: readonly SkillParam[] = [{ name: 'arguments', type: 'string' }];

/**
 * Present a plugin command as a Skill so the rest of the engine does not have
 * to know where it came from. Names are prefixed with the plugin so two
 * plugins can both ship a "review" command.
 */
export function commandToSkill(command: PluginCommand): Skill {
  return {
    name: `${command.plugin}:${command.name}`.replace(/[^A-Za-z0-9_:.-]/g, '-'),
    description: command.description,
    command: '',
    parameters: ARGUMENTS_PARAM,
    kind: 'prompt',
    prompt: command.body,
    source: command.plugin,
    allowedTools: command.allowedTools,
  };
}

/** Every command from every discovered plugin, as Skills. */
export async function loadPluginSkills(workspaceRoot: string): Promise<Skill[]> {
  const plugins = await discoverPlugins(workspaceRoot);
  return plugins.flatMap((plugin) => plugin.commands.map(commandToSkill));
}
