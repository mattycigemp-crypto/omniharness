#!/usr/bin/env node
'use strict';

const path = require('node:path');

import(path.join(__dirname, 'dist', 'cli.js')).catch((error) => {
  console.error(`omniharness-cli: failed to launch: ${error instanceof Error ? error.message : String(error)}`);
  process.exitCode = 1;
});
