import { Agent } from '@mastra/core/agent';
import { createTool } from '@mastra/core/tools';
import type { LanguageModelV1 } from '@ai-sdk/provider';
import { z } from 'zod';
import { OmniRouteClient } from '../config/omniRoute.js';
import type { AgentToolDeclaration, HarnessMessage, HarnessState } from '../types/index.js';
import { createSystemTools, type SystemTools } from '../tools/systemTools.js';

export interface MastraEngineConfig {
  workspaceRoot: string;
  model?: string;
  endpoint?: string;
  apiKey?: string;
  shellAllowed?: boolean;
}

export interface MastraEngine {
  readonly agent: Agent;
  readonly client: OmniRouteClient;
  readonly tools: SystemTools;
  readonly state: HarnessState;
  run(prompt: string, signal?: AbortSignal): Promise<{ content: string; model: string }>;
}

function mastraTool<TInput extends z.ZodTypeAny, TOutput extends z.ZodTypeAny>(tool: AgentToolDeclaration<z.infer<TInput>, z.infer<TOutput>>, inputSchema: TInput, outputSchema: TOutput) {
  return createTool({
    id: tool.name,
    description: tool.description,
    inputSchema,
    outputSchema,
    execute: (input: z.infer<TInput>) => tool.execute(input),
  });
}

function omniRouteModel(client: OmniRouteClient, model: string): LanguageModelV1 {
  return {
    specificationVersion: 'v1',
    provider: 'omniroute',
    modelId: model,
    defaultObjectGenerationMode: 'json',
    doGenerate: async (options) => {
      const messages: Array<{ role: 'system' | 'user' | 'assistant'; content: string }> = [];
      for (const part of options.prompt) {
        if (part.role === 'system') messages.push({ role: 'system', content: part.content });
        else if (part.role === 'user' || part.role === 'assistant') {
          const content = typeof part.content === 'string' ? part.content : part.content.map((item) => item.type === 'text' ? item.text : '').join('');
          messages.push({ role: part.role, content });
        }
      }
      const result = await client.chat(model, messages, options.abortSignal);
      return {
        text: result.content,
        finishReason: 'stop',
        usage: {
          promptTokens: result.usage?.inputTokens ?? 0,
          completionTokens: result.usage?.outputTokens ?? 0,
        },
        rawCall: { rawPrompt: JSON.stringify(options.prompt), rawSettings: {} },
        rawResponse: { headers: Object.fromEntries(result.headers.entries()) },
        warnings: [],
      };
    },
    doStream: async () => { throw new Error('streaming is not enabled by this adapter'); },
  };
}

export function createMastraEngine(config: MastraEngineConfig): MastraEngine {
  const client = new OmniRouteClient({ endpoint: config.endpoint, apiKey: config.apiKey });
  const tools = createSystemTools(config.workspaceRoot, config.shellAllowed ?? false);
  const activeModel = config.model ?? 'auto/best-coding';
  const agent = new Agent({
    name: 'omniharness-developer',
    instructions: 'You are OmniHarness, an autonomous developer agent running in the user\'s terminal and routed through the OmniRoute gateway. Inspect context before editing. Explain actions briefly, use tools only inside the workspace, and never claim verification without running it.',
    model: omniRouteModel(client, activeModel),
    tools: {
      read_file: mastraTool(tools.readFile, z.object({ path: z.string().min(1) }), z.object({ path: z.string(), content: z.string() })),
      write_file: mastraTool(tools.writeFile, z.object({ path: z.string().min(1), content: z.string() }), z.object({ path: z.string(), bytes: z.number() })),
      run_command: mastraTool(tools.runCommand, z.object({ command: z.string().min(1), args: z.array(z.string()).optional() }), z.object({ stdout: z.string(), stderr: z.string(), code: z.number() })),
      index_workspace: mastraTool(tools.indexWorkspace, z.void(), z.object({ root: z.string(), indexedAt: z.string().nullable(), files: z.array(z.unknown()).readonly(), contextLocked: z.boolean() })),
      git_diff: mastraTool(tools.gitDiff, z.void(), z.string()),
    },
  });
  const state: HarnessState = {
    taskStatus: 'idle', prompt: '', activeModel,
    workspace: { root: config.workspaceRoot, indexedAt: null, files: [], contextLocked: false },
    metrics: client.snapshotMetrics(), messages: [],
  };
  return {
    agent, client, tools, state,
    async run(prompt, signal) {
      state.prompt = prompt;
      state.taskStatus = 'running';
      const messages: HarnessMessage[] = [...state.messages, { role: 'user', content: prompt, createdAt: new Date().toISOString() }];
      const payload = [
        { role: 'system' as const, content: `You are OmniHarness, an autonomous developer agent running inside the user's terminal (the OmniHarness CLI, powered by the OmniRoute gateway at ${client.endpoint}). Current workspace: ${state.workspace.root}. Be concise and act carefully.` },
        ...messages.map(({ role, content }) => ({ role: role === 'user' ? 'user' as const : 'assistant' as const, content })),
      ];
      const result = await client.chat(state.activeModel, payload, signal);
      state.taskStatus = 'completed';
      state.metrics = client.snapshotMetrics();
      state.messages = [...messages, { role: 'assistant', content: result.content, createdAt: new Date().toISOString() }];
      return { content: result.content, model: result.model };
    },
  };
}
