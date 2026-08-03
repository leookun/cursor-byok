# Cursor 协议调试器

[中文](README.md) | [English](README.en.md)

这是一个独立运行的本地 HTTPS 调试代理，用于观察 Cursor 的 `BidiAppend` 和 `RunSSE` 通信。它不会修改 Cursor、系统代理或已安装客户端。

## 启动

在仓库根目录运行：

```bash
go run ./cmd/cursor-proxy-debugger
```

默认监听：

- HTTP/HTTPS 代理：`127.0.0.1:9090`
- 调试界面：`http://127.0.0.1:9091`
- MITM 目标：`api2.cursor.sh`

启动后会自动打开调试界面。

## 配置 Cursor

工具不会自动修改 Cursor。启动后需要手动完成以下配置：

1. 打开 Cursor 的代理设置，将代理地址修改为工具启动时显示的地址，默认是 `http://127.0.0.1:9090`。
2. 打开 Cursor 的 Network 设置，启用 HTTP/1.1。
3. 从 `http://127.0.0.1:9091/api/ca.crt` 下载代理 CA 证书，并确保 Cursor 信任该证书。

调试结束后，请恢复原来的 Cursor 代理和 Network 设置，以免影响正常网络请求。

## 构建

```bash
go build -o bin/cursor-proxy-debugger ./cmd/cursor-proxy-debugger
```

## 参数

```text
-proxy-addr       代理监听地址，默认 127.0.0.1:9090
-ui-addr          调试界面监听地址，默认 127.0.0.1:9091
-target-host      需要解密的目标主机，默认 api2.cursor.sh
-max-exchanges    内存中保留的最大请求数，默认 200
-open             启动后是否打开浏览器，默认 true
```

## 数据处理

- 仅对 `target-host` 执行 HTTPS MITM，其他 CONNECT 流量直接透传。
- `RunSSE` 按 5 字节 Connect 帧头增量拆帧，支持逐帧 gzip 解压。
- `BidiAppendRequest.data` 会继续解码为 `agent.v1.AgentClientMessage`。
- 请求列表支持按抓包时间正序/倒序排列，并可按协议中的 `request_id` 过滤。
- 调试界面支持简体中文和英文，可跟随浏览器语言并记住手动选择。
- 抓包只保留在当前进程内存中；关闭进程后消失。
- `Authorization`、`Cookie`、`Set-Cookie` 等 HTTP 头在界面中默认隐藏。
- 单侧原始正文默认最多保留 2 MiB；代理转发的数据不会被截断。
