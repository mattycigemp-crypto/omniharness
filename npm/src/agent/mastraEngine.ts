import { exec, spawn, type ChildProcess } from 'node:child_process';
import { appendFile, mkdir, readdir, readFile, stat } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import type { LanguageModelV1 } from '@ai-sdk/provider';
import { attachmentBlock, kindFromName, type AttachmentInput } from '../attachments.js';
import { OmniRouteClient, type ChatTool, type CompressionInfo, type McpToolDescriptor } from '../config/omniRoute.js';
import { saveActiveCombo } from '../config/settings.js';
import type { AgentMode, ChatContentPart, ChatWireMessage, HarnessMessage, HarnessState, PreviewServer, TodoAction, TodoItem, TodoSnapshot, ToolCallRequest, ToolCallResult } from '../types/index.js';
import { createSystemTools, type SystemTools } from '../tools/systemTools.js';
import { loadSkills, renderSkillCommand, skillSchema, type Skill } from '../skills.js';
import { chunkText, cosineSimilarity, type IndexedChunk } from '../search.js';
import { loadSemanticIndex, saveSemanticIndex, type SemanticCacheEntry } from '../semanticStore.js';

export interface MastraEngineConfig {
  workspaceRoot: string;
  model?: string;
  mode?: AgentMode;
  endpoint?: string;
  apiKey?: string;
  /** OmniRoute management token: when set, OmniRoute MCP tools are discovered and exposed to the agent. */
  mgmtToken?: string;
  shellAllowed?: boolean;
}

export type HarnessEvent =
  | { type: 'thinking'; text: string }
  | { type: 'thinking_delta'; delta: string }
  | { type: 'text_delta'; delta: string }
  | { type: 'tool_start'; tool: string; input: unknown }
  | { type: 'tool_result'; tool: string; summary: string }
  | { type: 'approval_requested'; tool: string; input: Record<string, unknown> }
  | { type: 'text'; content: string; model?: string; compression?: CompressionInfo }
  | { type: 'preview'; url: string }
  | { type: 'attach'; name: string; kind: AttachmentInput['kind']; size: number }
  | { type: 'todos'; todos: readonly TodoItem[] };

export interface ApprovalAction {
  tool: string;
  input: Record<string, unknown>;
}

export interface MastraEngine {
  readonly client: OmniRouteClient;
  readonly tools: SystemTools;
  readonly state: HarnessState;
  readonly skills: readonly Skill[];
  readonly mcpTools: readonly McpToolDescriptor[];
  subscribe(listener: (event: HarnessEvent) => void): () => void;
  selectModel(model: string): Promise<void>;
  attach(paths: readonly string[]): Promise<readonly AttachmentInput[]>;
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
  crazy: 'You are in CRAZY MODE: a fully autonomous agent (OpenClaw/Hermes style) that works continuously without asking for permission — every tool call is auto-approved. Decide your own next steps, keep the visible task queue current with update_todo, persist important facts with write_memory, and keep working until the task is genuinely complete. Verify your own work and iterate.',
};

const MEMORY_FILE = 'memory.md';

