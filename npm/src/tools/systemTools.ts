import { execFile } from 'node:child_process';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { promisify } from 'node:util';
import { z } from 'zod';
import type { AgentToolDeclaration, WorkspaceFile, WorkspaceState } from '../types/index.js';

const execFileAsync = promisify(execFile);
const MAX_OUTPUT = 32_000;
const relativePath = z.string().min(1).refine((value) => !path.isAbsolute(value) && value !== '..' && !value.startsWith(`..${path.sep}`));

function escapes(root: string, target: string): boolean {
  const relative = path.relative(root, target);
  return relative.startsWith('..') || path.isAbsolute(relative);
}

/**
 * Resolve symlinks as far down the path as actually exists, then rejoin the
 * components that do not. A path being created cannot be resolved outright, and
 * it is exactly the one confinement has to judge before the write happens.
 */
async function resolveDeepest(target: string): Promise<string> {
  const trailing: string[] = [];
  let current = target;
  for (;;) {
    try {
      return path.join(await fs.realpath(current), ...trailing);
    } catch {
      const parent = path.dirname(current);
      if (parent === current) return target; // reached the root, nothing resolved
      trailing.unshift(path.basename(current));
      current = parent;
    }
  }
}

/**
 * Confine a path to the workspace, lexically and through symlinks.
 *
 * The lexical check alone is not confinement: a symlink inside the workspace
 * pointing out of it satisfies every string comparison while resolving
 * somewhere else entirely, and repositories carry symlinks routinely — the
 * agent does not have to create one. Both the read and the write path were
 * escaping through them.
 */
async function within(root: string, target: string): Promise<string> {
  const absoluteRoot = await resolveDeepest(path.resolve(root));
  const absoluteTarget = path.resolve(absoluteRoot, target);
  if (escapes(absoluteRoot, absoluteTarget)) throw new Error('path escapes workspace root');
  if (escapes(absoluteRoot, await resolveDeepest(absoluteTarget))) {
    throw new Error('path escapes workspace root through a symlink');
  }
  return absoluteTarget;
}

export interface SystemTools {
  readFile: AgentToolDeclaration<{ path: string }, { path: string; content: string }>;
  writeFile: AgentToolDeclaration<{ path: string; content: string }, { path: string; bytes: number }>;
  runCommand: AgentToolDeclaration<{ command: string; args?: readonly string[] }, { stdout: string; stderr: string; code: number }>;
  indexWorkspace: AgentToolDeclaration<void, WorkspaceState>;
  gitDiff: AgentToolDeclaration<void, string>;
}

export function createSystemTools(workspaceRoot: string, shellAllowed = false): SystemTools {
  const root = path.resolve(workspaceRoot);
  const readFile: SystemTools['readFile'] = {
    name: 'read_file', description: 'Read a UTF-8 file inside the workspace.', risk: 'low', inputSchema: { path: 'relative file path' },
    async execute(input) {
      const parsed = relativePath.parse(input.path);
      const target = await within(root, parsed);
      return { path: parsed, content: await fs.readFile(target, 'utf8') };
    },
  };
  const writeFile: SystemTools['writeFile'] = {
    name: 'write_file', description: 'Write a UTF-8 file inside the workspace.', risk: 'high', inputSchema: { path: 'relative file path', content: 'file contents' },
    async execute(input) {
      const parsed = relativePath.parse(input.path);
      const target = await within(root, parsed);
      await fs.mkdir(path.dirname(target), { recursive: true });
      await fs.writeFile(target, input.content, 'utf8');
      return { path: parsed, bytes: Buffer.byteLength(input.content) };
    },
  };
  const runCommand: SystemTools['runCommand'] = {
    name: 'run_command', description: 'Run an approved executable in the workspace.', risk: 'high', inputSchema: { command: 'executable', args: 'arguments' },
    async execute(input, signal) {
      if (!shellAllowed) throw new Error('shell execution is disabled by policy');
      const result = await execFileAsync(input.command, [...(input.args ?? [])], { cwd: root, maxBuffer: MAX_OUTPUT, signal });
      return { stdout: result.stdout.slice(0, MAX_OUTPUT), stderr: result.stderr.slice(0, MAX_OUTPUT), code: 0 };
    },
  };
  const gitDiff: SystemTools['gitDiff'] = {
    name: 'git_diff', description: 'Collect the current workspace git diff.', risk: 'low', inputSchema: {},
    async execute(_input, signal) {
      const result = await execFileAsync('git', ['diff', '--no-ext-diff', '--'], { cwd: root, maxBuffer: MAX_OUTPUT, signal });
      return result.stdout.slice(0, MAX_OUTPUT);
    },
  };
  const indexWorkspace: SystemTools['indexWorkspace'] = {
    name: 'index_workspace', description: 'Recursively index non-hidden workspace files.', risk: 'low', inputSchema: {},
    async execute() {
      const files: WorkspaceFile[] = [];
      async function walk(directory: string): Promise<void> {
        for (const entry of await fs.readdir(directory, { withFileTypes: true })) {
          if (entry.name.startsWith('.') || entry.name === 'node_modules') continue;
          const target = path.join(directory, entry.name);
          if (entry.isDirectory()) {
            files.push({ path: path.relative(root, target), bytes: 0, kind: 'directory' });
            await walk(target);
          } else if (entry.isFile()) {
            const stat = await fs.stat(target);
            files.push({ path: path.relative(root, target), bytes: stat.size, kind: 'file', extension: path.extname(entry.name) || undefined });
          }
        }
      }
      await walk(root);
      files.sort((a, b) => a.path.localeCompare(b.path));
      return { root, indexedAt: new Date().toISOString(), files, contextLocked: false };
    },
  };
  return { readFile, writeFile, runCommand, indexWorkspace, gitDiff };
}
