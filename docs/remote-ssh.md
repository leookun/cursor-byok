# Remote SSH verification checklist

This is a test checklist, not a record of successful tests. Record the Cursor version (3.21.16 or newer), cursor-byok revision, desktop OS, remote OS, provider/protocol, and the actual result for each check. Use only disposable files and approved commands. Redact hostnames, paths, provider credentials, and private content before sharing evidence.

## Setup and status boundaries

- [ ] Confirm local Cursor requests work with cursor-byok running and local CA/proxy integration configured.
- [ ] Open **Cursor → Remote SSH guide** in cursor-byok, including when local integration is off or CA setup is incomplete.
- [ ] Confirm the navigation and page labels describe **local** integration only; neither claims SSH readiness.
- [ ] In the desktop app, copy the settings. Confirm success appears only after the clipboard operation succeeds. When clipboard access fails, confirm the guide offers manual copying and never reports success.
- [ ] Confirm the help dialog supports keyboard navigation, Escape, focus return, scrolling, and light/dark themes.
- [ ] Connect to the SSH host. Merge `"cursorAgentHost.remoteInferenceRoute": "always"` into that host's **Remote [SSH: host]** settings, preserving other settings.
- [ ] Run **Developer: Reload Window**, start a new chat, and explicitly choose a BYOK model rather than Auto.
- [ ] Confirm requests arrive at the local cursor-byok without an SSH tunnel, remote CA installation, public listener, or disabled TLS verification.

## Test each feature independently

Run these checks first locally, then in a fresh SSH chat. Record unsupported, failed, or untested features explicitly rather than inferring success from another test.

- [ ] Model selection and streaming response: identify the configured provider and verify the response completes.
- [ ] Multi-turn context: ask a follow-up about content established in the same chat.
- [ ] Cancellation: cancel generation, then send another request in the same chat.
- [ ] File tools: read and edit a disposable file that exists only in the SSH workspace; verify the edit on the remote host and clean it up.
- [ ] Terminal tools: run a harmless command that identifies the remote working directory and verify it did not run on the desktop.
- [ ] Repository search: search a symbol unique to the remote repository. Verify results refer to that repository, not a similarly named local checkout.
- [ ] WebFetch: fetch an approved public page and consume the result in the remote session, including any returned result-file reference.
- [ ] MCP: invoke an enabled Model Context Protocol tool and verify which host owns its execution.
- [ ] Skills: invoke a skill from the intended workspace and verify it uses that workspace's context.
- [ ] Tab: exercise completions with the configured Tab service. Agent chat success does not establish Tab success.
- [ ] Commit generation: generate (without committing) a message from disposable remote changes and verify it describes the remote diff.
- [ ] Official models/Auto: test separately when the account has the required access; these do not validate a BYOK model.
- [ ] Reconnect/reload: disconnect, reconnect, reload, and start another new chat. Confirm the per-host setting persists and local-only workflows still work.

## Failure evidence

For a loopback connection failure, record which process tried to reach `127.0.0.1`/`localhost`, on which machine, and whether the effective remote inference setting was `always`. Remote loopback addresses refer to the remote host.

For a certificate failure, record the process, destination, and exact trust/hostname error separately. Keep TLS verification enabled. Do not transfer CA private keys, publish credentials, or assume a remote CA installation is the remedy.

A successful frontend demo, local model test, or automated backend test is not evidence that the SSH feature checks above passed.

## Environment checks recorded for this branch

Record only measured facts. Do not treat this section as a substitute for the checklist above.

- Local Cursor package: `3.22.12` (`cursor --version`).
- Disposable Ubuntu 24.04 LTS SSH host is reachable for fixture setup. Hostnames, addresses, and credentials are not recorded here.
- A disposable remote worktree with uncommitted and untracked markers was created for later live acceptance. It does not by itself prove Agent routing or tool execution.
- Automated coverage added/kept green for local proxy settings, inference route preservation, WebFetch conversation-private paging, and Semble Cursor-host absolute-path snapshots (`cargo test -p cursor-server --lib local_app::`, `search::cache`, `host_snapshot`, `search_provider`, and `tool_round` / `interrupt` / `connect_wire`).
- Live Remote SSH Agent chat with `cursorAgentHost.remoteInferenceRoute: "always"`, remote tool execution, Tab, and commit generation remain **unchecked** until completed in a real SSH window against cursor-byok.
