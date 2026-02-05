# Changelog

## [Unreleased] - 2026-02-05

### Added
- **🚀 GitHub Auto-Deploy System**: Complete CI/CD integration with GitHub repositories
- **📊 Web Dashboard**: Modern React-like UI for deployment management and monitoring
- **🪝 Webhook Server**: GitHub webhook integration for automatic deployments
- **📦 Modular Architecture**: Clean separation of concerns with reusable modules
- **🔧 Extended API Manager**: Comprehensive management tool with deployment capabilities
- **📋 Real-time Monitoring**: Live service status, logs, and system metrics
- **🔄 Backup/Restore**: Configuration backup and restore functionality

### Changed
- **Technology Stack**: Migrated from Python to Node.js for better performance and compatibility
- **Project Structure**: Reorganized into modular components for better maintainability
- **Installation Process**: Enhanced with automatic dependency management
- **User Interface**: Complete redesign with modern, responsive web interface

## [Previous] - 2026-02-05

### Fixed
- **Apt lock handling**: Добавлена функция `safe_apt()` для безопасной работы с пакетным менеджером
  - Автоматическое ожидание освобождения блокировки apt (до 5 минут)
  - Повторные попытки установки пакетов (до 3 раз)
  - Информативные сообщения о состоянии процесса
  - Решает проблему "Could not get lock /var/lib/dpkg/lock-frontend"

### Improved
- Все вызовы `apt-get` теперь используют безопасную функцию `safe_apt()`
- Лучшая обработка ошибок при установке пакетов
- Более надежная установка Fluent Bit и других зависимостей

### Technical Details
Проблема возникала когда:
1. Скрипт пытался установить пакеты через apt-get
2. В это время работал процесс автоматического обновления системы (unattended-upgrades)
3. Система блокировала доступ к apt для предотвращения конфликтов
4. Установка завершалась с ошибкой

Теперь скрипт:
- Проверяет блокировку перед каждой операцией с пакетами
- Ждет освобождения блокировки (до 5 минут)
- Повторяет неудачные операции до 3 раз
- Предоставляет четкую информацию о процессе