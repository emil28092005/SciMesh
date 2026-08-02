# План: Coordinator Admin UI и Worker Setup UI

> Статус: план. Текущий `/ui` — демонстрационный control room (джобы, ворклоады, docs).
> Два новых UI решают две разные задачи и живут в **разных бинарниках**:
> админка кластера — в `coordinator`, визард установки воркера — в
> `worker-agent` (для тех, кто ставит ТОЛЬКО воркер и не имеет координатора
> на своей машине).

## 1. Цель

1. **Coordinator Admin UI** (`/ui/admin`) — полноценная консоль админа
   распределённого кластера: система, джобы, воркеры, юзеры/роли,
   ворклоады, настройки, метрики. Живёт в бинарнике `coordinator`.
2. **Worker Setup UI** — локальный визард **в бинарнике `worker-agent`**
   (`worker-agent setup` → браузер на 127.0.0.1): пошаговое подключение
   машины к координатору — URL/токен/ключ, рабочий каталог, проверка
   соединения, запуск и статус воркера. Не требует установки координатора
   и работает на машине, где есть только воркер.

## 2. Принципы

- **Coordinator UI** (админка кластера): `/ui/admin/*` — только `admin`,
  bounded read model, браузер не касается БД, оба движка (sqlite/postgres)
  одинаково, секреты (`worker.token`) только админу с подтверждением.
- **Worker UI** (локальный визард в `worker-agent`): слушает только
  `127.0.0.1`, без аутентификации (локальная машина), не требует никаких
  внешних сервисов; конфигурация сохраняется в JSON-файле рядом с
  рабочим каталогом воркера; запуск воркера — отдельным процессом
  (`worker-agent --config <path>`), который визард же может остановить.
- Никаких секретов в логах и в самом UI после сохранения.

## 3. Coordinator Admin UI (`/ui/admin`)

### 3.1 Экран «Обзор системы» — `/ui/admin`
- движок БД (sqlite/postgres), версия бинарника, data dir, uptime;
- занятое место blob-хранилища (байты по артефактам) и число артефактов;
- чипы: джобы/воркеры/таски по статусам (из существующих business metrics);
- токен воркера: кнопка «Показать worker token» (admin only, подтверждение).

### 3.2 Экран «Джобы» — `/ui/admin/jobs`
- таблица: id, ворклоад, статус, владелец, создан, завершён, результат;
- фильтры: статус, ворклоад, владелец; поиск по id (частичное совпадение);
- пагинация (limit/offset), переход в существующий detail-просмотр;
- действие: cancel (существующий usecase).

### 3.3 Экран «Воркеры» — `/ui/admin/workers`
- таблица: имя, id, статус (online/busy/offline), capabilities, trust,
  владелец, последний heartbeat;
- переключение trust (trusted/untrusted) для воркеров — новый usecase;
- индикация «какие ворклоады умеет» (capabilities из реестра).

### 3.4 Экран «Юзеры и ключи» — `/ui/admin/users`
- список юзеров через userservice (существующий прокси `callUserserviceAuthed`);
- verify/unverify, promote/demote (существующие действия);
- worker-ключи юзера: просмотр (без секрета), revoke.

### 3.5 Экран «Ворклоады» — `/ui/admin/workloads`
- каталог из embedded `workloads.json` (уже есть read view);
- enable/disable: новая таблица `workload_settings (workload TEXT PK, enabled
  BOOLEAN)`, миграции sqlite + postgres; каталог читается с учётом оверрайдов;
- параметры: схема, reduction, upload_ready, ui_elements (read-only).

### 3.6 Экран «Настройки» — `/ui/admin/settings`
- read-only конфигурация без секретов: addr, storage dir, docs dir,
  лимиты (max upload, attempts, quorum), lease/reaper интервалы, engine;
- подсказки: где лежат миграции, как сделать бэкап sqlite (скопировать файл).

### 3.7 API (все — admin, JSON)

| Метод | Путь | Назначение |
| --- | --- | --- |
| GET | `/ui/admin/api/system` | движок, версия, storage usage, counts, worker token (admin) |
| GET | `/ui/admin/api/jobs` | пагинированный список с фильтрами |
| GET | `/ui/admin/api/workers` | список воркеров |
| POST | `/ui/admin/api/workers/{id}/trust` | переключить trust |
| GET | `/ui/admin/api/users` | список юзеров (userservice) |
| GET | `/ui/admin/api/workloads` | каталог + enabled |
| POST | `/ui/admin/api/workloads/{name}/enabled` | enable/disable |
| GET | `/ui/admin/api/settings` | конфигурация (без секретов) |

## 4. Worker Setup UI (в бинарнике `worker-agent`)

### 4.1 Как это выглядит

```bash
curl -fsSL .../install.sh | bash -s worker
worker-agent setup          # печатает URL и открывает браузер
# → http://127.0.0.1:12700 — локальный визард, ничего устанавливать не нужно
```

### 4.2 Визард (шаги)

1. **Координатор**: URL (например `http://192.168.1.10:8080`) + способ
   аутентификации: токен (serve) или worker key (кластер);
