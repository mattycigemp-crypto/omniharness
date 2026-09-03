#!/usr/bin/env node
import React from 'react';
import { render } from 'ink';
import { createMastraEngine } from './agent/mastraEngine.js';
import { TerminalInterface } from './ui/terminalInterface.js';
import { ownVersion, runUpdate } from './update.js';
import { readActiveCombo } from './config/settings.js';
import { OmniRouteClient } from './config/omniRoute.js';
import { doctor, helpText, models } from './doctor.js';
import { debounceResizeEvents } from './ui/resizeDebounce.js';

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
    // Render in the primary buffer, not the alternate screen.
    //
    // The alternate screen has no scrollback — that is what it is for — so
    // while the harness was running you could not scroll back to anything: not
    // a file the agent read three steps ago, not the reasoning behind an edit.
    // The transcript only appeared once you quit, dumped into the primary
    // buffer on exit, which is the wrong time to want it.
    //
    // Staying in the primary buffer means the TUI's <Static> region writes
    // settled turns straight into real scrollback, where the terminal scrolls
    // them like any other output and they are still there afterwards. The exit
    // dump goes with it: scrollback already holds the transcript, and printing
    // it again would duplicate every line.
    //
    // The cost is that quitting no longer restores the pre-launch screen. For
    // a tool whose output you are meant to read back, that is the right trade.
    //
    // Coalesce resize delivery before Ink's own internal listener ever sees
    // it — see resizeDebounce.ts. Only meaningful for a real terminal; a
    // non-TTY stdout never emits 'resize' and patching it would be inert.
    const stdout = process.stdout.isTTY ? debounceResizeEvents(process.stdout, 80) : process.stdout;

    // The app owns Ctrl+C so idle quits but an in-flight run is cancelled first.
    const { waitUntilExit } = render(<TerminalInterface engine={engine} />, { stdout, exitOnCtrlC: false });
    await waitUntilExit();
  })();
}
