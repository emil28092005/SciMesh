# План реализации: Coordinator Admin UI и Worker Setup UI

> Статус: **в работе**. Визуал утверждён — мокапы:
> [`ui-mockups/coordinator-admin.html`](ui-mockups/coordinator-admin.html),
> [`ui-mockups/worker-setup.html`](ui-mockups/worker-setup.html).
> Два UI живут в **разных бинарниках**: админка кластера — в `coordinator`
> (`/ui/admin`), визард установки воркера — в `worker-agent`
> (`worker-agent setup`, локально на 127.0.0.1).

## 1. Цель

1. **Coordinator Admin UI** — консоль админа распределённого кластера:
   System, Jobs, Workers, Users & keys, Workloads, Metrics, Settings.
   Реальные данные, никакой симуляции; только роль `admin`.
2. **Worker Setup UI** — локальный визард в бинарнике `worker-agent` для
   машины, на которой стоит только воркер: подключение (URL + токен/ключ),
   параметры машины, preflight-проверка, запуск/остановка, статус и лог.

## 2. Что уже есть в коде (фундамент)

- `requireAdmin` middleware и `/ui/admin` (`internal/transport/http/ui_admin.go`)
  — сейчас минимальная панель: прокси `promote/demote/verify/unverify` в
  userservice.
- Userservice (`internal/userservice/`): `POST /users/{id}/promote|demote|
  verify|unverify` (admin), `GET/POST/DELETE /worker-keys` (per-user),
  `POST /worker-tokens/exchange`. **Нет**: list users, list all keys.
- `domain.Worker.TrustLevel` (`trusted`/`untrusted`) + quorum для untrusted —
  колонка в БД есть, нужен только метод смены и отображение.
- `domain.Job.OwnerID *uuid.UUID` — владелец джобы из JWT `sub` (может быть nil).
- Каталог ворклоадов `internal/workloads` (embedded `workloads.json`) —
  enable/disable кладём поверх через таблицу настроек.
- UIReadRepo (`ui_read_repo.go` в обоих движках) — bounded read model для UI.
- Агент целиком конфигурируется env (таблица в `mkdocs/sdk/worker-integration.md`);
  `--config` маппится на ту же поверхность `agent.Config`.

## 3. Coordinator Admin UI

### 3.1 Страницы ↔ API ↔ данные

Все API — JSON, под `withUISession` + `requireAdmin`, префикс
`/ui/admin/api/`. Страница `admin.html` — оболочка из мокапа (сайдбар,
7 разделов), данные подтягивает JS через fetch (паттерн dashboard).

| Раздел | API | Данные |
| --- | --- | --- |
| System | `GET /system` | version, uptime, storage stats, health (db/userservice/reducer), node info |
| Jobs | `GET /jobs?status=&page=` | пагинированный список + счётчики по статусам; owner email резолвится через userservice |
| Workers | `GET /workers`, `POST /workers/{id}/trust` | список + trust, смена trust (trusted/untrusted) |
| Users & keys | `GET /users`, `POST /users/{id}/role`, `GET /worker-keys`, `POST /worker-keys/{id}/revoke` | прокси/агрегация userservice |
| Workloads | `GET /workloads`, `POST /workloads/{name}/enabled` | каталог + persisted enabled-флаг |
| Metrics | `GET /metrics` | jobs/day (7d), jobs by workload, shards, failure rate, avg shard time |
| Settings | `GET /settings`, `POST /token/reveal` | read-only конфиг + reveal токена (audit-лог) |

### 3.2 Userservice — новые эндпоинты

- `GET /users` (admin) — `id, email, role, verified, created_at`.
- `GET /worker-keys/all` (admin) — все ключи + owner email.
- Репозитории: `ListUsers(ctx)`, `ListWorkerKeysAll(ctx)` в `memstore` и
  `storage/sqlite` (+ тесты). Координатор вызывает их через
  `callUserserviceAuthed` и отдаёт в свой bounded API — браузер юзерсервис
  не касается.

### 3.3 Storage координатора — новые методы

- `WorkerRepository.SetTrust(ctx, id, trust)` — оба движка + тесты.
- `UIReadRepo.ListJobsPaginated(ctx, status string, limit, offset int)`
  → `([]UIJob, total int, counts map[string]int)` — фильтр по статусу,
  счётчики для табов.
- `UIReadRepo.JobMetrics(ctx, since time.Time)` → jobs/day, by workload,
  shards completed/failed, avg shard duration (из `tasks`).
- `UIReadRepo.StorageStats(ctx)` → суммы байт по kind артефактов
  (datasets/artifacts) + размер файла БД (sqlite: `page_count*page_size`;
  postgres: `pg_database_size(current_database())`).
- Миграция `0002_workload_settings` (оба движка):
  `workload_settings(workload TEXT PRIMARY KEY, enabled BOOL NOT NULL,
  updated_at TIMESTAMP NOT NULL)`; отсутствие строки = enabled (default).
  Repo: `WorkloadSettingsRepo{ List, Set }`.
- **Enforcement**: `SubmitDataset` отклоняет disabled-ворклоад;
  `/ui/api/workloads` и форма new-job помечают disabled (скрываем из выбора).

### 3.4 Usecase — `internal/usecase/admin.go`

