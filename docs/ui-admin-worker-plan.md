# План: Coordinator Admin UI и Worker Setup UI

> Статус: план. Текущий `/ui` — демонстрационный control room (джобы, ворклоады, docs).
> Два новых UI решают две разные задачи: **управление платформой** (админка
> координатора) и **подключение машин как воркеров** (установочный визард).

## 1. Цель

1. **Coordinator Admin UI** (`/ui/admin`) — полноценная консоль оператора:
   система, джобы, воркеры, юзеры/роли, ворклоады, настройки, метрики.
2. **Worker Setup UI** (`/ui/workers/setup`) — пошаговый визард, который
   превращает любую машину (Linux/macOS/Windows, amd64/arm64) в воркера
   без чтения документации: выбрать платформу → получить готовую команду →
   запустить → убедиться, что машина появилась в «My machines».

Оба UI живут в бинарнике координатора (single-binary philosophy), защищены
той же сессией (JWT через userservice), браузер по-прежнему не касается БД —
только bounded read model от координатора.

## 2. Принципы

- **Не демо**: каждый экран работает от реального состояния (репозитории +
  metrics), без симуляции; пустые состояния информативны.
- **Роли**: всё в `/ui/admin` — только `admin`; `/ui/workers/setup` — любой
  аутентифицированный пользователь (ключи привязаны к аккаунту).
- **Секреты**: `worker.token` показывается только админу, с явным
  подтверждением и предупреждением; в командах setup-визарда по умолчанию
  плейсхолдер `$(coordinator token)`.
- **Новые API** — `/ui/admin/api/*` (JSON), те же middleware
  (`withUISession`, `requireAdmin`), тот же паттерн bounded read model.
- **Оба движка БД** (sqlite и postgres) поддерживаются одинаково; новые
  таблицы — миграции в обоих наборах.

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

## 4. Worker Setup UI (`/ui/workers/setup`)

### 4.1 Визард (4 шага)

1. **Платформа**: автоопределение (navigator/platform + архитектура), ручной
   выбор (linux/darwin/windows × amd64/arm64) → показ установочной команды
   (`install.sh | bash -s worker`, `install.ps1` с `SCIMESH_COMPONENT=worker`).
2. **Способ аутентификации**:
   - a) токен serve-инстанса: поле ввода + (для admin) кнопка «вставить токен»
     — забирает из `/ui/admin/api/system` (показывается только админу);
   - b) worker key: форма создания ключа (существующий
     `POST /ui/api/worker-keys`) — для кластерного режима;
3. **Готовая команда**: полный блок `export ...` + `worker-agent`
   (COORDINATOR_URL, токен/ключ, WORK_DIR, WORKER_NAME) + кнопка Copy;
   чек-лист зависимостей: Python 3, scimesh (`pip install scimesh` или
   `SCIMESH_PIP_PACKAGE`).
4. **Проверка**: `worker-agent --check` (новый флаг: `GET /health` +
   `--version`, без регистрации) + инструкция «появится в My machines».

### 4.2 Дополнительно
- сайдбар «My machines» (существующий список ключей с revoke);
- ссылка на этот же флоу из дашборда и из `install.sh worker` (печать URL).

### 4.3 Изменения в агенте
- `worker-agent --check [--coordinator-url URL]`: пингует координатор,
  печатает версии и пригодность (python3/scimesh видимость), exit 0/1.

## 5. Компоненты и структура кода

```
coordinator/internal/transport/http/
  ui_admin.go            # новые admin-хендлеры + шаблоны admin/*.html
  ui_worker_setup.go     # setup-визард + worker-setup.html
  templates/admin.html   # (заменяет текущий минимальный)
  templates/worker-setup.html
  server.go              # роуты /ui/admin/* (requireAdmin), /ui/workers/setup

coordinator/internal/usecase/
  admin.go               # AdminSystem/ListJobsAdmin/ListWorkersAdmin/WorkloadSettings
  ports.go               # + PaginatedJobs(ctx, filter, limit, offset)

coordinator/internal/storage/{sqlite,postgres}/
  ui_read_repo.go        # + ListJobsPaginated, StorageStats
  migrations/*.sql       # + workload_settings

coordinator/internal/agent/
  main.go / daemon       # + --check mode

coordinator/cmd/coordinator/main.go  # (serve уже отдаёт UI; правки не нужны)
```

## 6. Милестоуны

- **M1. Admin-фундамент**: экран «Система» + таблица джобов (фильтры,
  пагинация) + storage usage. API + шаблоны + тесты.
- **M2. Admin-управление**: воркеры (trust), юзеры/ключи (userservice),
  ворклоады enable/disable (таблица `workload_settings` + миграции обоих
  движков), настройки (read-only).
- **M3. Worker Setup**: визард 4 шага + `worker-agent --check` + admin-кнопка
  «вставить токен» + ключевой флоу (токен/ключ).
- **M4. Полировка и верификация**: пустые состояния, mobile, копирование,
  E2E в браузере (admin-флоу и setup-флоу с реальным worker-agent),
  обновление mkdocs/standalone.md, CI зелёный.

## 7. Тестирование

- **Go (unit)**: usecase admin (memstore + sqlite), permission-тесты
  (admin vs user → 403), фильтры/пагинация, enable/disable ворклоадов
  (схема учитывается), `--check` (с/без координатора).
- **Go (integration, postgres)**: пагинация и storage-метрики на реальной БД.
- **E2E (браузер)**: вход админом → `/ui/admin` показывает реальную систему;
  `/ui/workers/setup` → сгенерированная команда запускает `worker-agent` на
  той же машине → воркер появляется в «My machines» и берёт джоб.
- **CI**: существующие джобы + новые тесты в общем `go test -race`; sqlite и
  postgres пути одинаково зелёные.

## 8. Открытые решения

- **enable/disable ворклоадов**: хранить в `workload_settings` (миграции
  обоих движков); решение принято — таблица добавляется в M2.
- **Токен в UI**: только админ, с подтверждением; в командах визарда —
  плейсхолдер, чтобы не светить секрет в логах истории.
- **`worker-agent --check`**: без регистрации, только health + версии.
- Скоуп v1: без редактирования конфигурации и без управления миграциями
  из UI (это задача `setup`/CLI).
