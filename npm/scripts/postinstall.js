#!/usr/bin/env node

const { resolveBinary, defaultDownloadURL, cacheDir } = require('../lib/install');

(async () => {
  if (process.env.RUNIC_SKIP_POSTINSTALL === '1') {
    return;
  }

  try {
    const binary = await resolveBinary({ skipDownload: false });
    if (!binary) {
      return;
    }
    console.log(`[runic-wrapper] binary ready: ${binary}`);
  } catch (err) {
    console.warn(`[runic-wrapper] postinstall skipped: ${err.message}`);
    console.warn(`[runic-wrapper] on first run, wrapper will try: ${defaultDownloadURL()}`);
    console.warn(`[runic-wrapper] cache dir: ${cacheDir()}`);
  }
})();
