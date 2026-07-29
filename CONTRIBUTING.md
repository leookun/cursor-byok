# 贡献指南

> English version: [CONTRIBUTING_EN.md](./CONTRIBUTING_EN.md)

感谢你考虑为 cursor-byok 做出贡献！

## 开发环境

| 依赖 | 版本要求 |
|------|---------|
| Go | >= 1.25 |
| Node.js | >= 20 |
| Yarn | 1.x (classic) |
| [Task](https://taskfile.dev) | >= 3 |
| [Wails v3 CLI](https://v3alpha.wails.dev) | alpha.74+ |

Linux 额外依赖：`libgtk-3-dev`、`libwebkit2gtk-4.1-dev`（Wails 运行时需要）。

## 快速开始

```bash
# 安装前端依赖
cd frontend && yarn install --frozen-lockfile && cd ..

# 启动开发模式（热重载）
task dev

# 构建当前平台分发包
task build
```

## 项目结构

```
├── main.go                 # 入口
├── internal/               # Go 后端（代理、转发、客户端管理等）
├── frontend/               # Vue 3 + Vite + Tailwind 前端
│   ├── src/
│   │   ├── views/          # 页面
│   │   ├── components/     # 组件
│   │   ├── i18n/           # 国际化（zh-CN / en-US / ja-JP / ru-RU）
│   │   └── state/          # 全局状态
│   └── plugins/            # Vite 插件（i18n 静态扫描等）
├── prompt/                 # 内置 Agent prompt 模板
├── proto/                  # Protobuf 定义
├── build/                  # 构建配置与平台 Taskfile
├── scripts/                # 辅助脚本（release、metrics）
└── Taskfile.yml            # 顶层任务编排
```

## 开发规范

### 提交信息

采用 [Conventional Commits](https://www.conventionalcommits.org/zh-hans/) 风格：

```
feat(proxy): 支持自定义 upstream 超时
fix(i18n): 补全日语翻译缺失 key
release: 0.0.42
```

### 代码风格

- Go：遵循 `gofmt` / `go vet`，不引入额外 linter 配置。
- 前端：Vue SFC + Composition API，Tailwind 工具类优先。
- 新增 UI 文案必须同步更新所有 locale 文件（`frontend/src/i18n/locales/`）。

### 分支与 PR

1. 从 `main` 创建功能分支：`feat/xxx`、`fix/xxx`。
2. 保持 PR 小而聚焦，一个 PR 解决一个问题。
3. PR 描述中说明动机和测试方式。

## 构建与发布

```bash
# 构建全平台（仅 macOS 主机）
task build:all

# 准备发布资产
task release:prepare

# 发布到 GitHub Releases
task release:github
```

## 许可证

提交代码即表示你同意以 [MIT License](./LICENSE) 授权你的贡献。