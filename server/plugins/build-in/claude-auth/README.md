# Claude OAuth (experimental)

Personal, unofficial subscription integration for Cursor BYOK. This is **not an Anthropic-supported
login method**. Anthropic's
[authentication policy](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use)
prohibits offering third-party Claude.ai login and routing requests through subscription
credentials. Access may be denied or restricted without notice. An existing subscription does not
guarantee API access through this plugin.

**Status:** automated Linux checks and a live account smoke test passed on 2026-09-30. The live
check covered browser consent, code exchange, profile lookup, token refresh, discovery of 13 models,
and one streamed text response from `claude-haiku-4-5-20251001`. This remains an experimental,
unofficial integration; other models, accounts, and the full desktop sign-in UI were not
live-tested.

## Structure

```text
server/
├── plugins/build-in/claude-auth/   Claude-specific integration
│   ├── plugin.json               Manifest and allowed HTTPS hosts
│   ├── main.ts                   Provider and account registration
│   ├── oauth.ts                  Browser authorization and host-owned PKCE/callback integration
│   ├── auth.ts                   Token validation, account identity, and refresh coordination
│   ├── resources.ts              Safe account presentation and manual credential refresh
│   ├── models.ts                 Paginated API model discovery and capability metadata
│   ├── provider.ts               Invocation, credential patches, and error/limit classification
│   ├── messages.ts               Messages serialization, streaming, and signed-thinking replay
│   ├── assets/plugin.svg         Neutral plugin icon, not Anthropic branding
│   ├── claude_test.ts            Mocked OAuth, resource, discovery, and provider tests
│   ├── messages_test.ts          Mocked Messages protocol and history-prefix tests
│   ├── host_smoke.ts             Full lifecycle through a real sandboxed Deno worker
│   └── deno.json                 Local SDK imports and development tasks
└── src/plugin/                   Existing plugin host
    ├── builtin.rs                Embeds this plugin in release builds
    └── worker.rs                 Keeps auxiliary HTTP fetches out of model-call recording
```

No frontend, database schema, or provider-independent conversation changes are required. Existing
modules are not moved or deleted.

## Authentication and invocation

```text
Add account
  → host starts 127.0.0.1:<dynamic port>/callback, generates state + PKCE
  → plugin opens claude.ai/oauth/authorize with localhost:<same port>/callback
  → user completes consent in browser
  → host validates callback state
  → plugin exchanges code + state + verifier using the exact advertised redirect
  → plugin reads /api/oauth/profile for stable account and organization identity
  → host persists private credentials, UI receives only the account display name

Sync models
  → /v1/models pagination using the selected account's valid token
  → host stores the returned model catalog and capabilities

Model invocation
  → host supplies account snapshot + canonical request
  → plugin refreshes expiring credentials (one refresh per old token within the worker)
  → /v1/messages streaming request
  → text / thinking / tool events → host → Cursor
  → terminal result includes rotated credentials, even on handled inference errors
  → host applies the credential/state patch
```

- Authorization/token constants match inspected Claude Code and current public client
  implementations.
- The plugin requests only `user:profile user:inference`. This requested scope set passed the live
  smoke test. It does not request API-key creation, file-upload, or MCP rights.
- `localhost` is used in both authorization and exchange. The existing host still binds IPv4
  loopback. The browser must run on the same machine as the server, with working localhost
  resolution.
- Account identity comes from the profile endpoint, because token responses may omit identity
  fields.
- Tokens stay in the host's private plugin storage. This is **not a promise of OS-keychain
  encryption**; this plugin uses the existing host storage model.
- The worker-local refresh cache deduplicates concurrent refreshes and stale snapshots. It is not a
  separate persistent store. A crash between token rotation and the host's final patch can require
  signing in again. Running multiple app processes against the same profile is not supported.
- Model discovery does not rotate credentials, because the existing model-list contract cannot
  persist a resource patch. If the token is expiring, refresh the account, then sync models.
- HTTP 401 receives one refresh/retry before output starts. Stream-level errors and partially
  emitted responses are never retried by this plugin. HTTP 429 uses `Retry-After` for account
  cooling.
- No static model fallback, credential-file import, API-key fallback, or usage-quota estimation is
  provided. Available models are exactly what the upstream catalog returns; entitlement can still
  differ at inference time. The fast-latency hint is not mapped to an unverified subscription
  feature.

## Upstream compatibility limitation

The plugin sends Bearer authorization, `anthropic-version: 2023-06-01`, and
`anthropic-beta: oauth-2025-04-20`. It preserves user instructions and exact tool names. It **does
not impersonate the official Claude Code client**, inject an official-client system identity, or
forge a Claude CLI version.