const SYSTEM_FRAME = (endpoint: string, root: string, mode: AgentMode, skillNames: readonly string[], memory: string): string =>
  'You are OmniHarness, an autonomous developer agent running inside the user\'s terminal (OmniHarness CLI, powered by the OmniRoute gateway at '
  + `${endpoint}). Workspace: ${root}. Act carefully and concretely; use the provided tools rather than guessing at file contents. `
  + MODE_PROMPT[mode]
  + (mode === 'build' ? VERIFY_RULES : '')
  + (mode === 'crazy' && memory !== '' ? `\n\nPERSISTENT MEMORY (from previous sessions):\n${memory}` : '')
  + (skillNames.length > 0 ? `\nCustom skills available: ${skillNames.map((name) => `\`${name}\``).join(', ')}.` : '');

const MAX_TURNS: Record<AgentMode, number> = { plan: 12, build: 24, research: 12, crazy: 120 };
const MAX_OUTPUT = 32_000;
const RISKY_TOOLS = new Set(['write_file', 'run_command', 'start_preview']);

export async function createMastraEngine(config: MastraEngineConfig): Promise<MastraEngine> {
  const client = new OmniRouteClient({ endpoint: config.endpoint, apiKey: config.apiKey, mgmtToken: config.mgmtToken });
  const tools = createSystemTools(config.workspaceRoot, config.shellAllowed ?? false);
  const activeModel = config.model ?? 'auto/best-coding';
  const listeners = new Set<(event: HarnessEvent) => void>();
  let preview: PreviewServer | null = null;
  let previewChild: ChildProcess | null = null;
  let approvalHandler: ((action: ApprovalAction) => Promise<boolean>) | null = null;
  let pendingAttachments: readonly (AttachmentInput & { dataUrl?: string })[] = [];
  const semanticCache = await loadSemanticIndex(config.workspaceRoot);

  const state: HarnessState = {
    taskStatus: 'idle', prompt: '', mode: config.mode ?? 'build', activeModel,
    workspace: { root: config.workspaceRoot, indexedAt: null, files: [], contextLocked: false },
    metrics: client.snapshotMetrics(), messages: [], preview: null, taskQueue: [],
  };

  const memoryPath = join(state.workspace.root, '.omniharness', MEMORY_FILE);

  let memory = '';
  try { memory = (await readFile(memoryPath, 'utf8')).trim(); } catch { /* no memory yet */ }

  const emitTodos = (): void => {
    const snapshot: TodoSnapshot = { todos: [...state.taskQueue], updatedAt: new Date().toISOString() };
    state.lastTodoUpdate = snapshot;
    emit({ type: 'todos', todos: snapshot.todos });
  };

  const applyTodo = (action: TodoAction): string => {
    const queue = [...state.taskQueue];
    switch (action.action) {
      case 'add': {
        const id = `t${Date.now().toString(36)}${queue.length.toString(36)}`;
        queue.push({ id, title: action.title, status: 'pending' });
        state.taskQueue = queue;
        emitTodos();
        return `todo added: ${action.title}`;
      }
      case 'update': {
        const item = queue.find((entry) => entry.id === action.id);
        if (!item) return `error: no todo with id ${action.id}`;
        item.title = action.title ?? item.title;
        state.taskQueue = queue;
        emitTodos();
        return `todo updated: ${item.title}`;
      }
      case 'start': {
        const target = (action.id ? queue.find((entry) => entry.id === action.id) : undefined) ?? queue.find((entry) => entry.status === 'pending');
        if (!target) return 'error: no pending todo to start';
        for (const entry of queue) entry.status = entry.id === target.id ? 'active' : entry.status === 'active' ? 'pending' : entry.status;
        state.taskQueue = queue;
        emitTodos();
        return `started: ${target.title}`;
      }
      case 'complete': {
        const target = (action.id ? queue.find((entry) => entry.id === action.id) : undefined) ?? queue.find((entry) => entry.status !== 'done');
        if (!target) return 'error: no todo to complete';
        target.status = 'done';
        state.taskQueue = queue;
        emitTodos();
        return `completed: ${target.title}`;
      }
      case 'remove': {
        const before = queue.length;
        state.taskQueue = queue.filter((entry) => entry.id !== action.id);
        if (state.taskQueue.length === before) return `error: no todo with id ${action.id}`;
        emitTodos();
        return `todo removed`;
      }
      default: return 'error: unknown todo action';
    }
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
    semantic_search: {
      name: 'semantic_search', description: 'Embed the workspace and search it by meaning, returning the most relevant files with matching snippets. Use instead of guessing which file holds a concept.', highRisk: false, parameters: { type: 'object', properties: { query: { type: 'string' }, limit: { type: 'integer' }, refresh: { type: 'boolean' } }, required: ['query'] },
      execute: async (input, signal) => {
        const query = String(input.query ?? '').trim();
        if (query === '') return 'error: query is required';
        if (input.refresh === true) semanticCache.clear();
        const limit = typeof input.limit === 'number' && input.limit > 0 ? Math.min(input.limit, 10) : 5;
        const chunks = await indexWorkspaceSemantics(client, semanticCache, state.workspace.root);
        void saveSemanticIndex(state.workspace.root, semanticCache).catch(() => { /* persistence best-effort */ });
        if (chunks.length === 0) return 'no indexable text files in the workspace';
        const [queryVector] = await client.embed([query], undefined, signal);
        const ranked = chunks
          .map((chunk) => ({ chunk, score: cosineSimilarity(queryVector, chunk.embedding) }))
          .sort((a, b) => b.score - a.score)
          .slice(0, limit);
        return ranked.map(({ chunk, score }) => `${chunk.path} (${score.toFixed(3)})\n  ${chunk.text.slice(0, 200)}`).join('\n');
      },
    },
    update_todo: {
      name: 'update_todo', description: 'Maintain the visible task queue the user watches: add a step, start it, complete it, or remove it. Keep steps small and current as you work.', highRisk: false, parameters: { type: 'object', properties: { action: { type: 'string', enum: ['add', 'update', 'start', 'complete', 'remove'] }, title: { type: 'string' }, id: { type: 'string' } }, required: ['action'] },
      execute: async (input) => applyTodo(input as TodoAction),
    },
    write_memory: {
      name: 'write_memory', description: 'Persist a fact to long-term memory so future sessions remember it. Use for decisions, learned constraints, and project state worth keeping.', highRisk: false, parameters: { type: 'object', properties: { fact: { type: 'string' } }, required: ['fact'] },
      execute: async (input) => {
        const fact = String(input.fact ?? '').trim();
        if (fact === '') return 'error: fact is required';
        await mkdir(join(state.workspace.root, '.omniharness'), { recursive: true });
        await appendFile(memoryPath, `- ${new Date().toISOString()}: ${fact}\n`, 'utf8');
        memory = `${memory}${memory === '' ? '' : '\n'}- ${fact}`;
        return 'memory saved';
      },
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

  // When an OmniRoute management token is configured, discover the gateway's MCP
  // tools and expose them to the agent. Best-effort: a discovery failure (gateway
  // down, disabled MCP transport, wrong scopes) simply leaves the built-in tools.
  let mcpTools: readonly McpToolDescriptor[] = [];
  if (client.hasMcpToken) {
    try {
      mcpTools = await client.listMcpTools();
      for (const descriptor of mcpTools) {
        if (systemTools[descriptor.name]) continue; // never shadow a built-in tool
        systemTools[descriptor.name] = {
          name: descriptor.name, description: descriptor.description ?? '', highRisk: false,
          parameters: descriptor.inputSchema ?? { type: 'object', properties: {} },
          execute: (input, signal) => client.callMcpTool(descriptor.name, input, signal),
        };
      }
    } catch {
      mcpTools = [];
    }
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
    if (state.mode !== 'crazy' && approvalHandler && registered.highRisk) {
      emit({ type: 'approval_requested', tool: call.function.name, input: parsed });
      const approved = await approvalHandler({ tool: call.function.name, input: parsed });
      if (!approved) return 'user denied this tool call';
    }
    try { return await registered.execute(parsed, signal); }
    catch (reason: unknown) { return `error: ${reason instanceof Error ? reason.message : String(reason)}`; }
  }

  return {
    client, tools, state, skills, mcpTools,
    subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
    setApprovalHandler(handler) { approvalHandler = handler; },
    async selectModel(model) {
      state.activeModel = model;
      try { await saveActiveCombo(model); } catch { /* persistence is best-effort */ }
    },
    async attach(paths) {
      const loaded: (AttachmentInput & { dataUrl?: string })[] = [];
      const failures: string[] = [];
      for (const raw of paths) {
        const full = resolve(state.workspace.root, raw);
        try {
          const info = await stat(full);
          if (!info.isFile()) { failures.push(`${raw}: not a file`); continue; }
          const kind = kindFromName(raw);
          const base: AttachmentInput = { name: raw, size: info.size, kind };
          const attachment: typeof base & { dataUrl?: string } = base;
          if (kind === 'image' && info.size <= 10 * 1024 * 1024) {
            const bytes = await readFile(full);
            attachment.dataUrl = `data:image/${mimeFromName(raw)};base64,${bytes.toString('base64')}`;
          }
          loaded.push(attachment);
          emit({ type: 'attach', name: raw, kind, size: info.size });
        } catch (reason: unknown) {
          failures.push(`${raw}: ${reason instanceof Error ? reason.message : String(reason)}`);
        }
      }
      if (failures.length > 0) throw new Error(`attach failed: ${failures.join('; ')}`);
      pendingAttachments = loaded;
      return loaded.map(({ dataUrl: _dataUrl, ...rest }) => rest);
    },
    stop() { void stopPreviewChild(); listeners.clear(); },
    async run(prompt, signal) {
      state.prompt = prompt;
      state.taskStatus = 'running';
      const userMessage: HarnessMessage = { role: 'user', content: prompt, createdAt: new Date().toISOString() };
      state.messages = [...state.messages, userMessage];

      const wire: ChatWireMessage[] = [
        { role: 'system', content: SYSTEM_FRAME(client.endpoint, state.workspace.root, state.mode, skills.map((skill) => skill.name), memory) },
        ...state.messages.map(asWireMessage),
      ];
      if (pendingAttachments.length > 0) {
        const note = attachmentBlock(pendingAttachments);
        const images: ChatContentPart[] = pendingAttachments
          .filter((a): a is AttachmentInput & { dataUrl: string } => a.dataUrl !== undefined)
          .map((a) => ({ type: 'image_url', image_url: { url: a.dataUrl } }));
        wire[wire.length - 1] = images.length > 0
          ? { role: 'user', content: [{ type: 'text', text: `${note}${prompt}` }, ...images] }
          : { role: 'user', content: `${note}${prompt}` };
        pendingAttachments = [];
      }

      const results: ToolCallResult[] = [];
      let turn = 0;
      let model = activeModel;
      let content = '';
      let reasoning = '';
      let toolCalls: ToolCallRequest[] = [];
      let compression: CompressionInfo | undefined;

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
        if (result.compression) compression = result.compression;
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
          if (turn > MAX_TURNS[state.mode]) {
            const error = `too many tool turns (limit ${MAX_TURNS[state.mode]})`;
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
        if (next.compression) compression = next.compression;
      }

      state.taskStatus = 'completed';
      state.metrics = client.snapshotMetrics();
      if (state.mode === 'crazy') {
        const summary = content.split('\n')[0]?.slice(0, 160) ?? '';
        try {
          await mkdir(join(state.workspace.root, '.omniharness'), { recursive: true });
          await appendFile(memoryPath, `- ${new Date().toISOString()}: ran "${prompt.slice(0, 80)}" → ${summary}\n`, 'utf8');
        } catch { /* memory persistence is best-effort */ }
      }
      const answer = content;
      if (reasoning) {
        emit({ type: 'thinking', text: reasoning });
        state.messages = [...state.messages, { role: 'thought', content: reasoning, createdAt: new Date().toISOString() }];
      }
      for (const result of results) {
        const tool = result.call.function.name;
        state.messages = [...state.messages, { role: 'tool', content: result.output.slice(0, 500), toolName: tool, createdAt: new Date().toISOString() }];
      }
      emit({ type: 'text', content: answer, model, compression });
      state.messages = [...state.messages, { role: 'assistant', content: answer, model, createdAt: new Date().toISOString() }];
      return { content: answer, model };
    },
  };
}

function asWireMessage(message: HarnessMessage): ChatWireMessage {
  if (message.role === 'user' || message.role === 'assistant') return { role: message.role, content: message.content };
  return { role: 'assistant', content: message.content };
}

function mimeFromName(name: string): string {
  const ext = name.slice(name.lastIndexOf('.') + 1).toLowerCase();
  return ext === 'jpg' ? 'jpeg' : ext === 'svg' ? 'svg+xml' : ext;
}

function truncate(text: string, max = 4000): string {
  return text.length <= max ? text : `${text.slice(0, max)}\n… (truncated ${text.length - max} chars)`;
}

const MAX_INDEX_FILES = 200;
const MAX_INDEX_BYTES = 256 * 1024;

/** Reuse cached embeddings for unchanged files; embed new/changed ones, batched. */
async function indexWorkspaceSemantics(client: OmniRouteClient, cache: Map<string, SemanticCacheEntry>, root: string): Promise<IndexedChunk[]> {
  const files: { path: string; mtimeMs: number }[] = [];
  async function walk(directory: string): Promise<void> {
    if (files.length >= MAX_INDEX_FILES) return;
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      if (files.length >= MAX_INDEX_FILES) return;
      if (entry.name.startsWith('.') || entry.name === 'node_modules') continue;
      const target = resolve(directory, entry.name);
      if (entry.isDirectory()) await walk(target);
      else if (entry.isFile()) {
        try {
          const info = await stat(target);
          if (info.size > 0 && info.size <= MAX_INDEX_BYTES) files.push({ path: target, mtimeMs: info.mtimeMs });
        } catch { /* unreadable file: skip */ }
      }
    }
  }
  await walk(root);
  const results: IndexedChunk[] = [];
  const changed = new Map<string, { mtimeMs: number; chunks: IndexedChunk[] }>();
  for (const file of files) {
    const cached = cache.get(file.path);
    if (cached && cached.mtimeMs === file.mtimeMs) {
      results.push(...cached.chunks);
      continue;
    }
    try {
      const text = await readFile(file.path, 'utf8');
      changed.set(file.path, { mtimeMs: file.mtimeMs, chunks: [] });
      for (const piece of chunkText(text)) changed.get(file.path)!.chunks.push({ path: file.path, text: piece, embedding: [] });
    } catch { /* binary/unreadable: skip */ }
  }
  const toEmbed: IndexedChunk[] = [];
  for (const entry of changed.values()) toEmbed.push(...entry.chunks);
  for (let i = 0; i < toEmbed.length; i += 16) {
    const batch = toEmbed.slice(i, i + 16);
    const vectors = await client.embed(batch.map((chunk) => chunk.text));
    batch.forEach((chunk, index) => { chunk.embedding = vectors[index]!; });
  }
  for (const file of files) {
    const entry = changed.get(file.path);
    if (entry) {
      cache.set(file.path, entry);
      results.push(...entry.chunks);
    }
  }
  // Drop cached entries for files no longer present so the persisted index stays clean.
  const present = new Set(files.map((file) => file.path));
  for (const path of [...cache.keys()]) {
    if (!present.has(path)) cache.delete(path);
  }
  return results;
}