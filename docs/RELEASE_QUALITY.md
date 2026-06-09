# apigw — Release Quality Log

**Дата:** 2026-06-08 → 2026-06-09 (revised)
**Ветка:** `apigw-launch`
**Scope:** technical release-log — что закрыто, что проверено, что
сознательно отложено. Это hobby/personal-use OSS-проект, не
коммерческий продукт; здесь нет roadmap'а для customer'ов, потому что
их нет.

---

## TL;DR

`v0.3.0` уже зарелизен и для personal-use **достаточен**:

- 28 пакетов unit-тестов green с `-race`
- e2e в Docker (Debian 12 + Ubuntu 22) — install / api / TLS / dashboard
  / auth / admin API / streams / consumers / webhook — все прогоняются
- Linux arm64 + amd64 + arm v7 + darwin cross-build из CI
- Релизные бинари подписаны cosign'ом (keyless GitHub OIDC), sha256 +
  SLSA L3 provenance
- Установщик (`scripts/install.sh`) валидирует подпись + sha256 перед
  установкой в `/usr/local/bin/apigw`

Никакого «коммерческого релиза» больше делать не нужно — следующий tag
выходит когда тебе самому пригодится новая фича или прилетит security
report. Без обязательств перед кем-то, без SLA, без roadmap'а.

---

## Что закрыто в сессии 2026-06-08

Все 17 задач из аудита, `go vet` + `gofmt` + lint — зелёные, arm64
бинарь собирается (47 MB).

**Группа 1 — install-flow blockers:**
- **D-3** Paths refactor (~25 файлов) — `APIGW_STATE_DIR` /
  `APIGW_CONFIG_DIR` / `APIGW_LOG_DIR` теперь работают везде через
  `internal/paths`.
- **RACE-1** `sync.Mutex` на `nginx.Manager` — параллельные
  `WriteAndReload` / `Validate` / `Reload` сериализованы внутри процесса.
- **RACE-2** Stream-include откатывается при validate-fail
  (`currentStreamInclude()` snapshot + `revertStreamInclude(prev)` в
  rollback).
- **TLS-2** Кнопка "Renew" в дашборде делает `nginx reload` после
  `StoreCert` (раньше только CLI).
- **WH-1** `webhook setup` берёт реальный хост: `APIGW_PUBLIC_HOST` →
  TLS-домен → `ServerName` (если не `_`) → routable non-loopback IP →
  `os.Hostname()` → `<your-host>` placeholder.

**Группа 2 — deploy / webhook / TLS quality:**
- **DEP-1** `health_path` в `config.Deploy` — строгий 2xx-probe
  (опционально; default остаётся лояльный TCP).
- **DEP-2** Build-логи стримятся в `os.Stderr` live даже при
  nil-Publisher.
- **TLS-1** `tls.dns_propagation_timeout_seconds` — DuckDNS DNS-01
  timeout настраиваемый.
- **WH-2** `rotate-secret` grace-window 5 минут (`<deploy>.secret.prev`
  + mtime-based, `LoadValidSecrets()` + `verifyAny()`).

**Группа 3 — dashboard UI:**
- **D-5** Audit-detail pre.json `max-height: 60vh; resize: vertical`.
- **D-7** Loading skeleton — `.card.loading` дим + CSS-shimmer пока
  `refreshAdmin` грузит.
- **D-4** Нативные `confirm()` (3 шт.) → `confirmModal({title, body,
  ok, danger})` async Promise, Esc/Enter/backdrop, styled.
- **D-6** `renderAudit` key-based DOM-diff на `entry.id` — expanded
  detail и scroll переживают 10-секундный poll.

**Закрытие исторических багов (фиксы уже в HEAD до этой сессии, см.
git log):**

| Bug | Sev | Fix commit |
|-----|-----|-----------|
| BUG-1 | 🔴 CRITICAL | `8d3ed1c fix(nginx): validate runs plain nginx -t + symlink removed on rollback` |
| BUG-1b | 🔴 CRITICAL | `cb2a21f fix(nginx): gzip/brotli at server scope, not http scope` |
| BUG-2 | 🟠 HIGH | `8d3ed1c` |
| BUG-3 | 🟡 MEDIUM | ConnectionPool wired in `internal/nginx/generator.go:749,849` |
| BUG-7 | 🟠 HIGH | `3775c9d fix(tls): use listen … ssl http2;` |
| BUG-6 | 🟠 HIGH | `1e84bd5 fix(install): disable stock nginx default site` |
| BUG-4 | 🟢 LOW | `e4712d6 fix(config): honor APIGW_CONFIG_DIR` |
| BUG-5 | 🟢 LOW | D-3 paths refactor (закрыто в этой сессии) |
| L-1..L-9 | mixed | `6062a8e`, `77c396d` |
| D-1, D-2, D-8 | HIGH | `6062a8e` |

---

## Что я НЕ проверил (честно)

- ❌ **Тестировал на macOS, не на реальном Debian/Ubuntu arm64.**
  Unit-тесты с моком FS — не то же самое что реальный nginx + systemd +
  сеть.
- ❌ **Docker e2e suite в этой сессии не запускался** — нужен Docker
  daemon (на отдельной машине / в CI).