A live Haiku request succeeded without these transformations on 2026-09-30. That result does not
establish compatibility with every model or account. Anthropic may still reject third-party
subscription requests, so the plugin reports upstream refusals rather than treating OAuth login as a
guarantee of inference access.

## Install and try

Use a build of **this checkout**, not a copy of the plugin placed into an unpatched release. The
host recording fix is necessary: the upstream host could record a refresh-token request/response as
the first model request when detailed logging was enabled.

The plugin is bundled automatically through `server/src/plugin/builtin.rs`. Debug builds discover it
from `server/plugins/build-in/claude-auth`. Linux system dependencies and verification commands are
listed in the repository's [CI workflow](../../../../.github/workflows/ci.yml). With Rust, Node.js
22, and those dependencies installed, run `npm ci` followed by `npm run tauri:build -- --no-bundle`
from `apps/desktop` to build without publishing a release.

After starting that build:

1. Initialize the plugin runtime if the application asks.
2. Open plugin management and select **Claude OAuth (experimental)**.
3. Choose **Sign in with Claude** and complete consent in the local browser.
4. Sync the model catalog and enable a returned model.
5. Run a short connectivity test before using a real conversation.

Building or installing this change does not automatically sign in or import credentials. A source
checkout does not isolate application data: the host uses `~/.cursor-byok-v3` by default. Use a
separate OS account/environment for an isolated runtime profile.

Deleting an account removes the local resource through the host; there is no plugin-specific remote
revocation hook. Revoke the authorization through Anthropic's account controls when needed.

## Verification

From this directory with Deno 2.9.6 installed (the version used by the host):

```sh
deno task check
deno task lint
deno task fmt
deno task test
deno task test:host
```

The 32 unit tests use mocked networking and need no tokens or network permissions. The separate
worker integration test starts the actual SDK worker with the host's sandbox flags and drives OAuth
completion, profile lookup, model discovery, automatic token refresh, streaming events, and
credential patches through its JSON protocol. Its parent test process needs subprocess and read
permissions; the child has only plugin/SDK read access and no direct network access. All upstream
responses in this test are simulated, so it does not establish live Claude compatibility.

These checks also run in the `Claude plugin (Deno)` CI job. Tests cover PKCE/state forwarding,
callback consistency, expiry, identity lookup, refresh-token rotation/coalescing, safe errors,
single-retry behavior, account cooling, pagination, append-only history, images, tool calls, ordered
signed/redacted thinking, usage merging, cancellation, and truncated streams.

Workspace verification, from the repository root with the Rust toolchain and build prerequisites
installed:

```sh
cargo fmt --all -- --check
cargo clippy --locked --workspace --all-targets -- -D warnings
cargo test --locked --workspace --all-targets
cargo build --locked --workspace
```

On Linux with Rust 1.98.1, all 306 workspace tests, strict Clippy, formatting, and the workspace
debug build passed, including the OAuth recording regression test. The desktop TypeScript checks and
Vite production build also passed with cached dependencies. A fresh `npm ci` was blocked by a
registry download timeout. macOS/Windows checks and graphical desktop interaction were not run
locally.

### Live smoke test

On 2026-09-30, a temporary harness called the real plugin modules with a loopback callback and
explicit browser consent. Code exchange, profile lookup, immediate refresh-token rotation, discovery
of 13 models, and a short streamed response from `claude-haiku-4-5-20251001` all succeeded.
Credentials were kept only in process memory and were not written to logs, Git, or the running
application's profile. This verified the plugin against the upstream service, not the full graphical
desktop workflow or durable account recovery after restart. Tool calling, thinking replay, rate
limits, and error paths are covered by mocked tests rather than this single live inference request.

## Protocol references

- [Official policy](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use)
- [Claude Code 2.1.63 implementation](https://unpkg.com/@anthropic-ai/claude-code@2.1.63/cli.js):
  historical official evidence for variable-port localhost redirect and token/profile contracts.
- [pi OAuth implementation, pinned revision](https://github.com/badlogic/pi-mono/blob/db6cc71dc7b69202dc560e71106bb9dfd454e758/packages/ai/src/auth/oauth/anthropic.ts)
- [OpenCode OAuth implementation, pinned revision](https://github.com/ex-machina-co/opencode-anthropic-auth/blob/6e6d285b9895a013962a775e74a5616cce5e973e/src/auth.ts)
- [Anthropic model metadata types](https://github.com/anthropics/anthropic-sdk-typescript/blob/main/src/resources/models.ts)
