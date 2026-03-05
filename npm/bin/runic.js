#!/usr/bin/env node

const { spawn } = require('child_process');
const { resolveBinary } = require('../lib/install');

(async () => {
  try {
    const bin = await resolveBinary();
    const args = process.argv.slice(2);

    const child = spawn(bin, args, {
      stdio: 'inherit',
      env: process.env
    });

    child.on('error', (err) => {
      console.error(`[runic-wrapper] failed to start binary: ${err.message}`);
      process.exit(1);
    });

    child.on('exit', (code, signal) => {
      if (signal) {
        process.kill(process.pid, signal);
        return;
      }
      process.exit(code ?? 0);
    });
  } catch (err) {
    console.error(`[runic-wrapper] ${err.message}`);
    process.exit(1);
  }
})();