`AdminSystem`, `ListJobsAdmin(status,page)` (+owner emails), `ListWorkersAdmin`,
`SetWorkerTrust`, `ListUsersAdmin`, `SetUserRole` (через userservice promote/demote),
`ListWorkerKeysAdmin`, `RevokeWorkerKeyAdmin`, `ListWorkloadsAdmin`,
`SetWorkloadEnabled`, `AdminMetrics`, `RevealWorkerToken` (читает
`worker.token`/env, пишет audit-лог).

### 3.5 Шаблон

`templates/admin.html` заменяется на дизайн мокапа: сайдбар (Operate /
Access / Platform), topbar с env-бейджем (serve/postgres, addr), 7 разделов,
рендер через JS. Имя ворклоада — реальное `descriptor-batch`.

## 4. Worker Setup UI (бинарник `worker-agent`)

### 4.1 Новые файлы

```
coordinator/internal/agent/
  configfile.go        # ConfigFile (json), Load/Save (0600), путь по умолчанию
  check.go             # CheckCoordinator(url, auth) — health + версии + python/scimesh
  setupui/
    server.go          # локальный HTTP 127.0.0.1:12700, API + запуск/остановка
    template.html      # визард + статус (по мокапу), go:embed
coordinator/cmd/worker-agent/main.go   # + setup / --config / --check
```

### 4.2 CLI

- `worker-agent setup [--port 12700] [--no-open]` — визард; печатает URL,
  открывает браузер.
- `worker-agent --config <path>` — демон из JSON-конфига; env имеет приоритет.
- `worker-agent --check [--coordinator-url URL]` — пинг `/health`, auth
  (token → claim-endpoint 401/200 probe или exchange для ключа), python3 +
  `import scimesh`; exit 0/1. Используется визардом на шаге 3.

### 4.3 config.json

`~/.scimesh-worker/config.json` (переопределяется `WORKER_CONFIG`), права 0600:
```json
{
  "coordinator_url": "http://192.168.1.10:8080",
  "token": "…",                     // или
  "worker_key": "…", "userservice_url": "http://…:8081",
  "work_dir": "…", "worker_name": "emil-laptop",
  "cpu_count": 8, "memory_mb": 16384,
  "task_runner": ["python", "-m", "scimesh.worker.task"]
}
```

### 4.4 API визарда (только 127.0.0.1, без auth)

| Метод | Путь | Назначение |
| --- | --- | --- |
| GET | `/api/status` | конфиг (секрет маскирован), running (pid alive), статистика из лога, runtime |
| POST | `/api/config` | валидация + сохранение `config.json` (0600) |
| POST | `/api/test` | preflight: coordinator reachable, auth ok, python, scimesh |
| POST | `/api/start` | spawn `worker-agent --config <path>` (лог → `worker.log`, pid-файл) |
| POST | `/api/stop` | SIGTERM по pid-файлу |
| GET | `/api/logs?tail=` | хвост `worker.log` |

Статистика — парсинг лога (registered/claimed/completed/failed/heartbeat).
Spawner — интерфейс, в тестах подменяется.

## 5. Милестоуны

- **M1. Admin-фундамент**: оболочка `admin.html` по мокапу + `GET /system`
  (storage stats, health, node) + `GET /jobs` (фильтр+пагинация+счётчики) +
  `GET /metrics`. Тесты: pagination/metrics/storage-stats на sqlite,
  permission-тесты 403.
- **M2. Access & Platform**: userservice `ListUsers`/`ListWorkerKeysAll`
  (mem+sqlite+http), `SetTrust` (оба движка), users/keys/trust API и
  разделы, `workload_settings` миграция 0002 + enable/disable + enforcement
  в `SubmitDataset` и форме, settings + token reveal (audit). Тесты на каждый
  слой.
- **M3. Worker Setup**: `configfile.go`, `--config`, `--check`, `setupui`
  (server + шаблон по мокапу, 6 API), тесты (roundtrip конфига, права 0600,
  check с httptest, API визарда с подменённым spawner).
- **M4. E2E + docs**: браузерный E2E (admin видит реальные данные; визард
  запускает реального воркера против тестового координатора → регистрация →
  джоб), обновить `mkdocs/index.md`, `mkdocs/standalone.md`
  (`worker-agent setup` вместо ручных export), README, чекбоксы CTX-19/20.

## 6. Тестирование

- **Go unit**: usecase admin (fakes + sqlite), repos обоих движков
  (SetTrust, pagination, metrics, settings, storage stats), userservice
  List/ListAll (mem+sqlite), HTTP permission (user → 403 на все `/ui/admin/api/*`),
  enforcement disabled-ворклоада, configfile/check/setupui агента.
- **Go integration (postgres)**: миграция 0002 и parity новых методов —
  через существующий docker-хелпер, skip без docker.
- **E2E браузер**: M4.

## 7. Открытые решения (зафиксировано)

- Локальный визард без аутентификации — слушает только 127.0.0.1.
- Settings-раздел v1 — read-only + reveal токена; редактирование конфигурации
  и prune/reset (danger zone) — v2, в UI помечены как таковые.
- `owner` джобы: email из userservice по `OwnerID`; при nil — «cluster token».
- Статистика воркера в визарде — из лога, без новых эндпоинтов координатора.
