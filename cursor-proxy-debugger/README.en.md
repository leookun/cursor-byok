# Cursor Protocol Debugger

[中文](README.md) | [English](README.en.md)

This standalone local HTTPS debugging proxy captures Cursor's `BidiAppend` and `RunSSE` traffic. It does not modify Cursor, the system proxy, or the installed client.

## Start

Run the following command from the repository root:

```bash
go run ./cmd/cursor-proxy-debugger
```

Default addresses:

- HTTP/HTTPS proxy: `127.0.0.1:9090`
- Debugging UI: `http://127.0.0.1:9091`
- MITM target: `api2.cursor.sh`

The debugging UI opens automatically after startup.

## Configure Cursor

The tool does not modify Cursor automatically. After starting it, configure Cursor manually:

1. Open Cursor's proxy settings and set the proxy to the address printed by the tool. The default is `http://127.0.0.1:9090`.
2. Open Cursor's Network settings and enable HTTP/1.1.
3. Download the proxy CA certificate from `http://127.0.0.1:9091/api/ca.crt` and make sure Cursor trusts it.

Restore the original Cursor proxy and Network settings after debugging to avoid affecting normal network requests.

## Build

```bash
go build -o bin/cursor-proxy-debugger ./cmd/cursor-proxy-debugger
```

## Options

```text
-proxy-addr       Proxy listen address; default: 127.0.0.1:9090
-ui-addr          Debugging UI listen address; default: 127.0.0.1:9091
-target-host      Host to decrypt and capture; default: api2.cursor.sh
-max-exchanges    Maximum number of exchanges retained in memory; default: 200
-open             Open the browser after startup; default: true
```

## Data Handling

- HTTPS MITM is applied only to `target-host`; other CONNECT traffic passes through unchanged.
- `RunSSE` is decoded incrementally using the 5-byte Connect frame header and supports per-frame gzip decompression.
- `BidiAppendRequest.data` is further decoded as `agent.v1.AgentClientMessage`.
- Requests can be sorted chronologically or in reverse chronological order and filtered by protocol `request_id`.
- The UI supports Simplified Chinese and English, follows the browser language, and remembers a manual selection.
- Captured traffic is stored only in process memory and is discarded when the process exits.
- Sensitive HTTP headers such as `Authorization`, `Cookie`, and `Set-Cookie` are hidden in the UI by default.
- Raw bodies are retained up to 2 MiB per side by default; forwarded traffic is never truncated.
