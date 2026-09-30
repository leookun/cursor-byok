# Claude OAuth (experimental)

Personal, unofficial subscription integration for Cursor BYOK. This is **not an Anthropic-supported
login method**. Anthropic's
[authentication policy](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use)
prohibits offering third-party Claude.ai login and routing requests through subscription
credentials. Access may be denied or restricted without notice. An existing subscription does not
guarantee API access through this plugin.

**Status:** local, mocked protocol tests pass. Browser consent, token exchange, profile/model
access, and inference have not been tested against a real account. This is an experimental
implementation, not a verified working subscription connection.

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
- The plugin requests only `user:profile user:inference`; acceptance of this reduced scope set has
  not been verified with a live account. It does not request API-key creation, file-upload, or MCP
  rights.
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

Current third-party clients sometimes add these identity transformations to avoid subscription API
rejections. Their necessity is not officially documented, but this is a significant compatibility
risk: authorization can succeed while inference is rejected. This implementation reports the refusal
instead of claiming that OAuth login guarantees usable inference.

## Install and try

Use a build of **this checkout**, not a copy of the plugin placed into an unpatched release. The
host recording fix is necessary: the upstream host could record a refresh-token request/response as
the first model request when detailed logging was enabled.

The plugin is bundled automatically through `server/src/plugin/builtin.rs`. Debug builds discover it
from `server/plugins/build-in/claude-auth`. Build instructions are in the repository's
[contributing guide](../../../../CONTRIBUTING_EN.md).

After starting that build:

1. Initialize the plugin runtime if the application asks.
2. Open plugin management and select **Claude OAuth (experimental)**.
3. Choose **Sign in with Claude** and complete consent in the local browser.
4. Sync the model catalog and enable a returned model.
5. Run a short connectivity test before using a real conversation.

No account login, token import, installation into the running app, or live model call is performed
by this change. A source checkout does not isolate application data: the host uses
`~/.cursor-byok-v3` by default. Use a separate OS account/environment for an isolated runtime
profile.

Deleting an account removes the local resource through the host; there is no plugin-specific remote
revocation hook. Revoke the authorization through Anthropic's account controls when needed.

## Verification

From this directory with Deno 2 installed:

```sh
deno task check
deno task lint
deno task fmt
deno task test
```

Tests use mocked networking and need no tokens or network permissions. They cover PKCE/state
forwarding, callback consistency, expiry, identity lookup, refresh-token rotation/coalescing, safe
errors, single-retry behavior, account cooling, pagination, append-only history, images, tool calls,
ordered signed/redacted thinking, usage merging, cancellation, and truncated streams.

Host regression tests, from `server/` with the Rust toolchain and build prerequisites installed:

```sh
cargo test --lib plugin::builtin::tests
cargo test --lib plugin::worker::tests
cargo fmt --all -- --check
```

## Protocol references

- [Official policy](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use)
- [Claude Code 2.1.63 implementation](https://unpkg.com/@anthropic-ai/claude-code@2.1.63/cli.js):
  historical official evidence for variable-port localhost redirect and token/profile contracts.
- [pi OAuth implementation, pinned revision](https://github.com/badlogic/pi-mono/blob/db6cc71dc7b69202dc560e71106bb9dfd454e758/packages/ai/src/auth/oauth/anthropic.ts)
- [OpenCode OAuth implementation, pinned revision](https://github.com/ex-machina-co/opencode-anthropic-auth/blob/6e6d285b9895a013962a775e74a5616cce5e973e/src/auth.ts)
- [Anthropic model metadata types](https://github.com/anthropics/anthropic-sdk-typescript/blob/main/src/resources/models.ts)
