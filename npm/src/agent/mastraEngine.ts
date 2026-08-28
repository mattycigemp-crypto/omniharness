import { exec, spawn, type ChildProcess } from 'node:child_process';
import type { LanguageModelV1 } from '@ai-sdk/provider';
import { OmniRouteClient, type ChatTool } from '../config/omniRoute.js';
import { saveActiveCombo } from '../config/settings.js';
import type { AgentMode, ChatWireMessage, HarnessMessage, HarnessState, PreviewServer, ToolCallRequest, ToolCallResult } from '../types/index.js';
import { createSystemTools, type SystemTools } from '../tools/systemTools.js';
import { loadSkills, renderSkillCommand, skillSchema, type Skill } from '../skills.js';

export interface MastraEngineConfig {
  workspaceRoot: string;
  model?: string;
  mode?: AgentMode;
  endpoint?: string;
  apiKey?: string;
  shellAllowed?: boolean;
}

export type HarnessEvent =
  | { type: 'thinking'; text: string }
  | { type: 'thinking_delta'; delta: string }
  | { type: 'text_delta'; delta: string }
  | { type: 'tool_start'; tool: string; input: unknown }
  | { type: 'tool_result'; tool: string; summary: string }
  | { type: 'approval_requested'; tool: string; input: Record<string, unknown> }
  | { type: 'text'; content: string }
  | { type: 'preview'; url: string };

export interface ApprovalAction {
  tool: string;
  input: Record<string, unknown>;
}

export interface MastraEngine {
  readonly client: OmniRouteClient;
  readonly tools: SystemTools;
  readonly state: HarnessState;
  readonly skills: readonly Skill[];
  subscribe(listener: (event: HarnessEvent) => void): () => void;
  selectModel(model: string): Promise<void>;
  setApprovalHandler(handler: (action: ApprovalAction) => Promise<boolean>): void;
  run(prompt: string, signal?: AbortSignal): Promise<{ content: string; model: string }>;
  stop(): void;
}

interface RegisteredTool {
  name: string;
  description: string;
  highRisk: boolean;
  parameters: unknown;
  execute(input: Record<string, unknown>, signal?: AbortSignal): Promise<string>;
}

const VERIFY_RULES =
  '\n\nWORK LOGIC — you MUST follow this discipline when implementing:\n'
  + '1. Read files with read_file before editing them. Never guess at contents.\n'
  + '2. Make the smallest correct change with write_file, then run the relevant checks with run_command '
  + '(tests, typecheck/lint/build — whichever applies to the project you touched).\n'
  + '3. After checks, read back your own changes with git_diff to confirm exactly what you changed.\n'
  + '4. NEVER claim success or report completion until the checks you ran PASS with zero errors. '
  + 'Do not say \"done\", \"works\", or \"all set\" without that evidence.\n'
  + '5. If a check fails, fix the underlying cause and re-run it. Repeat until it is green.\n'
  + '6. If you truly cannot run a check (no test runner present), say so explicitly instead of pretending.\n'
  + '\nSPEED & CONCISION — reason briefly and use tools directly rather than narrating. '
  + 'Read once, act once, verify once. Avoid re-reading the same file repeatedly.';

const MODE_PROMPT: Record<AgentMode, string> = {
  plan: 'You are in PLAN mode: investigate the workspace, name risks and steps, and produce a concrete plan. Do not edit files or run commands. Use read_file, index_workspace, and git_diff.',
  build: 'You are in BUILD mode: implement the request with minimal, correct changes. The full work discipline below is MANDATORY.',
  research: 'You are in RESEARCH mode: investigate and answer with evidence from the workspace. Use read_file, index_workspace, and git_diff. Do not modify files or run commands.',
};

