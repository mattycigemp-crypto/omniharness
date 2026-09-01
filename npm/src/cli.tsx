#!/usr/bin/env node
import React from 'react';
import { render } from 'ink';
import { createMastraEngine } from './agent/mastraEngine.js';
import { TerminalInterface } from './ui/terminalInterface.js';
import { ownVersion, runUpdate } from './update.js';
import { readActiveCombo } from './config/settings.js';
import { OmniRouteClient } from './config/omniRoute.js';
import { doctor, helpText, models } from './doctor.js';
import type { HarnessMessage } from './types/index.js';

const ALT_ENTER = '\x1b[?1049h\x1b[H';
const ALT_LEAVE = '\x1b[?1049l';

/** Plain-text transcript flushed to the primary buffer on exit — the audit trail. */
function dumpTranscript(messages: readonly HarnessMessage[]): void {
  if (messages.length === 0) return;
  const label: Record<HarnessMessage['role'], string> = {
    user: 'you', assistant: 'assistant', thought: 'think', action: 'action',
    tool: 'tool', command: 'command', error: 'error',
  };
  process.stdout.write('\n');
  for (const message of messages) {
    const who = message.model ?? label[message.role] ?? message.role;
    process.stdout.write(`\n─ ${who} ─────\n${message.content.trim()}\n`);
  }
  process.stdout.write('\n');
}

// A crash anywhere below would otherwise surface as a raw Node stack trace, or
// as an unhandled rejection that terminates the process without saying why.
// A CLI should fail with a sentence.
function die(error: unknown): never {
  console.error(`omniharness: ${error instanceof Error ? error.message : String(error)}`);
  process.exit(1);
}
process.on('unhandledRejection', die);
process.on('uncaughtException', die);

const command = process.argv[2];

if (command === 'update') {
  process.exitCode = runUpdate();
} else if (command === '--version' || command === '-v' || command === 'version') {
  console.log(`omniharness ${ownVersion()}`);
} else if (command === '--help' || command === '-h' || command === 'help') {
  console.log(helpText(ownVersion()));
} else if (command === 'doctor') {
  void (async () => {
    const report = await doctor(new OmniRouteClient());
    console.log(report.lines.join('\n'));
    process.exitCode = report.ok ? 0 : 1;
  })();
} else if (command === 'models') {
  void (async () => {
    const client = new OmniRouteClient();
    try {
      console.log((await models(client)).join('\n'));
    } catch (error) {
      // "fetch failed" on its own tells the user nothing they can act on.
      // Name the endpoint that was tried and what to check, the way doctor
      // does — a connection failure here almost always means the gateway is
      // not running or OMNIROUTE_URL points somewhere else.
      const reason = error instanceof Error ? error.message : String(error);
      console.error(`omniharness models: ${reason}`);
      console.error(`  endpoint: ${client.endpoint}`);
      console.error('  check that OmniRoute is running, and that OMNIROUTE_URL points at it');
      console.error("  run 'omniharness doctor' for a full connection report");
      process.exitCode = 1;
    }
  })();
} else if (command !== undefined) {
  // Anything else is a mistake, not an invitation to open the TUI: `omniharness
  // doctr` used to start an interactive session instead of naming the typo.
  console.error(`omniharness: unknown command '${command}'`);
  console.error("run 'omniharness --help' for usage");
  process.exitCode = 1;
} else {
  void (async () => {
    const saved = await readActiveCombo();
    const engine = await createMastraEngine({ workspaceRoot: process.cwd(), model: saved });
    // Render the structured UI in the alternate screen buffer so that, on exit,
    // the primary buffer's scrollback is left holding the plain-text transcript —
    // the raw audit trail, without maintaining a parallel log.
    const alt = process.stdout.isTTY === true;
    if (alt) process.stdout.write(ALT_ENTER);
    // The app owns Ctrl+C so idle quits but an in-flight run is cancelled first.
    const { waitUntilExit } = render(<TerminalInterface engine={engine} />, { exitOnCtrlC: false });
    try {
      await waitUntilExit();
    } finally {
      if (alt) {
        process.stdout.write(ALT_LEAVE);
        dumpTranscript(engine.state.messages);
      }
    }
  })();
}
