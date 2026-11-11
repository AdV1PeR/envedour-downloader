# Конфигурация Envedour Bot

Полное описание всех параметров конфигурации и переменных окружения.

## Содержание

- [Формат конфигурации](#формат-конфигурации)
- [Обязательные параметры](#обязательные-параметры)
- [Базовые настройки](#базовые-настройки)
- [Cookies для платформ](#cookies-для-платформ)
- [Дополнительные настройки](#дополнительные-настройки)
- [Настройка systemd сервисов](#настройка-systemd-сервисов)
- [Оптимизация Redis](#оптимизация-redis)
- [Примеры конфигурации](#примеры-конфигурации)

## Формат конфигурации

Конфигурация задается через переменные окружения в файле `.env` или через systemd environment.

### Расположение .env файла

Бот ищет `.env` файл в следующем порядке:

1. Текущая рабочая директория
2. Директория, где находится бинарник

При установке через `install.sh`, файл копируется в:
- `/opt/envedour-bot/.env` - для бота
- `/opt/telegram-bot-api/.env` - для Telegram Bot API

### Формат файла .env

```bash
# Комментарии начинаются с #
KEY=value
KEY="value with spaces"
KEY='value with spaces'
```

## Обязательные параметры

Эти параметры должны быть установлены для работы бота.

### BOT_TOKEN

**Описание**: Токен бота от @BotFather  
**Тип**: Строка  
**Формат**: `1234567890:ABCdefGHIjklMNOpqrsTUVwxyz`  
**Пример**: `BOT_TOKEN=1234567890:ABCdefGHIjklMNOpqrsTUVwxyz`

**Как получить**:
1. Откройте https://t.me/BotFather
2. Отправьте `/newbot`
3. Следуйте инструкциям
4. Скопируйте токен

### TELEGRAM_API_ID

**Описание**: API ID от my.telegram.org  
**Тип**: Число  
**Пример**: `TELEGRAM_API_ID=12345678`

**Как получить**:
1. Перейдите на https://my.telegram.org/apps
2. Войдите в аккаунт
3. Создайте приложение
4. Скопируйте `api_id`

### TELEGRAM_API_HASH

**Описание**: API Hash от my.telegram.org  
**Тип**: Строка (hex)  
**Пример**: `TELEGRAM_API_HASH=abcdef1234567890abcdef1234567890`

**Как получить**: Вместе с `TELEGRAM_API_ID` на my.telegram.org

## Базовые настройки

### REDIS_ADDR

**Описание**: Адрес Redis сервера  
**Тип**: Строка (host:port)  
**По умолчанию**: `localhost:6379`  
**Пример**: `REDIS_ADDR=localhost:6379`

**Для удаленного Redis**:
```bash
REDIS_ADDR=192.168.1.100:6379
```

**С паролем** (если настроен):
```bash
REDIS_ADDR=localhost:6379
REDIS_PASSWORD=your_password  # Если используется
```

### WORKER_COUNT

**Описание**: Количество воркеров для обработки задач  
**Тип**: Число  
**По умолчанию**: `4`  
**Рекомендации**:
- Для 4-ядерного CPU: `4-6`
- Для 2-ядерного CPU: `2-3`
- Для 8+ ядер: `6-8`

**Пример**: `WORKER_COUNT=4`

### TMPFS_PATH

**Описание**: Путь к tmpfs для временных файлов  
**Тип**: Путь  
**По умолчанию**: `/dev/shm/videos`  
**Рекомендации**: Не изменяйте без необходимости

**Пример**: `TMPFS_PATH=/dev/shm/videos`

### MAX_FILE_SIZE_MB

**Описание**: Максимальный размер файла в MB  
**Тип**: Число  
**По умолчанию**: `2048` (2GB)  
**Рекомендации**:
- Для 4GB RAM: `1024-2048`
- Для 8GB+ RAM: `2048-4096`

**Пример**: `MAX_FILE_SIZE_MB=2048`

### LOCAL_API_URL

**Описание**: URL локального Bot API  
**Тип**: URL  
**По умолчанию**: `http://localhost:8089`  
**Формат**: `http://host:port` или `https://host:port`

**Примеры**:
```bash
# Локальный API
LOCAL_API_URL=http://localhost:8089

# Удаленный API
LOCAL_API_URL=http://192.168.1.100:8089

# Без локального API (использовать официальный)
# Просто не устанавливайте эту переменную или оставьте пустой
```

### MIN_FREE_MEM_MB

**Описание**: Минимальная свободная память в MB перед загрузкой  
**Тип**: Число  
**По умолчанию**: `256`  
**Рекомендации**:
- Минимум: `256` MB
- Рекомендуется: `512` MB
- Для стабильности: `1024` MB

**Пример**: `MIN_FREE_MEM_MB=256`

## Cookies для платформ

Cookies файлы используются для обхода ограничений платформ. Формат: Netscape cookies.

### TIKTOK_COOKIES

**Описание**: Путь к файлу cookies для TikTok  
**Тип**: Путь к файлу  
**По умолчанию**: Не установлено  
**Обязательно**: Да (для работы с TikTok)  
**Формат**: Netscape cookies

**Пример**: `TIKTOK_COOKIES=/opt/envedour-bot/tiktok_cookies.txt`

**Как получить**:
1. Установите расширение браузера для экспорта cookies
2. Войдите на tiktok.com в браузере
3. Экспортируйте cookies в формате Netscape
4. Сохраните на сервере

### INSTAGRAM_COOKIES

**Описание**: Путь к файлу cookies для Instagram  
**Тип**: Путь к файлу  
**По умолчанию**: Не установлено  
**Обязательно**: Да (для работы с Instagram)  
**Формат**: Netscape cookies

**Пример**: `INSTAGRAM_COOKIES=/opt/envedour-bot/instagram_cookies.txt`

### YOUTUBE_COOKIES

**Описание**: Путь к файлу cookies для YouTube  
**Тип**: Путь к файлу  
**По умолчанию**: Не установлено  
**Обязательно**: Нет (опционально)  
**Формат**: Netscape cookies

**Пример**: `YOUTUBE_COOKIES=/opt/envedour-bot/youtube_cookies.txt`

**Примечание**: YouTube обычно работает без cookies, но они могут помочь при ограничениях.

### COOKIES_FILE (устаревшее)

**Описание**: Общий файл cookies для всех платформ  
**Тип**: Путь к файлу  
**Статус**: Устаревшее, используйте платформо-специфичные файлы  
**Использование**: Только для обратной совместимости

## Дополнительные настройки

### DONOR_CHAT_IDS

**Описание**: Chat ID доноров (через запятую)  
**Тип**: Список чисел  
**По умолчанию**: Пусто  
**Формат**: `id1,id2,id3`

**Пример**: `DONOR_CHAT_IDS=123456789,987654321`

**Как узнать Chat ID**:
1. Отправьте сообщение боту @userinfobot
2. Он вернет ваш Chat ID

**Привилегии доноров**:
- Высокий приоритет в очереди
- Задачи обрабатываются первыми

### ALLOWED_GROUP_IDS

**Описание**: Группы, которым разрешено пользоваться ботом  
**Тип**: Список ID через запятую  
**По умолчанию**: Пусто (ни одна группа)  
**Пример**: `ALLOWED_GROUP_IDS=-1001234567890`

Группы из `.env` показываются в `/admin`, но удалить их через бота нельзя.
Для управления на ходу используйте `/admin` и `ALLOWED_GROUPS_FILE`.

### ALLOWED_USER_IDS

**Описание**: Пользователи, которым разрешено писать боту в личку  
**Тип**: Список Telegram user ID через запятую  
**По умолчанию**: Пусто (любой пользователь)  
**Пример**: `ALLOWED_USER_IDS=123456789,987654321`

### ADMIN_USER_IDS

**Описание**: Пользователи, которым доступна команда `/admin`  
**Тип**: Список Telegram user ID через запятую  
**По умолчанию**: Пусто (`/admin` выключена)  
**Пример**: `ADMIN_USER_IDS=123456789`

Это отдельное право, не связанное с `DONOR_CHAT_IDS`.

### ALLOWED_GROUPS_FILE

**Описание**: JSON-файл для групп, добавленных через `/admin`  
**Тип**: Путь к файлу  
**По умолчанию**: `/opt/envedour-bot/allowed_groups.json`  
**Права**: файл `600`, владелец — пользователь, от которого работает бот

### Настройки Telegram Bot API

Эти параметры используются сервисом `telegram-bot-api.service`:

#### TELEGRAM_LOCAL

**Описание**: Использовать локальный Bot API  
**Тип**: Булево  
**По умолчанию**: `true`  
**Пример**: `TELEGRAM_LOCAL=true`

#### TELEGRAM_STAT

**Описание**: Включить статистику Bot API  
**Тип**: Число (0 или 1)  
**По умолчанию**: `0`  
**Пример**: `TELEGRAM_STAT=0`

#### TELEGRAM_FILTER

**Описание**: Фильтр обновлений  
**Тип**: Число  
**По умолчанию**: `0`  
**Пример**: `TELEGRAM_FILTER=0`

#### TELEGRAM_MAX_WORKERS

**Описание**: Максимум воркеров Bot API  
**Тип**: Число  
**По умолчанию**: `1000`  
**Пример**: `TELEGRAM_MAX_WORKERS=1000`

#### TELEGRAM_HTTP_PORT

**Описание**: Порт для локального Bot API  
**Тип**: Число  
**По умолчанию**: `8089`  
**Пример**: `TELEGRAM_HTTP_PORT=8089`

**Важно**: Если измените порт, обновите `LOCAL_API_URL` соответственно.

## Настройка systemd сервисов

### Переопределение переменных окружения

Можно переопределить переменные для конкретного сервиса:

```bash
sudo systemctl edit envedour-down-bot
```

Добавьте:

```ini
[Service]
# Переопределение переменных
Environment="WORKER_COUNT=6"
Environment="MAX_FILE_SIZE_MB=4096"
Environment="MIN_FREE_MEM_MB=512"
```

### Ограничения ресурсов

Настройки в `envedour-down-bot.service`:

```ini
[Service]
MemoryHigh=3.5G    # Предупреждение при превышении
MemoryMax=4G       # Максимальная память
CPUQuota=380%      # Почти 4 ядра
IOWeight=100       # Приоритет ввода-вывода
Nice=-10           # Высокий приоритет CPU
```

Можно изменить через:

```bash
sudo systemctl edit envedour-down-bot
```

```ini
[Service]
MemoryHigh=2G
MemoryMax=3G
CPUQuota=200%
```

## Оптимизация Redis

Redis настраивается автоматически через `setup.sh`. Ручная настройка:

### Конфигурация Redis

```bash
sudo nano /etc/redis/redis.conf.d/arm-optimized.conf
```

```conf
# Максимальная память
maxmemory 1gb

# Политика очистки при нехватке памяти
maxmemory-policy allkeys-lru

# Отключить сохранение на диск (только RAM)
save ""

# Включить активное перехеширование
activerehashing yes
```

### Применение изменений

```bash
sudo systemctl restart redis-server
```

### Проверка конфигурации

```bash
redis-cli CONFIG GET maxmemory
redis-cli CONFIG GET maxmemory-policy
```

## Примеры конфигурации

### Минимальная конфигурация

```bash
# .env
BOT_TOKEN=1234567890:ABCdefGHIjklMNOpqrsTUVwxyz
TELEGRAM_API_ID=12345678
TELEGRAM_API_HASH=abcdef1234567890abcdef1234567890
REDIS_ADDR=localhost:6379
```

### Базовая конфигурация

```bash
# .env
BOT_TOKEN=1234567890:ABCdefGHIjklMNOpqrsTUVwxyz
TELEGRAM_API_ID=12345678
TELEGRAM_API_HASH=abcdef1234567890abcdef1234567890

REDIS_ADDR=localhost:6379
WORKER_COUNT=4
TMPFS_PATH=/dev/shm/videos
MAX_FILE_SIZE_MB=2048
LOCAL_API_URL=http://localhost:8089
MIN_FREE_MEM_MB=256
```

### Полная конфигурация

```bash
# .env

# Обязательные
BOT_TOKEN=1234567890:ABCdefGHIjklMNOpqrsTUVwxyz
TELEGRAM_API_ID=12345678
TELEGRAM_API_HASH=abcdef1234567890abcdef1234567890

# Базовые настройки
REDIS_ADDR=localhost:6379
WORKER_COUNT=4
TMPFS_PATH=/dev/shm/videos
MAX_FILE_SIZE_MB=2048
LOCAL_API_URL=http://localhost:8089
MIN_FREE_MEM_MB=256

# Cookies
TIKTOK_COOKIES=/opt/envedour-bot/tiktok_cookies.txt
INSTAGRAM_COOKIES=/opt/envedour-bot/instagram_cookies.txt
YOUTUBE_COOKIES=/opt/envedour-bot/youtube_cookies.txt

# Доноры
DONOR_CHAT_IDS=123456789,987654321

# Управление группами через /admin
ADMIN_USER_IDS=123456789
ALLOWED_GROUPS_FILE=/opt/envedour-bot/allowed_groups.json

# Telegram Bot API
TELEGRAM_LOCAL=true
TELEGRAM_STAT=0
TELEGRAM_FILTER=0
TELEGRAM_MAX_WORKERS=1000
TELEGRAM_HTTP_PORT=8089
```

### Конфигурация для слабого железа

```bash
# .env
BOT_TOKEN=...
TELEGRAM_API_ID=...
TELEGRAM_API_HASH=...

# Уменьшенные настройки
WORKER_COUNT=2
MAX_FILE_SIZE_MB=1024
MIN_FREE_MEM_MB=512
```

### Конфигурация для мощного железа

```bash
# .env
BOT_TOKEN=...
TELEGRAM_API_ID=...
TELEGRAM_API_HASH=...

# Увеличенные настройки
WORKER_COUNT=6
MAX_FILE_SIZE_MB=4096
MIN_FREE_MEM_MB=256
```

## Проверка конфигурации

### Валидация .env файла

```bash
# Проверьте синтаксис
cat /opt/envedour-bot/.env

# Проверьте, что файл читается
sudo -u botuser cat /opt/envedour-bot/.env
```

### Тест конфигурации

```bash
# Запустите бота с проверкой конфигурации
sudo -u botuser /opt/envedour-bot/envedour-bot-arm64 --arm-optimized

# Должен показать ошибки конфигурации, если есть
```

### Проверка переменных окружения

```bash
# В systemd сервисе
sudo systemctl show envedour-down-bot | grep Environment
```

---

**Следующий шаг**: См. [USAGE.md](USAGE.md) для использования бота