const SYSTEM_FRAME = (endpoint: string, root: string, mode: AgentMode, skillNames: readonly string[]): string =>
  'You are OmniHarness, an autonomous developer agent running inside the user\'s terminal (OmniHarness CLI, powered by the OmniRoute gateway at '
  + `${endpoint}). Workspace: ${root}. Act carefully and concretely; use the provided tools rather than guessing at file contents. `
  + MODE_PROMPT[mode]
  + (mode === 'build' ? VERIFY_RULES : '')
  + (skillNames.length > 0 ? `\nCustom skills available: ${skillNames.map((name) => `\`${name}\``).join(', ')}.` : '');

const MAX_TURNS = 24;
const MAX_OUTPUT = 32_000;
const RISKY_TOOLS = new Set(['write_file', 'run_command', 'start_preview']);

export async function createMastraEngine(config: MastraEngineConfig): Promise<MastraEngine> {
  const client = new OmniRouteClient({ endpoint: config.endpoint, apiKey: config.apiKey });
  const tools = createSystemTools(config.workspaceRoot, config.shellAllowed ?? false);
  const activeModel = config.model ?? 'auto/best-coding';
  const listeners = new Set<(event: HarnessEvent) => void>();
  let preview: PreviewServer | null = null;
  let previewChild: ChildProcess | null = null;
  let approvalHandler: ((action: ApprovalAction) => Promise<boolean>) | null = null;

  const state: HarnessState = {
    taskStatus: 'idle', prompt: '', mode: config.mode ?? 'build', activeModel,
    workspace: { root: config.workspaceRoot, indexedAt: null, files: [], contextLocked: false },
    metrics: client.snapshotMetrics(), messages: [], preview: null,
  };

  const emit = (event: HarnessEvent): void => { for (const listener of listeners) listener(event); };
  const skills = await loadSkills(config.workspaceRoot);

  const systemTools: Record<string, RegisteredTool> = {
    read_file: {
      name: 'read_file', description: tools.readFile.description, highRisk: false, parameters: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
      execute: async (input) => { const r = await tools.readFile.execute({ path: String(input.path) }); return r.content; },
    },
    write_file: {
      name: 'write_file', description: tools.writeFile.description, highRisk: true, parameters: { type: 'object', properties: { path: { type: 'string' }, content: { type: 'string' } }, required: ['path', 'content'] },
      execute: async (input) => { const r = await tools.writeFile.execute({ path: String(input.path), content: String(input.content) }); return `wrote ${r.bytes} bytes to ${r.path}`; },
    },
    run_command: {
      name: 'run_command', description: tools.runCommand.description, highRisk: true, parameters: { type: 'object', properties: { command: { type: 'string' }, args: { type: 'array', items: { type: 'string' } } }, required: ['command'] },
      execute: async (input, signal) => {
        const args = Array.isArray(input.args) ? input.args.map(String) : [];
        const r = await tools.runCommand.execute({ command: String(input.command), args }, signal);
        return `exit ${r.code}\n${r.stdout}${r.stderr ? `\n${r.stderr}` : ''}`;
      },
    },
    index_workspace: {
      name: 'index_workspace', description: tools.indexWorkspace.description, highRisk: false, parameters: { type: 'object', properties: {} },
      execute: async () => {
        const r = await tools.indexWorkspace.execute();
        state.workspace = { ...state.workspace, indexedAt: r.indexedAt, files: r.files };
        const paths = r.files.map((file) => file.path).slice(0, 100).join('\n');
        return `indexed ${r.files.length} entries under ${r.root}:\n${paths}`;
      },
    },
    git_diff: {
      name: 'git_diff', description: tools.gitDiff.description, highRisk: false, parameters: { type: 'object', properties: {} },
      execute: async (_, signal) => { const r = await tools.gitDiff.execute(undefined, signal); return r === '' ? '(no diff)' : r; },
    },
    start_preview: {
      name: 'start_preview', description: 'Start a local preview server for the workspace and report its URL. Provide the command to run.', highRisk: true, parameters: { type: 'object', properties: { command: { type: 'string' }, args: { type: 'array', items: { type: 'string' } }, port: { type: 'integer' } }, required: ['command'] },
      execute: async (input) => {
        const port = typeof input.port === 'number' ? input.port : 4000;
        const args = Array.isArray(input.args) ? input.args.map(String) : [];
        await stopPreviewChild();
        previewChild = spawn(String(input.command), args, { cwd: state.workspace.root, shell: true, stdio: 'pipe' });
        const server = { url: `http://localhost:${port}`, command: String(input.command), startedAt: new Date().toISOString() };
        preview = server;
        state.preview = server;
        emit({ type: 'preview', url: server.url });
        return `preview running at ${server.url}`;
      },
    },
  };

  const execShell = (command: string, options: { cwd: string; timeout: number; maxBuffer: number }): Promise<{ stdout: string; stderr: string }> =>
    new Promise((resolve, reject) => {
      exec(command, options, (error: Error & { stdout?: string | Buffer; stderr?: string | Buffer } | null, stdout: string | Buffer, stderr: string | Buffer) => {
        if (error) {
          error.stdout = stdout;
          error.stderr = stderr;
          reject(error);
        } else {
          resolve({ stdout: String(stdout), stderr: String(stderr) });
        }
      });
    });
  const execSkill = async (script: string): Promise<string> => {
    try {
      const r = await execShell(script, { cwd: state.workspace.root, timeout: 30_000, maxBuffer: MAX_OUTPUT });
      return `exit 0\n${r.stdout}${r.stderr ? `\n${r.stderr}` : ''}`;
    } catch (reason: unknown) {
      const err = reason as { stdout?: string | Buffer | undefined; stderr?: string | Buffer | undefined; code?: number | string | null };
      return `exit ${err.code ?? -1}\n${String(err.stdout ?? '')}\n${String(err.stderr ?? '')}`;
    }
  };
  for (const skill of skills) {
    systemTools[skill.name] = {
      name: skill.name, description: skill.description, highRisk: true, parameters: (skillSchema(skill) as { parameters: unknown }).parameters ?? { type: 'object', properties: {} },
      execute: async (input) => {
        if (!config.shellAllowed) throw new Error('shell execution is disabled; custom skills need shell access');
        const script = renderSkillCommand(skill.command, skill.parameters, input);
        return execSkill(script);
      },
    };
  }

  const toolSchemas: ChatTool[] = Object.values(systemTools).map((entry) => ({
    type: 'function', function: { name: entry.name, description: entry.description, parameters: entry.parameters },
  }));

  async function stopPreviewChild(): Promise<void> {
    if (!previewChild) return;
    previewChild.kill();
    previewChild = null;
  }

  async function runTool(call: ToolCallRequest, signal?: AbortSignal): Promise<string> {
    const registered = systemTools[call.function.name];
    if (!registered) return `error: unknown tool ${call.function.name}`;
    let parsed: Record<string, unknown> = {};
    try { parsed = call.function.arguments ? JSON.parse(call.function.arguments) as Record<string, unknown> : {}; }
    catch { parsed = {}; }
    if (approvalHandler && registered.highRisk) {
      emit({ type: 'approval_requested', tool: call.function.name, input: parsed });
      const approved = await approvalHandler({ tool: call.function.name, input: parsed });
      if (!approved) return 'user denied this tool call';
    }
    try { return await registered.execute(parsed, signal); }
    catch (reason: unknown) { return `error: ${reason instanceof Error ? reason.message : String(reason)}`; }
  }

  return {
    client, tools, state, skills,
    subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
    setApprovalHandler(handler) { approvalHandler = handler; },
    async selectModel(model) {
      state.activeModel = model;
      try { await saveActiveCombo(model); } catch { /* persistence is best-effort */ }
    },
    stop() { void stopPreviewChild(); listeners.clear(); },
    async run(prompt, signal) {
      state.prompt = prompt;
      state.taskStatus = 'running';
      const userMessage: HarnessMessage = { role: 'user', content: prompt, createdAt: new Date().toISOString() };
      state.messages = [...state.messages, userMessage];

      const wire: ChatWireMessage[] = [
        { role: 'system', content: SYSTEM_FRAME(client.endpoint, state.workspace.root, state.mode, skills.map((skill) => skill.name)) },
        ...state.messages.map(asWireMessage),
      ];

      const results: ToolCallResult[] = [];
      let turn = 0;
      let model = activeModel;
      let content = '';
      let reasoning = '';
      let toolCalls: ToolCallRequest[] = [];

      const finishRound = async (): Promise<void> => {
        content = ''; reasoning = ''; toolCalls = [];
        const result = await client.chatStream(state.activeModel, wire, {
          signal, tools: toolSchemas,
          onDelta: (delta) => {
            if (delta.type === 'reasoning') { reasoning += delta.delta; emit({ type: 'thinking_delta', delta: delta.delta }); }
            else if (delta.type === 'text') { content += delta.delta; emit({ type: 'text_delta', delta: delta.delta }); }
            else if (delta.type === 'tool_call') {
              if (!toolCalls.some((call) => call.id === delta.call.id)) { toolCalls.push(delta.call); }
              else { toolCalls = toolCalls.map((call) => call.id === delta.call.id ? delta.call : call); }
            }
          },
        });
        if (result.model) model = result.model;
      };

      await finishRound();
      while (toolCalls.length > 0) {
        if (reasoning) {
          emit({ type: 'thinking', text: reasoning });
          state.messages = [...state.messages, { role: 'thought', content: reasoning, createdAt: new Date().toISOString() }];
        }
        wire.push({ role: 'assistant', content, tool_calls: toolCalls });
        let executedAny = false;
        for (const call of toolCalls) {
          if (signal?.aborted) break;
          turn += 1;
          if (turn > MAX_TURNS) {
            const error = `too many tool turns (limit ${MAX_TURNS})`;
            state.messages = [...state.messages, { role: 'error', content: error, createdAt: new Date().toISOString() }];
            state.taskStatus = 'failed';
            return { content: error + '; latest text: ' + content, model: activeModel };
          }
          emit({ type: 'tool_start', tool: call.function.name, input: undefined });
          const output = await runTool(call, signal);
          const summary = output.split('\n')[0] ?? '';
          emit({ type: 'tool_result', tool: call.function.name, summary });
          results.push({ call, output });
          wire.push({ role: 'tool', tool_call_id: call.id, content: truncate(output) });
          executedAny = true;
        }
        if (!executedAny) break;
        content = ''; reasoning = ''; toolCalls = [];
        const next = await client.chatStream(state.activeModel, wire, {
          signal, tools: toolSchemas,
          onDelta: (delta) => {
            if (delta.type === 'reasoning') { reasoning += delta.delta; emit({ type: 'thinking_delta', delta: delta.delta }); }
            else if (delta.type === 'text') { content += delta.delta; emit({ type: 'text_delta', delta: delta.delta }); }
            else if (delta.type === 'tool_call') {
              if (!toolCalls.some((call) => call.id === delta.call.id)) { toolCalls.push(delta.call); }
              else { toolCalls = toolCalls.map((call) => call.id === delta.call.id ? delta.call : call); }
            }
          },
        });
        if (next.model) model = next.model;
      }

      state.taskStatus = 'completed';
      state.metrics = client.snapshotMetrics();
      const answer = content;
      if (reasoning) {
        emit({ type: 'thinking', text: reasoning });
        state.messages = [...state.messages, { role: 'thought', content: reasoning, createdAt: new Date().toISOString() }];
      }
      for (const result of results) {
        const tool = result.call.function.name;
        state.messages = [...state.messages, { role: 'tool', content: result.output.slice(0, 500), toolName: tool, createdAt: new Date().toISOString() }];
      }
      emit({ type: 'text', content: answer });
      state.messages = [...state.messages, { role: 'assistant', content: answer, model, createdAt: new Date().toISOString() }];
      return { content: answer, model };
    },
  };
}

function asWireMessage(message: HarnessMessage): ChatWireMessage {
  if (message.role === 'user' || message.role === 'assistant') return { role: message.role, content: message.content };
  return { role: 'assistant', content: message.content };
}

function truncate(text: string, max = 4000): string {
  return text.length <= max ? text : `${text.slice(0, max)}\n… (truncated ${text.length - max} chars)`;
}