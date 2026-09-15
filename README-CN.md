<div align="center">

# cursor-byok

cursor-byok 是一个运行在本机的 Cursor 模型网关，帮助你在 Cursor 中使用自己配置的模型服务。

[English README](./README.md) · [使用指南](https://docs.leokun.cn) · [下载](https://github.com/leookun/cursor-byok/releases/latest) · [提交问题](https://github.com/leookun/cursor-byok/issues)

[![Release](https://img.shields.io/github/v/release/leookun/cursor-byok?style=flat-square)](https://github.com/leookun/cursor-byok/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/leookun/cursor-byok/total?style=flat-square)](https://github.com/leookun/cursor-byok/releases)
[![License](https://img.shields.io/github/license/leookun/cursor-byok?style=flat-square)](./LICENSE)
[![Platforms](https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey?style=flat-square)](https://github.com/leookun/cursor-byok/releases/latest)

</div>

![将 cursor-byok 连接到多种模型 API](./images/en-brand-1.png)

![cursor-byok 控制面板](./images/en-home-1.png)

## 项目简介

cursor-byok 是一个开源的本地模型网关。它在你的设备上运行服务，接收 Cursor 发出的 Agent 请求，将请求转发到你配置的模型服务，并尽可能保留 Cursor Agent 的工具调用、Skills、MCP 和多轮对话能力。

你可以连接兼容 OpenAI 或 Anthropic 协议的服务，自定义服务地址、模型 ID、API Key 和请求参数，也可以使用 Cursor 平台默认选项之外的模型通道。

> [!IMPORTANT]
> cursor-byok 免费且开源，但你连接的模型服务商可能会按用量收费。本项目是独立项目，与 Cursor 或其开发者没有关联，也未获得其认可。

## 主要功能

- **自定义模型通道**：配置自己的 API 地址、凭据和模型 ID。
- **多种 API 协议**：支持 OpenAI Responses API、OpenAI Chat Completions API 和 Anthropic Messages API 兼容服务。
- **模型管理**：添加、复制、编辑、排序模型配置，并批量测试连接。
- **连接性能测试**：查看首字延迟、生成速度、总耗时和原始服务商响应。
- **Agent 工作流**：继续使用工具调用、Skills、MCP 和多轮对话。
- **会话指标**：查看 Token 用量、缓存命中率、对话轮次和估算价值。
- **TAB 补全服务**：在公益服务、官方直连和自定义服务之间选择连接方式。
- **跨平台运行**：支持 macOS、Windows 和 Linux。

## 快速开始

1. 从 [GitHub Releases](https://github.com/leookun/cursor-byok/releases/latest) 下载适合你操作系统的最新版本。
2. 启动 cursor-byok，打开 **Cursor 配置**，按提示初始化本地 CA（证书颁发机构）。
3. 在模型设置中添加模型，填写服务地址、API Key 和模型名称，然后保存并运行 **测试**。
4. 确认测试通过后，保持 cursor-byok 运行。
5. **首次升级 Cursor 或首次配置模型后，完全退出并重新启动 Cursor，然后新开一个对话**。在模型列表中选择已配置的模型，开始使用 Agent。

完整的安装步骤、配置说明和常见问题，请参阅[中文使用指南](https://docs.leokun.cn/zh/docs)。

> [!TIP]
> 首次升级 Cursor 或首次完成配置后，必须完全退出并重新启动 Cursor，再新开一个对话。配置前已经打开的对话不会加载新连接；使用自定义模型时，请在模型列表中手动选择该模型，不要选择 **Auto**。

## 模型配置

每个模型配置都是独立的上游通道，可以单独设置服务商、协议、凭据和生成参数。

![模型设置页面](./images/en-model-1.png)

### 类型与协议选择

| 模型系列 | 模型类型 | 请求协议 |
| --- | --- | --- |
| Claude 系列 | **Anthropic** | Messages API |
| GPT / OpenAI 系列 | **OpenAI** | **Responses API** |
| 其他模型 | **OpenAI** | **Chat Completions API** |

GPT 系列建议使用 **Responses API**。如果使用 Chat Completions，可能无法保留提示词缓存，导致速度变慢和费用增加。

### 常用字段

- **模型类型**：选择 OpenAI 或 Anthropic，决定上游接口格式。
- **请求协议**：OpenAI 类型需要继续选择 Responses API 或 Chat Completions API。
- **服务器地址**：可以填写服务商基础地址，让应用按协议追加标准端点，也可以填写完整请求 URL 并原样使用。
- **API Key**：填写上游服务要求的访问密钥。密钥保存在本机，用于发送模型请求。
- **模型名称**：填写服务商接口接受的模型标识，也可以使用 **获取模型** 读取服务商返回的模型列表。
- **显示名称**：Cursor 模型列表中显示的名称，不会改变发送给上游的模型标识。
- **备注**：显示在 Cursor 的模型说明中。

还可以根据模型能力设置上下文窗口 Token、最大输出 Token、推理或思考强度、自定义 Headers，以及 OpenAI 或 Anthropic 的额外参数。自定义 Headers 和额外参数必须是 JSON 对象，只应填写服务商明确支持的字段。

保存配置后运行 **测试**，确认地址、协议、API Key、模型标识和流式响应都正常，再在 Cursor 中使用该模型。

## TAB 补全服务

Cursor 的 Tab 补全由独立的 TAB 服务处理，不经过模型通道。你可以在 **系统设置 → TAB 设置** 中选择以下模式：

- **使用公益服务（默认）**：使用项目作者部署的公共服务，无需额外配置。
- **直连**：直接连接当前 Cursor 账号对应的官方 TAB 服务，适合账号拥有官方额度的情况。
- **自定义**：自行部署 [`cursor-tab-server`](https://github.com/leookun/cursor-byok/tree/archive/v0.0.49/cursor-tab-server)，然后填写 TAB 服务地址。

修改 TAB 设置后，建议重启 Cursor 并新开一个对话，确保新的连接方式生效。

## 与官方账号并存

新版设计支持 cursor-byok 与 Cursor 官方服务并存：

- 直接在 Cursor 中登录自己的账号。如果之前使用旧版生成的 fake 账户，请先退出该账户，再登录自己的账号。
- 账号拥有官方额度时，官方模型和本地模型可以随时切换混用。
- **Auto 只使用官方模型**，不会自动使用你配置的本地模型。账号没有官方额度时，请手动选择自己配置的模型。
- 插件、代码库索引等 Cursor 功能可以继续使用。

## 数据流转

```text
Cursor 客户端
    │
    │ Agent 请求与工具结果
    ▼
cursor-byok 本地服务
    │
    │ OpenAI / Anthropic 兼容请求
    ▼
你配置的模型 API
```

API Key、模型配置和应用设置保存在本机。模型请求仍会发送到你选择的上游服务商，请根据对应服务商的隐私政策和计费规则使用。

## 项目结构

```text
cursor-byok/
├── apps/
│   ├── desktop/
│   │   ├── src/
│   │   │   ├── features/ # 首页、模型、调用记录与设置
│   │   │   ├── shell/    # 窗口框架、页面布局与广告外壳
│   │   │   ├── shared/   # UI、虚拟列表、状态、API 与平台能力
│   │   │   ├── i18n/     # 本地化运行时与语言目录
│   │   │   └── styles/   # 全局主题与排版令牌
│   │   └── src-tauri/    # Tauri 桌面生命周期
├── server/
│   ├── src/
│   │   ├── cursor/    # Cursor 协议适配
│   │   ├── run/       # 通用 Agent Runtime 与内部 Port
│   │   ├── provider/  # 模型供应商适配
│   │   ├── model/     # 聚合领域模型
│   │   ├── store/     # SQLite Repository
│   │   ├── control/   # 管理面 API
│   │   ├── harness/   # Cursor 本地集成
│   │   └── search/    # Web 与 Semble 搜索接入
│   ├── prompt/
│   └── migrations/
├── crates/
│   └── semble-core/   # 本地代码索引与搜索核心库
├── protocols/
│   └── cursor/        # Cursor 协议定义的唯一来源
├── support/                     # 仓库辅助设施
│   ├── cursor-protocol-extractor/ # Cursor 协议提取与 Go 代码生成工具
│   ├── cursor-capture/            # Cursor 协议抓取和调试工具
│   └── benchmarks/                # 代码搜索和索引基准测试
├── images/            # README 展示图片
├── Cargo.toml         # Rust 工作区配置
└── Makefile           # 常用开发、检查和构建命令
```

## 本地开发

### 环境要求

- Rust 工具链和 Cargo
- Node.js 与 npm
- Tauri 2 的系统构建依赖
- Docker（仅在构建 Docker 镜像时需要）

### 安装依赖

```bash
npm --prefix apps/desktop install
```

### 启动开发环境

启动桌面前端：

```bash
make dev-web
```

启动桌面应用：

```bash
make dev-desktop
```

### 检查与构建

运行完整检查：

```bash
make check
```

分别构建各部分：

```bash
make build-web       # 构建桌面前端
make build-server    # 构建 Rust 本地服务
make build-desktop   # 构建 Tauri 桌面安装包
make build-docker    # 构建 Docker 镜像
```

## 路线图

项目将继续改进模型兼容性、Agent 工具、本地运行稳定性和自托管体验，并探索支持更多 IDE、聊天和 Agent 工作流。

计划与进展请参阅[发布路线图](https://github.com/leookun/cursor-byok/discussions/32)。

## 社区与反馈

- [中文使用指南](https://docs.leokun.cn/zh/docs)
- [GitHub Issues](https://github.com/leookun/cursor-byok/issues)
- [Telegram 社区](https://t.me/cursor_byok)
- QQ 群：`1095916242`、`1094411438`、`1095918002`、`1094419321`

提交问题时，请附上操作系统、cursor-byok 版本、模型类型、请求协议、已脱敏的服务地址、错误信息和复现步骤。请勿公开 API Key 或其他凭据。

## 参与贡献

### Cursor CLI 接管

仓库提供可选的 [Windows/macOS CLI 启动器](./support/cursor-cli/README.md)，复用运行中的助手模型配置、动态端口和本地身份。CLI 设置独立保存，保留工具审批。它目前需要从源码目录手动运行，桌面安装程序不会自动安装；macOS 的真实供应商调用仍需在目标机器验证。

欢迎提交 Issue 和 Pull Request。提交代码前请先阅读项目中的开发说明，并运行 `make check` 确认格式、测试和前端构建检查通过。

## 许可证

本项目采用 [MIT License](./LICENSE) 开源。

## Cursor 模型精确映射

Cursor 托管模型编号默认使用 Cursor 上游。要让指定编号使用已有的 BYOK 配置，可以通过本地管理 API 设置：

- `GET /__byok-api__/api/settings/cursor-model-aliases` 读取映射。
- `PUT /__byok-api__/api/settings/cursor-model-aliases` 整体替换映射，例如 `{"cursor-grok-4.6-high-fast":"<已配置模型的 hash>"}`。目标必须使用已配置模型的内部 hash，不能使用供应商模型名或显示名称。

映射必须明确配置，默认不改变任何托管模型的线路。供应商地址、密钥和模型名来自目标 BYOK 配置；Cursor 请求中明确指定的参数仍优先于已保存的默认值，与直接选择 BYOK 模型一致。首次模型选择、model details 和显式子代理模型选择都会应用映射，已经运行的请求保持原线路。目标被删除或不存在时在本地报错，不回退到托管模型。发送 `{}` 可清除全部映射；修改供应商配置导致目标 hash 变化时，也需要更新映射。
