const fs = require('fs');
const fsp = require('fs/promises');
const os = require('os');
const path = require('path');
const https = require('https');
const crypto = require('crypto');

function platformInfo() {
  const p = process.platform;
  const a = process.arch;

  const platformMap = {
    darwin: 'darwin',
    linux: 'linux',
    win32: 'windows'
  };

  const archMap = {
    x64: 'amd64',
    arm64: 'arm64'
  };

  const platform = platformMap[p];
  const arch = archMap[a];
  if (!platform) {
    throw new Error(`unsupported platform: ${p}`);
  }
  if (!arch) {
    throw new Error(`unsupported architecture: ${a}`);
  }

  return {
    platform,
    arch,
    exeExt: platform === 'windows' ? '.exe' : ''
  };
}

function assetName() {
  const info = platformInfo();
  return `runic-${info.platform}-${info.arch}${info.exeExt}`;
}

function cacheDir() {
  if (process.env.RUNIC_CACHE_DIR) {
    return process.env.RUNIC_CACHE_DIR;
  }
  return path.join(os.homedir(), '.runic', 'bin');
}

function cachedBinaryPath() {
  return path.join(cacheDir(), assetName());
}

function defaultDownloadURL() {
  if (process.env.RUNIC_BINARY_URL) {
    return process.env.RUNIC_BINARY_URL;
  }
  const base = process.env.RUNIC_RELEASE_BASE_URL || 'https://github.com/cosmobean/runic/releases';
  return `${base}/latest/download/${assetName()}`;
}

async function pathExists(p) {
  try {
    await fsp.access(p, fs.constants.F_OK);
    return true;
  } catch {
    return false;
  }
}

async function isExecutable(p) {
  if (process.platform === 'win32') {
    return pathExists(p);
  }
  try {
    await fsp.access(p, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function localCandidates() {
  const ext = platformInfo().exeExt;
  return [
    path.resolve(process.cwd(), `runic${ext}`),
    path.resolve(__dirname, '..', '..', `runic${ext}`)
  ];
}

async function findLocalBinary() {
  for (const p of localCandidates()) {
    if (await pathExists(p) && await isExecutable(p)) {
      return p;
    }
  }
  return null;
}

function streamToFile(stream, outPath) {
  return new Promise((resolve, reject) => {
    const out = fs.createWriteStream(outPath);
    stream.pipe(out);
    stream.on('error', reject);
    out.on('error', reject);
    out.on('finish', () => resolve());
  });
}

function download(url, outPath, redirectsLeft = 5) {
  return new Promise((resolve, reject) => {
    const req = https.get(url, (res) => {
      const status = res.statusCode || 0;
      if ([301, 302, 303, 307, 308].includes(status)) {
        if (!res.headers.location) {
          reject(new Error(`redirect without location header from ${url}`));
          return;
        }
        if (redirectsLeft <= 0) {
          reject(new Error(`too many redirects fetching ${url}`));
          return;
        }
        const next = new URL(res.headers.location, url).toString();
        res.resume();
        download(next, outPath, redirectsLeft - 1).then(resolve, reject);
        return;
      }

      if (status !== 200) {
        reject(new Error(`download failed (${status}) from ${url}`));
        res.resume();
        return;
      }

      streamToFile(res, outPath).then(resolve, reject);
    });

    req.on('error', reject);
    req.setTimeout(30000, () => {
      req.destroy(new Error('download timeout'));
    });
  });
}

async function maybeVerifySha256(filePath) {
  const expected = process.env.RUNIC_BINARY_SHA256;
  if (!expected) {
    return;
  }
  const b = await fsp.readFile(filePath);
  const actual = crypto.createHash('sha256').update(b).digest('hex');
  if (actual.toLowerCase() !== expected.toLowerCase()) {
    throw new Error(`binary checksum mismatch: expected ${expected}, got ${actual}`);
  }
}

async function downloadBinary() {
  const outDir = cacheDir();
  await fsp.mkdir(outDir, { recursive: true, mode: 0o755 });

  const binPath = cachedBinaryPath();
  const tmpPath = `${binPath}.tmp-${process.pid}`;
  const url = defaultDownloadURL();

  await download(url, tmpPath);
  await maybeVerifySha256(tmpPath);

  if (process.platform !== 'win32') {
    await fsp.chmod(tmpPath, 0o755);
  }
  await fsp.rename(tmpPath, binPath);
  return binPath;
}

async function resolveBinary(opts = {}) {
  const skipDownload = Boolean(opts.skipDownload);

  if (process.env.RUNIC_BINARY_PATH) {
    const p = path.resolve(process.env.RUNIC_BINARY_PATH);
    if (!await pathExists(p)) {
      throw new Error(`RUNIC_BINARY_PATH does not exist: ${p}`);
    }
    return p;
  }

  const local = await findLocalBinary();
  if (local) {
    return local;
  }

  const cached = cachedBinaryPath();
  if (await pathExists(cached)) {
    return cached;
  }

  if (skipDownload) {
    return null;
  }

  return downloadBinary();
}

module.exports = {
  resolveBinary,
  assetName,
  defaultDownloadURL,
  cacheDir,
  cachedBinaryPath
};
