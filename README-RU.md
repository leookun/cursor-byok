<div align="center">

# cursor-byok

cursor-byok — локальная реализация бэкенда Cursor: шлюз моделей на вашем устройстве.

<br>
<br>
<a href="https://trendshift.io/repositories/39260?utm_source=repository-badge&amp;utm_medium=badge&amp;utm_campaign=badge-repository-39260" target="_blank" rel="noopener noreferrer"><img src="https://trendshift.io/api/badge/repositories/39260" alt="leookun/cursor-byok | Trendshift" width="250" height="55" /></a>

[English README](./README.md) · [中文说明](./README-CN.md) · [Руководство](https://docs.leokun.cn) · [Скачать](https://github.com/leookun/cursor-byok/releases/latest) · [Сообщить об ошибке](https://github.com/leookun/cursor-byok/issues)

[![Release](https://img.shields.io/github/v/release/leookun/cursor-byok?style=flat-square)](https://github.com/leookun/cursor-byok/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/leookun/cursor-byok/total?style=flat-square)](https://github.com/leookun/cursor-byok/releases)
[![License](https://img.shields.io/github/license/leookun/cursor-byok?style=flat-square)](./LICENSE)
[![Platforms](https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey?style=flat-square)](https://github.com/leookun/cursor-byok/releases/latest)

</div>

![Подключение cursor-byok к различным API моделей](./images/en-brand-1.png)

![Панель управления cursor-byok](./images/en-home-1.png)

## О проекте

cursor-byok — открытый локальный шлюз моделей для Cursor. Он запускает сервис на вашей машине, который связывает Cursor с настроенными вами API моделей, маршрутизирует запросы через ваши провайдеры и сохраняет возможности Cursor Agent: вызовы инструментов, Skills и MCP.

Вы можете подключать сервисы, совместимые с OpenAI и Anthropic, настраивать endpoint, ID моделей, API-ключи и параметры запросов, а также использовать каналы моделей за пределами встроенных опций платформы.

> [!IMPORTANT]
> cursor-byok бесплатен и с открытым исходным кодом, но API моделей, которые вы подключаете, могут тарифицироваться по использованию. Это независимый проект, не связанный с Cursor и его разработчиками и не одобренный ими.

## Возможности

- **Свои каналы моделей:** настройте endpoint, учётные данные и ID моделей.
- **Несколько протоколов API:** OpenAI- и Anthropic-совместимые API или свой endpoint.
- **Управление моделями:** добавление, дублирование, редактирование, сортировка и пакетная проверка конфигураций.
- **Тесты соединения:** время до первого токена, скорость генерации и сырые ответы провайдера.
- **Agent-сценарии:** вызовы инструментов, Skills, MCP и многоходовые диалоги.
- **Метрики сессий:** использование токенов, доля попаданий в кэш, ход диалога и оценочная стоимость.
- **Кроссплатформенность:** macOS, Windows и Linux.

## Быстрый старт

1. Скачайте сборку для вашей платформы с [GitHub Releases](https://github.com/leookun/cursor-byok/releases/latest).
2. Запустите cursor-byok, откройте **Настройки моделей** и укажите endpoint, API-ключ и ID модели.
3. Проверьте конфигурацию модели. После успешной проверки вернитесь на панель и запустите сервис.
4. После обновления Cursor или первой настройки модели полностью выйдите из Cursor и перезапустите его, затем начните новый диалог и выберите настроенную модель.

Полные шаги установки, конфигурации системы и ответы на частые вопросы — в [руководстве пользователя](https://docs.leokun.cn).

## Управление моделями

Конфигурации моделей поддерживают протоколы OpenAI и Anthropic. Для каждого канала можно задать размер контекстного окна, максимум токенов вывода, уровень рассуждения, пользовательские заголовки и дополнительные параметры запроса.

![Настройки моделей cursor-byok](./images/en-model-1.png)

## Как это работает

```text
Клиент Cursor
    │
    │ Запросы Agent и результаты инструментов
    ▼
Локальный сервис cursor-byok
    │
    │ Запросы, совместимые с OpenAI / Anthropic
    ▼
Ваш API модели
```

cursor-byok выполняет адаптацию протоколов, пересылку запросов к моделям, координацию вызовов инструментов и состояние диалога на вашей машине. API-ключи и настройки приложения хранятся локально; запросы по-прежнему уходят к выбранному вами провайдеру.

## Зачем этот проект

Многие Agent-продукты связывают инструменты с фиксированным набором моделей, подписок и тарифов, ограничивая пользователя каналами платформы.

cursor-byok возвращает выбор моделей пользователю. Разработчики могут использовать уже имеющиеся API и кредиты, выбирать подходящие модели и провайдеров и при необходимости самостоятельно хостить связанные сервисы.

## Язык интерфейса

В приложении доступен русский язык интерфейса (`ru-RU`). В **Настройках → Язык** выберите **Русский** или оставьте **Язык системы** — при русской локали ОС интерфейс переключится автоматически.

## Дорожная карта

Проект продолжит улучшать совместимость моделей, инструменты Agent, стабильность локального runtime и опыт self-hosting, а также исследовать поддержку других IDE, чатов и Agent-сценариев.

См. [дорожную карту релизов](https://github.com/leookun/cursor-byok/discussions/32).

## Сообщество и поддержка

- [Руководство пользователя](https://docs.leokun.cn)
- [GitHub Issues](https://github.com/leookun/cursor-byok/issues)
- [Telegram-сообщество](https://t.me/cursor_byok)
- QQ-группы: `1095916242`, `1094411438`, `1095918002`, `1094419321`

## Разработка и вклад

Issues и pull request приветствуются. См. [Contributing Guide](./CONTRIBUTING_EN.md) для требований, команд сборки, структуры проекта и правил вклада.

## Участники

<a href="https://github.com/leookun/cursor-byok/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=leookun/cursor-byok" />
</a>

## Лицензия

Проект распространяется под [лицензией MIT](./LICENSE).
