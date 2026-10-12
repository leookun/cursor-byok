# Cursor CLI through local BYOK

This opt-in launcher supports the official **native Windows and macOS** Cursor CLI. It is
source tooling, not an automatic desktop installer feature. Keep both files
(`cursor-byok.ps1` on Windows or `cursor-byok` on Mac, plus `launcher.cjs`) together. It does not replace `agent`,
install the CLI, change PATH, or copy provider API keys.

## Windows setup and use

1. Install the official native Windows CLI following
   [Cursor's installation instructions](https://cursor.com/docs/cli/installation).
   Its standard package location is `%LOCALAPPDATA%\cursor-agent\versions`.
2. Build/run Cursor BYOK from this branch (including the CLI metadata route
   fixes), configure a model, and enable Cursor integration. Keep it running.
3. From the repository root, start a new CLI session:

   ```powershell
   & .\support\cursor-cli\cursor-byok.ps1 --list-models
   & .\support\cursor-cli\cursor-byok.ps1 --model <hash-from-list>
   ```

   Use a configured model's hash from the list. No provider ID or model hash is
   built into the launcher. To work in another directory, invoke the script by
   its absolute path or pass the CLI's `--workspace` option. The CLI may save a
   selection made with `--model` as its new default.

## macOS setup and use

Install the official macOS CLI following the same Cursor installation guide,
then launch the patched Cursor BYOK app, configure models, and enable Cursor
integration. Keep the helper running. From the extracted CLI launcher directory:

```bash
bash ./cursor-byok --list-models
bash ./cursor-byok --model <hash-from-list>
```

The standard CLI package directory is `~/.local/share/cursor-agent/versions`.
The launcher uses its bundled Node runtime; a separate Node installation is not
needed. CLI configuration lives in `~/.cursor-byok-v3/cli`. The macOS sandbox
keeps the CLI's default behavior. Neither launcher changes your shell profile
or overwrites the original `agent` command.

The launcher selects the latest installed version, reads the helper's current
service port from its SQLite database in read-only mode, and checks integration
status before starting. The CLI uses `%USERPROFILE%\.cursor-byok-v3\cli` as an
independent configuration directory. First use creates HTTP/1.1 and allowlist
approval settings; existing settings are preserved. Native Windows does not
implement the CLI sandbox, so this initial configuration disables that sandbox
while retaining tool approval. No `--force` or unrestricted approval is added.

Model requests go to the local service. Other CLI traffic uses its local proxy,
and the public BYOK CA is trusted only by the child process. The built-in local
identity is held in the CLI's memory credential store, not written to the normal
Cursor account credential store. The shell's proxy variables are unchanged.

This relies on Cursor CLI runtime settings verified with
`2026.09.10-fd3934a`; future CLI changes may require updating the launcher.
If the helper is stopped or integration is disabled, startup fails explicitly.
To stop using this setup, run the original CLI directly. The optional BYOK CLI
configuration can be backed up separately; the helper's model database remains
the single source of provider configuration.

## Validation and platform status

Run the launcher checks with Node 24 (or the CLI's bundled Node):

```powershell
node --test support/cursor-cli/launcher.test.cjs
```

Windows end-to-end checks used real Grok and Claude provider configurations,
issued a `Read` tool call, returned the exact contents of a local probe file,
and verified completed local BYOK provider/run records. Model listing alone
is not an end-to-end check.

The desktop project builds macOS applications for Apple Silicon and Intel.
Published upstream v0.1.7 packages predate these changes. Fork builds must state
their source commit and validation results. A successful build or CLI version
check does not verify real-provider requests on a Mac; validate a model response
and a Read tool call after configuring that machine.
