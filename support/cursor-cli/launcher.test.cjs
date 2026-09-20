const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { DatabaseSync } = require('node:sqlite');
const { endpointFromStore, initializeConfig, cliEnvironment } = require('./launcher.cjs');

function temporaryData(t) {
  const data = fs.mkdtempSync(path.join(os.tmpdir(), 'cursor-byok-cli-test-'));
  t.after(() => {
    assert.equal(path.dirname(path.resolve(data)), path.resolve(os.tmpdir()));
    assert.ok(path.basename(data).startsWith('cursor-byok-cli-test-'));
    fs.rmSync(data, { recursive: true, force: true });
  });
  return data;
}

test('discovers changing ports without modifying the database', t => {
  const data = temporaryData(t);
  const file = path.join(data, 'cursor-byok.db');
  const db = new DatabaseSync(file);
  db.exec('CREATE TABLE service_settings (setting_key TEXT, value_json TEXT)');
  db.prepare('INSERT INTO service_settings VALUES (?, ?)').run('network_ports', '{"service_port":12345}');
  assert.equal(endpointFromStore(data), 'http://127.0.0.1:12345');
  db.exec(`UPDATE service_settings SET value_json='{"service_port":23456}'`);
  db.close();
  const before = fs.readFileSync(file);
  assert.equal(endpointFromStore(data), 'http://127.0.0.1:23456');
  assert.deepEqual(fs.readFileSync(file), before);
});

test('creates isolated allowlist configuration and preserves existing settings byte for byte', t => {
  const data = temporaryData(t);
  initializeConfig(data);
  const file = path.join(data, 'cli', 'cli-config.json');
  const config = JSON.parse(fs.readFileSync(file, 'utf8'));
  assert.equal(config.approvalMode, 'allowlist');
  assert.deepEqual(config.permissions, { allow: [], deny: [] });
  assert.equal(config.network.useHttp1ForAgent, true);
  config.model = { modelId: 'user-selected-model' };
  config.permissions.deny = ['Shell(*)'];
  fs.writeFileSync(file, JSON.stringify(config));
  const before = fs.readFileSync(file);
  initializeConfig(data);
  assert.deepEqual(fs.readFileSync(file), before);
});

test('isolates credentials and proxies to the CLI child environment', () => {
  const inherited = { CURSOR_AUTH_TOKEN: 'test-account', CURSOR_API_KEY: 'test-key', HTTPS_PROXY: 'http://example.invalid', NO_PROXY: 'example.test', no_proxy: 'other.test' };
  const env = cliEnvironment('data', 'http://127.0.0.1:12345', { integration: 'enabled', proxy_url: 'http://127.0.0.1:23456' }, inherited);
  assert.equal(env.AGENT_CLI_CREDENTIAL_STORE, 'memory');
  assert.equal(env.CURSOR_API_KEY, undefined);
  assert.notEqual(env.CURSOR_AUTH_TOKEN, inherited.CURSOR_AUTH_TOKEN);
  assert.equal(env.CURSOR_API_ENDPOINT, 'http://127.0.0.1:12345');
  assert.equal(env.HTTPS_PROXY, 'http://127.0.0.1:23456/');
  assert.equal(env.NO_PROXY, 'example.test,other.test,localhost,127.0.0.1');
  assert.equal(inherited.HTTPS_PROXY, 'http://example.invalid');
  assert.equal(inherited.CURSOR_AUTH_TOKEN, 'test-account');
});

test('disables sandbox only on native Windows and retains Mac defaults', t => {
  const data = temporaryData(t);
  for (const platform of ['win32', 'darwin']) {
    const directory = path.join(data, platform);
    initializeConfig(directory, platform);
    const config = JSON.parse(fs.readFileSync(path.join(directory, 'cli', 'cli-config.json'), 'utf8'));
    assert.equal(config.approvalMode, 'allowlist');
    assert.equal(config.sandbox?.mode, platform === 'win32' ? 'disabled' : undefined);
  }
});

test('rejects disabled integration and a remote proxy', () => {
  assert.throws(() => cliEnvironment('data', 'http://127.0.0.1:1', { integration: 'disabled' }), /Enable Cursor/);
  assert.throws(() => cliEnvironment('data', 'http://127.0.0.1:1', { integration: 'enabled', proxy_url: 'http://example.invalid:1234' }), /local HTTP proxy/);
});
