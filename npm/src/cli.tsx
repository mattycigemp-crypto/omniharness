#!/usr/bin/env node
import React from 'react';
import { render } from 'ink';
import { createMastraEngine } from './agent/mastraEngine.js';
import { TerminalInterface } from './ui/terminalInterface.js';
import { ownVersion, runUpdate } from './update.js';

const command = process.argv[2];

if (command === 'update') {
  process.exitCode = runUpdate();
} else if (command === '--version' || command === '-v' || command === 'version') {
  console.log(`omniharness ${ownVersion()}`);
} else {
  const engine = createMastraEngine({ workspaceRoot: process.cwd() });
  render(<TerminalInterface engine={engine} />);
}