2. **Рабочий каталог**: путь + имя машины (WORKER_NAME);
3. **Проверка**: кнопка «Проверить соединение» — `GET /health` координатора
   (+ обмен ключа, если key), версии; чек-лист зависимостей (Python 3,
   scimesh) с командой установки;
4. **Запуск**: «Запустить воркер» — визард сохраняет `config.json` и
   запускает `worker-agent --config <path>` отдельным процессом.

### 4.3 Статусная страница (та же вкладка после запуска)

- состояние: не запущен / запущен, worker id, зарегистрирован ли;
- статистика: забрано задач, выполнено, ошибок, последний heartbeat;
- runtime: python, scimesh, capabilities (из каталога);
- кнопки: Остановить / Запустить / Открыть лог (хвост).

### 4.4 API визарда (локальный HTTP, 127.0.0.1)

| Метод | Путь | Назначение |
| --- | --- | --- |
| GET | `/api/status` | конфиг (без секрета), состояние, статистика |
| POST | `/api/config` | сохранить конфигурацию в `config.json` |
| POST | `/api/test` | проверка соединения с координатором |
| POST | `/api/start` | запустить воркера (self `--config`) |
| POST | `/api/stop` | остановить |
| GET | `/api/logs` | хвост лога воркера |

### 4.5 Изменения в агенте

- `worker-agent setup [--port 12700] [--no-open]` — локальный сервер визарда;
- `worker-agent --config <path>` — запуск демона из JSON-конфига (URL, токен
  или ключ, work dir, имя, task runner); env-переменные имеют приоритет;
- `worker-agent --check [--coordinator-url URL]` — пинг `/health` + версии,
  exit 0/1 (используется визардом на шаге 3);
- `config.json` по умолчанию: `~/.scimesh-worker/config.json` (переопределяется
  через `WORKER_CONFIG`); секрет хранится с правами 0600.

## 5. Компоненты и структура кода

```
# Coordinator Admin UI (бинарник coordinator)
coordinator/internal/transport/http/
  ui_admin.go            # admin-хендлеры + admin.html
  server.go              # роуты /ui/admin/* (requireAdmin)

coordinator/internal/usecase/
  admin.go               # AdminSystem/ListJobsAdmin/ListWorkersAdmin/WorkloadSettings
  ports.go               # + PaginatedJobs(ctx, filter, limit, offset)

coordinator/internal/storage/{sqlite,postgres}/
  ui_read_repo.go        # + ListJobsPaginated, StorageStats
  migrations/*.sql       # + workload_settings

# Worker Setup UI (бинарник worker-agent)
coordinator/internal/agent/
  setup/                 # локальный визард: сервер, шаблоны, API, config.json
  setup_ui.go            # worker-agent setup (сервер 127.0.0.1:12700)
  configfile.go          # --config <path>: JSON-конфиг демона
  check.go               # --check: пинг /health + версии
```

## 6. Милестоуны

- **M1. Admin-фундамент**: экран «Система» + таблица джобов (фильтры,
  пагинация) + storage usage. API + шаблоны + тесты.
- **M2. Admin-управление**: воркеры (trust), юзеры/ключи (userservice),
  ворклоады enable/disable (таблица `workload_settings` + миграции обоих
  движков), настройки (read-only).
- **M3. Worker Setup (в worker-agent)**: `setup`-сервер с визардом
  (config/test/start/stop/logs), `--config`, `--check`; E2E: визард запускает
  реального воркера, тот регистрируется и берёт джоб.
- **M4. Полировка и верификация**: пустые состояния, mobile, копирование,
  E2E в браузере (admin-флоу и setup-флоу с реальным worker-agent),
  обновление mkdocs/standalone.md, CI зелёный.

## 7. Тестирование

- **Go (unit)**: usecase admin (memstore + sqlite), permission-тесты
  (admin vs user → 403), фильтры/пагинация, enable/disable ворклоадов;
  агент: визард API (config persist 0600, test без/с координатором,
  start/stop), `--config`, `--check` (с/без координатора).
- **Go (integration, postgres)**: пагинация и storage-метрики на реальной БД.
- **E2E (браузер)**: вход админом → `/ui/admin` показывает реальную систему;
  на «голой» машине: `worker-agent setup` → визард → запуск → воркер
  регистрируется в координаторе и берёт джоб.
- **CI**: существующие джобы + новые тесты в общем `go test -race`; sqlite и
  postgres пути одинаково зелёные.

## 8. Открытые решения

- **enable/disable ворклоадов**: хранить в `workload_settings` (миграции
  обоих движков); решение принято — таблица добавляется в M2.
- **Токен в UI**: только админ, с подтверждением; в командах визарда —
  плейсхолдер, чтобы не светить секрет в логах истории.
- **`worker-agent --check`**: без регистрации, только health + версии.
- **Локальный визард без аутентификации**: сервер слушает только
  `127.0.0.1`; любой локальный процесс может управлять воркером — это
  сознательное упрощение для одной машины.
- Скоуп v1: без редактирования конфигурации координатора и без управления
  миграциями из UI (это задача `setup`/CLI).
