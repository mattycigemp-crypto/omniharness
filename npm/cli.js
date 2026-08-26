#!/usr/bin/env node
// omniharness-cli launcher: runs the platform-specific prebuilt binary.
// The binary is self-contained; this shim only locates and spawns it.
'use strict';

const { spawnSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const platform = process.platform;
const arch = process.arch;
const exe = platform === 'win32' ? 'omniharness.exe' : 'omniharness';
const binPath = path.join(__dirname, 'vendor', `${platform}-${arch}`, exe);

if (!fs.existsSync(binPath)) {
  console.error(
    `omniharness-cli: no prebuilt binary for ${platform}-${arch}.\n` +
      'This package ships binaries for the platforms listed in its package.json ' +
      '"os"/"cpu" fields. Build one with scripts/release-npm.sh.'
  );
  process.exit(1);
}

const result = spawnSync(binPath, process.argv.slice(2), { stdio: 'inherit' });
if (result.error) {
  console.error(`omniharness-cli: failed to launch ${binPath}: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