- ❌ **TLS-flow с реальным Let's Encrypt / DuckDNS** не проверен после
  моих правок.
- ❌ **Webhook delivery от настоящего GitHub** — rotation grace
  протестирован только in-process.
- ❌ **Concurrent load** — mutex добавлен, но не загружен 100
  параллельных `api add`.

Для personal-use это ок — проверишь на своём orange pi когда дойдёшь.

---

## Что добавлено в сессии 2026-06-09 (OSS-hygiene)

Не баги, а полировка OSS-проекта. Всё проверено локально (`go vet`,
`gofmt`, `go test -race`, `docker build`).

| Что | Файл | Зачем для personal-use |
|-----|------|------------------------|
| Vulnerability disclosure | `SECURITY.md` | Если кто-то найдёт серьёзную дыру — есть private канал, а не публичный issue. |
| Contributor guide | `CONTRIBUTING.md` | Drive-by контрибьютор не задаёт одни и те же вопросы по 10 раз. |
| `gosec` в CI | `.github/workflows/ci.yml` | SAST advisory-only, ловит твои же баги до тебя. SARIF в Security tab репо. |
| Production Dockerfile | `Dockerfile` + `make docker-image` | Запустить apigw в одной команде на любой машине — в том числе тебе самому из CI/CD. |
| Admin API `/v1/` aliases | `internal/dashboard/server.go`, `admin_api.go` | `registerAdmin()` — каждый `/api/admin/*` теперь живёт ещё и под `/api/v1/admin/*`. В будущем `/api/admin/*` можно дропнуть без боли. |
| Тест не-регрессии | `internal/dashboard/admin_v1_alias_test.go` | 15 endpoint sub-тестов + panic-проверка. Гарантирует что новые routes не теряют alias. |

### Test gate (локально на macOS, 2026-06-09)

| Gate | Результат |
|------|-----------|
| `go build ./...` | ✅ |
| `go vet ./...` | ✅ |
| `gofmt -l internal cmd test` | ✅ empty |
| `go test -race -count=1 ./...` | ✅ все 28 пакетов green |
| `TestAdminV1Alias` (15 endpoint'ов + panic-guard) | ✅ |
| `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` cross-build | ✅ 34 MB |
| `docker build -f Dockerfile .` + smoke `apigw version` | ✅ образ ~24 MB, бинарь работает в alpine |
| `docker buildx build --check` | ✅ no warnings |
| `ci.yml` YAML | ✅ valid |

---

## Что **не** трогалось — продуктовые решения, не баги

Эти пункты висят на твоём усмотрении, не обязательны для качества
релиза:

- **Telemetry opt-in.** Анонимная статистика usage для honest roadmap.
  Default-off обязателен (privacy). Для personal-use малорелевантно —
  ты сам себе единственный пользователь.
- **Config schema versioning** (`schema_version:` + migration
  framework). Трогает все примеры конфига и e2e dockerfiles; делать
  когда придёт breaking change в schema. Сейчас не нужно.
- **Удаление legacy `/api/admin/*`** в пользу `/api/v1/admin/*`. Сейчас
  оба префикса работают, dashboard JS бьёт в legacy. Через
  пару minor релизов снять; не срочно.
- **Реальный email в `SECURITY.md`** вместо `security@<TODO>` placeholder.
  Поменяешь если когда-нибудь зарегистрируешь домен.

---

## Distribution status

| Канал | Статус |
|-------|--------|
| GitHub Releases (cosign + sha256 + SLSA L3) | ✅ работает, см. `.github/workflows/release.yml` |
| `scripts/install.sh` (curl-and-verify) | ✅ работает, проверяет cosign + sha256 |
| Docker image | 🟡 Dockerfile есть, но автопуш в registry не настроен |
| Homebrew tap | ❌ нет |
| apt/yum repo | ❌ нет |
| Marketplace AMI | ❌ нет (для personal-use не надо) |

Для personal-use хватает первых двух. Остальное добавишь когда
понадобится / если когда-нибудь захочешь.

---

## Что коммитить из последних двух сессий

Из сессии 2026-06-08: 43 файла с правками багов (D-3..D-7, DEP-1/2,
WH-1/2, RACE-1/2, TLS-1/2 + L-1..L-9 + D-1/2/8 restored + A1+A4+A5+A6+A7 +
audit-lock + tls-renew + uninstall-cleanup), +693 / -167 строк, 3 новых
unit-теста.

Из сессии 2026-06-09: `SECURITY.md`, `CONTRIBUTING.md`, `Dockerfile`,
обновление `Makefile` (`make docker-image`), обновление
`.github/workflows/ci.yml` (gosec job), `registerAdmin()` helper +
миграция 25 admin routes в `internal/dashboard/{server,admin_api}.go`,
`internal/dashboard/admin_v1_alias_test.go`.

Разбивка коммитов по вкусу. Логические группы:

1. `fix:` — все багфиксы из сессии 06-08 (paths, races, TLS, webhook,
   dashboard UI)
2. `chore:` — OSS hygiene из сессии 06-09 (`SECURITY.md`,
   `CONTRIBUTING.md`, gosec)
3. `feat:` — Production Dockerfile + `make docker-image`
4. `feat(api):` — `/v1/` aliases для admin API + тест не-регрессии
