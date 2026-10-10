# GitHub Copilot (copilot-auth)

在 cursor-byok 里使用自己的 GitHub Copilot 订阅:设备码登录 GitHub → 自动同步该账号可用的 Copilot
模型(GPT / Claude / Gemini 等)→ 在 Cursor 中对话、Agent、工具调用。

## ⚠️ 风险声明

- 本插件使用 **VS Code Copilot Chat 的 OAuth Client ID `Iv1.b507a08c87ecfe98`**,并以 VS Code
  的请求头访问 Copilot API。这与 [copilot-api](https://github.com/caozhiyuan/copilot-api) 的默认
  行为相同,属于**非官方用法**,可能违反 GitHub
  服务条款,存在账号被限制或封禁的风险。请自行评估后使用。
- Client ID 与所有「伪装身份」常量集中在 `constants.ts`。如有自有 OAuth App(需加入 GitHub Copilot
  Partner Program),只需替换该文件中的常量。
- 本插件**不会**使用 OpenCode 的 Client ID——那等于冒充另一个已获官方合作的产品。
- GitHub token 与 Copilot token 只保存在本机资源记录中,不会出现在日志或账号卡片里。

## 使用

1. 在 cursor-byok 桌面端打开 GitHub Copilot 插件,点击「添加账号」→「使用 GitHub 登录」。
2. 打开验证页面,输入设备码并授权。账号卡片会显示 GitHub 用户名、套餐与 Premium 请求剩余百分比。
3. 点击「同步模型」,然后在 Cursor 中选择 GitHub Copilot 下的模型即可。

发布构建会自动预装本插件;手动安装时把整个 `copilot-auth/` 目录(测试与 `deno.json` 可省略) 拷到
`~/.cursor-byok-v3/plugins/installed/` 后重启 cursor-byok。

## 行为说明

| 项目          | 行为                                                                                                          |
| ------------- | ------------------------------------------------------------------------------------------------------------- |
| Copilot token | 约 30 分钟有效,剩余不足 5 分钟时在调用前自动续期并写回账号                                                    |
| 端点路由      | GPT 系走 `/responses`;Claude、Gemini 与旧模型走 `/chat/completions`;仅支持 `/v1/messages` 的模型暂不提供      |
| 计费          | 最后一条消息是用户输入时 `x-initiator: user`(消耗 premium request);工具结果/助手续跑回合为 `agent`,不额外计费 |
| 重试          | 408 / 425 / 429 / 5xx 与边缘节点的裸 403 最多重试 3 次;401 先重新换取 token 再重试一次                        |
| 额度耗尽      | 429 且提示额度不足时账号进入冷却,直到额度重置日(未知时 1 小时)                                                |
| 上下文        | 档位由 Cursor/宿主统一提供;超出 Copilot 上限(`model_max_prompt_tokens_exceeded`)时宿主自动压缩历史后重试      |
| 不支持        | GitHub Enterprise Server / ghe.com、Anthropic `/v1/messages` 原生协议、自动启用被策略禁用的模型               |

## 开发

```bash
deno check main.ts
deno test --allow-read
deno fmt --check && deno lint
```
