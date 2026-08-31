#!/usr/bin/env node
import React from 'react';
import { render } from 'ink';
import { createMastraEngine } from './agent/mastraEngine.js';
import { TerminalInterface } from './ui/terminalInterface.js';
import { ownVersion, runUpdate } from './update.js';
import { readActiveCombo } from './config/settings.js';

const command = process.argv[2];

if (command === 'update') {
  process.exitCode = runUpdate();
} else if (command === '--version' || command === '-v' || command === 'version') {
  console.log(`omniharness ${ownVersion()}`);
} else {
  void (async () => {
    const saved = await readActiveCombo();
    const    engine = await createMastraEngine({ workspaceRoot: process.cwd(), model: saved });
    // The app owns Ctrl+C so idle quits but an in-flight run is cancelled first.
    render(<TerminalInterface engine={engine} />, { exitOnCtrlC: false });
  })();
}
