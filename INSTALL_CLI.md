# Установка CLI через curl

## Стандартные практики установки CLI инструментов

### 1. Базовый подход (как nvm, Docker, Homebrew)

```bash
curl -fsSL https://example.com/install.sh | bash
```

**Флаги curl:**
- `-f` - fail silently on HTTP errors
- `-s` - silent mode (no progress bar)
- `-S` - show errors even in silent mode
- `-L` - follow redirects

### 2. С версионированием

```bash
# Установка конкретной версии
curl -fsSL https://example.com/install.sh | bash -s -- --version 1.0.0

# Или через переменную окружения
VERSION=1.0.0 curl -fsSL https://example.com/install.sh | bash
```

### 3. С проверкой целостности (SHA256)

```bash
# Скачать и проверить
curl -fsSL https://example.com/install.sh -o install.sh
echo "expected_hash  install.sh" | shasum -a 256 -c -
bash install.sh
```

### 4. Безопасная установка (скачать → проверить → установить)

```bash
# Скачать в файл
curl -fsSL https://example.com/install.sh -o /tmp/install.sh

# Проверить содержимое (опционально)
cat /tmp/install.sh

# Выполнить
bash /tmp/install.sh
```

## Примеры популярных инструментов

### Docker
```bash
curl -fsSL https://get.docker.com | bash
```

### Node.js (nvm)
```bash
curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash
```

### Homebrew (macOS)
```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

### Oh My Zsh
```bash
sh -c "$(curl -fsSL https://raw.githubusercontent.com/ohmyzsh/ohmyzsh/master/tools/install.sh)"
```

## Установка вашего CLI

### Вариант 1: Полная установка (рекомендуется)

```bash
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/devevghenicernev-png/api-gateway-installer/main/install-cli.sh)"
```

### Вариант 2: С версионированием

```bash
sudo BRANCH=v1.0.0 bash -c "$(curl -fsSL https://raw.githubusercontent.com/devevghenicernev-png/api-gateway-installer/main/install-cli.sh)"
```

## Лучшие практики

### 1. Обработка ошибок

```bash
#!/bin/bash
set -e  # Exit on error
set -u  # Exit on undefined variable
set -o pipefail  # Exit on pipe failure
```

### 2. Проверка зависимостей

```bash
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

if ! command_exists curl; then
    echo "Error: curl is required"
    exit 1
fi
```

### 3. Определение платформы

```bash
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
esac
```

### 4. Временные файлы

```bash
tmp_file=$(mktemp)
trap "rm -f $tmp_file" EXIT

curl -fsSL "$url" -o "$tmp_file"
# ... use tmp_file ...
```

### 5. Проверка прав доступа

```bash
if [ -w "$INSTALL_DIR" ]; then
    cp "$script" "$INSTALL_DIR/"
else
    sudo cp "$script" "$INSTALL_DIR/"
fi
```

### 6. Проверка после установки

```bash
if command -v tool-name >/dev/null 2>&1; then
    echo "✓ Installation successful"
    tool-name --version
else
    echo "✗ Installation failed"
    exit 1
fi
```

## Безопасность

### ✅ Хорошо:
- Использовать HTTPS
- Проверять SHA256 хеши
- Показывать что будет установлено
- Давать возможность отменить установку
- Использовать `set -e` для обработки ошибок

### ❌ Плохо:
- Выполнять код без проверки
- Использовать HTTP вместо HTTPS
- Скачивать бинарники без проверки подписи
- Устанавливать без подтверждения пользователя

## Пример полного скрипта установки

См. `install-cli.sh` в этом репозитории - пример правильной установки CLI через curl.

## Использование

После установки:

```bash
# Проверить версию
api-manage-extended --help

# Использовать CLI
api-manage-extended list
api-manage-extended status
```
