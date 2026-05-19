# tg-video-bot

Telegram-бот для скачивания видео через **Inline Mode** и личные сообщения.  
Напиши `@VidqueBot https://youtube.com/...` в любом чате — видео появится мгновенно из кэша или скачается в фоне с уведомлением в ЛС.

Поддерживает YouTube, Instagram Reels, TikTok, Twitter/X, VK и [1000+ платформ](https://github.com/yt-dlp/yt-dlp/blob/master/supportedsites.md) через `yt-dlp`.

[![Release](https://img.shields.io/github/v/release/Rabb1tof/tg-video-bot)](https://github.com/Rabb1tof/tg-video-bot/releases)
[![Docker](https://img.shields.io/badge/docker-ghcr.io-blue)](https://ghcr.io/Rabb1tof/tg-video-bot)

---

## Возможности

- **Inline-режим** — вставляй видео в любой чат без пересылки
- **История** — `@VidqueBot` без ссылки показывает последние скачанные видео
- **Умный кэш** — топ-3 видео пользователя хранится дольше обычного
- **Инвалидация кэша** — после загрузки история в inline обновляется мгновенно
- **Ссылки без протокола** — `youtube.com/watch?v=...` работает без `https://`
- **До 2 ГБ** через локальный Bot API сервер (стандартный лимит Telegram — 50 МБ)
- **Масштабируется** — Redis connection pool, настраиваемый пул воркеров

---

## Архитектура

```
Пользователь
    │  @bot https://...  (inline query)
    ▼
Telegram Servers ──► Bot (Go)
                        │
                   Redis cache?
                   ├─ ДА  ──► answerInlineQuery(CachedVideo) [мгновенно]
                   │           RefreshFileIDTTL (если топ-3 истории)
                   └─ НЕТ ──► answerInlineQuery(loading stub, TTL 5s)
                               Worker Pool (N горутин)
                                   │
                               yt-dlp --print title --no-simulate
                                   │
                               sendVideo → Приватный канал (CDN Telegram)
                                   │
                               file_id → Redis
                               history → Redis (LPUSH, макс 50)
                               IncrInlineVersion → сброс кэша inline
                                   │
                               PM: "✅ Видео готово"
```

**Стек:**

| Компонент | Технология |
|---|---|
| Бот | Go 1.22, go-telegram-bot-api/v5 |
| Загрузчик | yt-dlp + ffmpeg |
| Кэш | Redis 7 |
| Bot API сервер | aiogram/telegram-bot-api (локальный, снимает лимит 50 МБ) |
| Хранилище файлов | Приватный Telegram-канал (бесплатный CDN) |
| CI/CD | GitHub Actions → GHCR → Docker |

---

## Быстрый старт

### 1. Подготовка в Telegram

1. Создай бота через [@BotFather](https://t.me/BotFather):
   ```
   /newbot
   /setinline @VidqueBot
   /setinlinefeedback @VidqueBot
   ```

2. Создай **приватный канал**, добавь бота администратором с правом публикации.  
   ID канала: перешли сообщение из канала боту [@userinfobot](https://t.me/userinfobot).

3. Получи **API ID и Hash** на [my.telegram.org/apps](https://my.telegram.org/apps).

### 2. Конфигурация

```bash
cp .env.example .env
# Заполни: TELEGRAM_TOKEN, TELEGRAM_API_ID, TELEGRAM_API_HASH, STORAGE_CHANNEL_ID
```

### 3. Запуск

```bash
docker compose up -d --build
docker compose logs -f bot
```

Готово. Напиши `@VidqueBot https://youtu.be/dQw4w9WgXcQ` в любом чате.

---

## Переменные окружения

| Переменная | Обязательная | По умолчанию | Описание |
|---|---|---|---|
| `TELEGRAM_TOKEN` | ✅ | — | Токен бота от @BotFather |
| `TELEGRAM_API_ID` | ✅ | — | API ID с my.telegram.org |
| `TELEGRAM_API_HASH` | ✅ | — | API Hash с my.telegram.org |
| `STORAGE_CHANNEL_ID` | ✅ | — | ID приватного канала-хранилища |
| `WORKER_COUNT` | — | `5` | Параллельных загрузок |
| `MAX_FILE_SIZE_MB` | — | `500` | Максимальный размер файла (МБ) |
| `CACHE_TTL_HOURS` | — | `24` | TTL обычных видео в кэше (часы) |
| `HOT_CACHE_TTL_HOURS` | — | `168` | TTL топ-3 видео пользователя (часы) |
| `REDIS_POOL_SIZE` | — | `20` | Размер пула соединений Redis |
| `RATE_LIMIT_PER_HOUR` | — | `50` | Лимит запросов на пользователя в час |
| `LOG_LEVEL` | — | `info` | `info` или `debug` |
| `REDIS_PASSWORD` | — | `""` | Пароль Redis |

---

## Команды бота

| Команда | Описание |
|---|---|
| `/start` | Приветствие и инструкция |
| `/help` | Справка |
| `/stats` | Статистика: кэш, воркеры, очередь |
| `/history` | Последние скачанные видео |
| `/clear` | Очистить историю |

---

## Как работает инвалидация inline-кэша

Telegram кэширует результаты inline-запросов на стороне клиента. После загрузки нового видео история должна обновиться **мгновенно**.

Решение: каждый `result_id` в истории содержит версию (`h{ver}-{i}`). После загрузки вызывается `IncrInlineVersion` — версия увеличивается, Telegram видит новые `result_id` и запрашивает свежий список вместо кэшированного.

---

## Структура проекта

```
tg-video-bot/
├── .github/workflows/
│   └── release.yml          # CI/CD: сборка, тесты, публикация образа, релиз
├── cmd/bot/
│   └── main.go              # точка входа
├── internal/
│   ├── config/config.go     # загрузка env
│   ├── cache/redis.go       # Redis: кэш, история, rate limit, inline version
│   ├── downloader/ytdlp.go  # обёртка над yt-dlp
│   ├── worker/pool.go       # горутин-пул, дедупликация, загрузка в канал
│   └── bot/
│       ├── bot.go           # команды, PM-режим
│       └── inline.go        # inline query handler
├── Dockerfile
├── docker-compose.yml
└── .env.example
```

---

## CI/CD и релизы

Проект использует GitHub Actions:

- **Push в `main`** → сборка и запуск тестов
- **Тег `v*.*.*`** → сборка Docker-образа, публикация в GHCR, создание GitHub Release с changelog

Образ доступен по адресу:
```
ghcr.io/Rabb1tof/tg-video-bot:latest
ghcr.io/Rabb1tof/tg-video-bot:v1.2.3
```

Деплой через образ:
```bash
docker pull ghcr.io/Rabb1tof/tg-video-bot:latest
# В docker-compose.yml замени build: . на image: ghcr.io/Rabb1tof/tg-video-bot:latest
```

---

## Обновление yt-dlp

```bash
# В работающем контейнере:
docker compose exec bot pip install -U yt-dlp

# Или пересборка образа:
docker compose up -d --build
```

---

## EasyPanel / продакшн

Рекомендуемые значения для 1000+ пользователей:

```env
WORKER_COUNT=10
REDIS_POOL_SIZE=50
RATE_LIMIT_PER_HOUR=20
CACHE_TTL_HOURS=24
HOT_CACHE_TTL_HOURS=168
```

---

## Лицензия

MIT
