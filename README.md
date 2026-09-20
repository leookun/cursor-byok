<div align="center">

# cursor-byok
cursor-byok is a local implementation of Cursor's backend.
<br>
<br>
<a href="https://trendshift.io/repositories/39260?utm_source=repository-badge&amp;utm_medium=badge&amp;utm_campaign=badge-repository-39260" target="_blank" rel="noopener noreferrer"><img src="https://trendshift.io/api/badge/repositories/39260" alt="leookun/cursor-byok | Trendshift" width="250" height="55" /></a>

[User Guide](https://docs.leokun.cn) · [Download](https://github.com/leookun/cursor-byok/releases/latest) · [Report an Issue](https://github.com/leookun/cursor-byok/issues) · [中文版本说明](./README-CN.md)

[![Release](https://img.shields.io/github/v/release/leookun/cursor-byok?style=flat-square)](https://github.com/leookun/cursor-byok/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/leookun/cursor-byok/total?style=flat-square)](https://github.com/leookun/cursor-byok/releases)
[![License](https://img.shields.io/github/license/leookun/cursor-byok?style=flat-square)](./LICENSE)
[![Platforms](https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey?style=flat-square)](https://github.com/leookun/cursor-byok/releases/latest)



</div>

![Connect cursor-byok to a wide range of model APIs](./images/en-brand-1.png)

![cursor-byok dashboard](./images/en-home-1.png)

## About

cursor-byok is an open-source local model gateway for Cursor. It runs a service on your machine that connects Cursor to the model APIs you configure, routes model requests through your own providers, and preserves Cursor Agent capabilities such as tool calling, Skills, and MCP.

You can connect OpenAI- and Anthropic-compatible services, customize endpoints, model IDs, API keys, and request parameters, and use model channels beyond the options built into the platform.

> [!IMPORTANT]
> cursor-byok is free and open source, but the model APIs you connect may charge for usage. This is an independent project and is not affiliated with or endorsed by Cursor or its developers.

## Features

- **Bring your own model channels:** Configure your own API endpoint, credentials, and model IDs.
- **Multiple API protocols:** Use OpenAI- and Anthropic-compatible APIs or a custom endpoint.
- **Model management:** Add, duplicate, edit, reorder, and batch-test multiple model configurations.
- **Connection benchmarks:** Measure time to first token, generation speed, and inspect raw provider responses.
- **Agent workflows:** Keep tool calling, Skills, MCP, and multi-turn conversations available.
- **Session metrics:** Track token usage, cache hit rate, conversation turns, and estimated value.
- **Cross-platform:** Run on macOS, Windows, and Linux.

## Quick Start

1. Download the latest build for your platform from [GitHub Releases](https://github.com/leookun/cursor-byok/releases/latest).
2. Launch cursor-byok, open **Model Settings**, and enter the endpoint, API key, and model ID.
3. Test the model configuration. Once it passes, return to the dashboard and start the service.
4. Test the model configuration. Once it passes, return to the dashboard and start the service.
5. After upgrading Cursor or configuring a model for the first time, quit Cursor completely and restart it, then start a new conversation and select the configured model.

For complete installation steps, system configuration, and Frequently Asked Questions, see the [User Guide](https://docs.leokun.cn).

## Model Management

Model configurations support both OpenAI and Anthropic API protocols. Each model channel can independently define its context window, maximum output tokens, reasoning effort, custom headers, and additional request parameters.

![cursor-byok model settings](./images/en-model-1.png)

## How It Works

```text
Cursor client
    │
    │ Agent requests and tool results
    ▼
cursor-byok local service
    │
    │ OpenAI- / Anthropic-compatible requests
    ▼
Your model API
```

cursor-byok handles protocol adaptation, model request forwarding, tool-call coordination, and conversation state on your machine. API keys and application settings are stored locally; requests are still sent to the model provider you configure.

## Why This Project

Many Agent products bundle their tool capabilities with a fixed set of models, subscriptions, and billing options, leaving users limited to the channels offered by the platform.

cursor-byok is built to return model choice to the user. Developers can make full use of the APIs and credits they already have, choose the models and providers that fit their needs, and self-host related services when required.

## Roadmap

The project will continue to improve model compatibility, Agent tooling, local runtime stability, and the self-hosting experience while exploring support for more IDE, chat, and Agent workflows.

See the [release roadmap](https://github.com/leookun/cursor-byok/discussions/32) for plans and progress.

## Community and Support

- [User Guide](https://docs.leokun.cn)
- [GitHub Issues](https://github.com/leookun/cursor-byok/issues)
- [Telegram community](https://t.me/cursor_byok)
- QQ groups: `1095916242`, `1094411438`, `1095918002`, `1094419321`



## Explicit Cursor model routing

Cursor-hosted model IDs normally use the Cursor upstream. To route a specific ID through an existing BYOK model, use the local control API:

- `GET /__byok-api__/api/settings/cursor-model-aliases` reads the alias map.
- `PUT /__byok-api__/api/settings/cursor-model-aliases` replaces it with a JSON object such as `{"cursor-grok-4.6-high-fast":"<configured-model-hash>"}`. Obtain the target hash from the configured model, not its provider model ID or display name.

Mappings are exact and opt-in; no hosted models are redirected by default. The target supplies the provider URL, API key, and provider model ID. Explicit Cursor request parameters still take precedence over saved defaults, as with a direct BYOK selection. Initial selections, model-details selections, and explicit subagent model overrides use the same mapping. Existing runs keep their selected route. An unknown/deleted target is rejected locally rather than falling back to the hosted model. To remove all mappings, PUT `{}`. If changing provider configuration changes a target hash, update its mappings too.

## Development and Contributing

### Cursor CLI with local BYOK

An opt-in [Windows/macOS CLI launcher](./support/cursor-cli/README.md) reuses the
running helper's model configurations, dynamic ports, and local identity. It
keeps CLI settings separate and retains tool approval. This source launcher is
not automatically installed by the desktop app; macOS real-provider CLI integration still needs validation on the target machine.

Issues and pull requests are welcome. See the [Contributing Guide](./CONTRIBUTING_EN.md) for prerequisites, build commands, project structure, and contribution guidelines.

## Contributors

<a href="https://github.com/leookun/cursor-byok/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=leookun/cursor-byok" />
</a>


## License

This project is open source under the [MIT License](./LICENSE).


