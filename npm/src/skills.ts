import { promises as fs } from 'node:fs';
import path from 'node:path';

export interface SkillParam {
  name: string;
  type: 'string' | 'number' | 'boolean';
}

export interface Skill {
  name: string;
  description: string;
  command: string;
  parameters: readonly SkillParam[];
}

/**
 * Parse an OMNIHARNESS.md skills definition. Blocks start with `## <name>`
 * and contain `description:`, `command:`, and `param: <name> <type>` fields.
 * Command placeholders `{name}` are filled from the tool arguments at run time.
 */
export function parseSkills(text: string): Skill[] {
  const lines = text.split(/\r?\n/);
  const skills: Skill[] = [];
  let current: { name: string; description: string; command: string; parameters: SkillParam[] } | null = null;

  for (const raw of lines) {
    const line = raw.trimEnd();
    if (line.startsWith('## ')) {
      if (current?.name) skills.push(current as Skill);
      current = { name: line.slice(3).trim(), description: '', command: '', parameters: [] };
      continue;
    }
    if (!current) continue;
    const desc = /^description:\s*(.+)$/i.exec(line);
    if (desc) { current.description = desc[1]!.trim(); continue; }
    const cmd = /^command:\s*(.+)$/i.exec(line);
    if (cmd) { current.command = cmd[1]!.trim(); continue; }
    const param = /^param:\s+(\S+)\s+(string|number|boolean)$/i.exec(line);
    if (param) { current.parameters.push({ name: param[1]!, type: param[2]!.toLowerCase() as SkillParam['type'] }); continue; }
  }
  if (current?.name) skills.push(current as Skill);
  return skills.filter((skill) => skill.command !== '').map((skill) => skill);
}

/** Load and parse OMNIHARNESS.md from the workspace root, if present. */
export async function loadSkills(workspaceRoot: string): Promise<Skill[]> {
  try {
    const raw = await fs.readFile(path.join(workspaceRoot, 'OMNIHARNESS.md'), 'utf8');
    return parseSkills(raw);
  } catch {
    return [];
  }
}

function jsonTypeOf(value: unknown, type: SkillParam['type']): unknown {
  switch (type) {
    case 'number': {
      const n = Number(value);
      return Number.isFinite(n) ? n : 0;
    }
    case 'boolean': return value === true || value === 'true' || value === 1 || value === '1';
    default: return String(value ?? '');
  }
}

/**
 * Render the shell command for a skill call by substituting `{param}` tokens
 * with the typed argument values.
 */
export function renderSkillCommand(command: string, parameters: readonly SkillParam[], input: Record<string, unknown>): string {
  let rendered = command;
  for (const param of parameters) {
    const value = jsonTypeOf(input[param.name], param.type);
    rendered = rendered.split(`{${param.name}}`).join(typeof value === 'string' ? value : String(value));
  }
  return rendered.trim();
}

/** Build an OpenAI `function` parameters schema for a skill. */
export function skillSchema(skill: Skill): { name: string; description: string; parameters: unknown } {
  const properties: Record<string, { type: 'string' | 'number' | 'boolean' }> = {};
  const required: string[] = [];
  for (const param of skill.parameters) {
    properties[param.name] = { type: param.type };
    required.push(param.name);
  }
  return { name: skill.name, description: skill.description, parameters: { type: 'object', properties, required } };
}