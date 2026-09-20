const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { DatabaseSync } = require('node:sqlite');
const { spawn } = require('node:child_process');

function endpointFromStore(data) {
  const store = new DatabaseSync(path.join(data, 'cursor-byok.db'), { readOnly: true });
  try {
    const row = store.prepare("SELECT value_json FROM service_settings WHERE setting_key='network_ports'").get();
    const port = row && JSON.parse(row.value_json).service_port;
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      throw new Error('Start Cursor BYOK once to initialize its service port.');
    }
    return `http://127.0.0.1:${port}`;
  } finally {
    store.close();
  }
}

function initializeConfig(data, platform = process.platform) {
  const directory = path.join(data, 'cli');
  fs.mkdirSync(directory, { recursive: true });
  const config = {
    version: 1,
    editor: { vimMode: false },
    permissions: { allow: [], deny: [] },
    approvalMode: 'allowlist',
    network: { useHttp1ForAgent: true },
  };
  // Only native Windows lacks the CLI sandbox; retain the platform default on Mac.
  if (platform === 'win32') {
    config.sandbox = { mode: 'disabled', networkAccess: 'user_config_with_defaults' };
  }
  try {
    fs.writeFileSync(path.join(directory, 'cli-config.json'), JSON.stringify(config, null, 2) + '\n', { flag: 'wx' });
  } catch (error) {
    if (error.code !== 'EEXIST') throw error;
  }
}

function cliEnvironment(data, endpoint, status, inherited = process.env) {
  if (status.integration !== 'enabled') throw new Error('Enable Cursor integration in Cursor BYOK first.');
  const proxy = new URL(status.proxy_url);
  if (proxy.protocol !== 'http:' || proxy.hostname !== '127.0.0.1' || proxy.username || proxy.password) {
    throw new Error('Cursor BYOK must report a local HTTP proxy.');
  }
  // Same synthetic local identity as server/src/local_app/account.rs, including
  // serde_json's sorted payload keys. Never use or persist a real account token.
  const payload = { email: 'cursor@ai.com', exp: 4070908800, iss: 'cursor-client', scope: 'openid profile email', sub: 'cursor-local-user', type: 'session' };
  const encode = value => Buffer.from(JSON.stringify(value)).toString('base64url');
  const token = `${encode({ alg: 'HS256', typ: 'JWT' })}.${encode(payload)}.cursor-local-user`;
  const env = {
    ...inherited,
    AGENT_CLI_CREDENTIAL_STORE: 'memory',
    CURSOR_CONFIG_DIR: path.join(data, 'cli'),
    CURSOR_API_ENDPOINT: endpoint,
    CURSOR_AUTH_TOKEN: token,
    NODE_EXTRA_CA_CERTS: path.join(data, 'ca', 'ca.crt'),
    HTTP_PROXY: proxy.href, HTTPS_PROXY: proxy.href,
    http_proxy: proxy.href, https_proxy: proxy.href,
    NODE_USE_ENV_PROXY: '1',
  };
  delete env.CURSOR_API_KEY;
  env.NO_PROXY = [inherited.NO_PROXY, inherited.no_proxy, 'localhost', '127.0.0.1'].filter(Boolean).join(',');
  env.no_proxy = env.NO_PROXY;
  return env;
}

async function main() {
  if (!['win32', 'darwin'].includes(process.platform)) throw new Error('This launcher supports Windows and macOS.');
  const data = path.join(os.homedir(), '.cursor-byok-v3');
  const endpoint = endpointFromStore(data);
  let status;
  try {
    const response = await fetch(endpoint + '/__byok-api__/api/harness/cursor/status', { signal: AbortSignal.timeout(5000) });
    if (!response.ok) throw new Error('status failed');
    status = await response.json();
  } catch {
    throw new Error('Start Cursor BYOK before using Cursor CLI.');
  }
  const env = cliEnvironment(data, endpoint, status);
  const entry = path.join(path.dirname(process.execPath), 'index.js');
  if (!fs.existsSync(entry)) throw new Error('Use the cursor-byok launcher with the official CLI installed.');
  if (!fs.existsSync(env.NODE_EXTRA_CA_CERTS)) throw new Error('Cursor BYOK CA certificate is missing.');
  initializeConfig(data);
  const child = spawn(process.execPath, [entry, ...process.argv.slice(2)], { env, stdio: 'inherit', windowsHide: true });
  child.on('error', () => { process.stderr.write('Unable to start Cursor CLI.\n'); process.exitCode = 1; });
  child.on('exit', code => { process.exitCode = code ?? 1; });
}

module.exports = { endpointFromStore, initializeConfig, cliEnvironment };
if (require.main === module) {
  main().catch(error => { process.stderr.write(error.message + '\n'); process.exitCode = 1; });
}
