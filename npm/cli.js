#!/usr/bin/env node
'use strict';

const { spawnSync } = require('node:child_process');
const path = require('node:path');

const result = spawnSync(process.execPath, [path.join(__dirname, 'dist', 'cli.js'), ...process.argv.slice(2)], {
  stdio: 'inherit',
});

if (result.error) {
  console.error(`omniharness-cli: failed to launch: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status ?? 1);
