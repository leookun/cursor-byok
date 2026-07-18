# AGENTS.md

<!-- BEGIN CODEX PROJECT PROFILE -->
## Project Profile
- Wails v3 (`v3.0.0-alpha.74`) desktop application with a Go 1.25 module and a Vue 3/Vite 7 frontend.
- Distribution targets are Windows, macOS, and Linux; Windows packaging uses NSIS or MSIX.

## Repository Structure
- `main.go` and `internal/app`: desktop entry point and Wails lifecycle.
- `internal/bridge`, `internal/client`, `internal/backend`, `internal/mitm`: frontend services, runtime orchestration, local backend, and proxy implementation.
- `frontend/src`: Vue application source; `frontend/bindings` contains Wails-generated bindings.
- `proto` and `gen`: protocol sources and generated Go code.
- `build` and `Taskfile.yml`: platform build, packaging, icon, binding, and protocol generation workflows.
- `cursor-tab-server`: separate Go service with its own module and Docker build.

## Commands
- Frontend tests: `cd frontend && yarn test`
- Frontend production build: `cd frontend && yarn build`
- Go tests: `go test ./...`
- Desktop development: `wails3 dev -config ./build/config.yml -port 9245`
- Current-platform distribution build: `wails3 task build`
- Windows amd64 distribution build: `wails3 task build:windows:amd64`

## Change Rules
- Do not hand-edit `frontend/bindings`, `gen`, generated protocol copies, `frontend/dist`, `node_modules`, or `bin`; use the repository generators and build tasks.
- Preserve compatibility of persisted model groups, model adapters, routing configuration, Cursor settings, and certificate paths.
- Keep API keys and custom request headers out of logs, tests, commits, and generated project instructions.

## Verification
- Frontend changes require `yarn test` and `yarn build` from `frontend`.
- Go or bridge changes require `go test ./...`; use the target Windows architecture when validating Wails/WebView2 code.
- Packaging changes require the relevant Wails task and inspection of the produced artifact in `bin`.

## Project Risks
- The application owns a single-instance desktop lifecycle and mutates Cursor settings while starting or stopping a local backend and MITM proxy.
- Provider URLs, API keys, custom headers, local certificates, and update artifacts are security-sensitive inputs.
<!-- END CODEX PROJECT PROFILE -->
