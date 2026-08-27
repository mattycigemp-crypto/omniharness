#!/usr/bin/env node
import React from 'react';
import { render } from 'ink';
import { createMastraEngine } from './agent/mastraEngine.js';
import { TerminalInterface } from './ui/terminalInterface.js';

const engine = createMastraEngine({ workspaceRoot: process.cwd() });
render(<TerminalInterface engine={engine} />);
