# Архитектура координатора

Карта кода. Читать сверху вниз: сначала «где что лежит», потом «как проходит
запрос», в конце — «куда добавлять новое».

---

## 1. Четыре слоя

```
   infra       конфиг, пул БД, часы, HTTP-сервер, reaper    ← драйверы
   transport   HTTP-хендлеры          ← входящее: кто зовёт нас
   storage     репозитории на SQL     ← исходящее: кого зовём мы
   usecase     операции + ПОРТЫ (интерфейсы)                ← прикладные правила
   domain      Task, Job и их инварианты                    ← бизнес-правила

                       ┌── transport ──┐
   domain ◄── usecase ◄┤               ├◄── infra
                       └── storage ────┘
```

`transport` и `storage` — один и тот же слой (в книгах он зовётся «адаптеры»),
просто разделённый по направлению: транспорт принимает запросы снаружи, storage
обращается наружу сам. Так путь к файлу говорит о его роли, а не о категории.

**Единственное правило:** зависимости идут только внутрь. `domain` не импортирует
ничего из проекта. `usecase` видит только `domain`. `transport` и `storage` не
знают друг о друге.

Проверить в любой момент:

```sh
go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}' ./internal/domain | grep internal
# пусто = правило соблюдено
```

---

## 2. Где что лежит

| Файл | Что внутри | Строк |
| --- | --- | --- |
| `domain/task.go` | `Task` и **все** переходы состояний: аренда, завершение, провал, истечение | ~245 |
| `domain/job.go` | `Job`, разбиение на чанки, вывод статуса из счётчиков задач | ~107 |
| `domain/errors.go` | Нарушения бизнес-правил (`ErrLeaseConflict`, `ErrStaleAttempt`, …) | ~18 |
| `usecase/ports.go` | **Порты**: `TaskRepository`, `JobRepository`, `TxManager`, `Clock` | ~79 |
| `usecase/task.go` | Операции над задачей: claim, renew, complete, fail, expire | ~200 |
| `usecase/job.go` | Операции над job: create, status, results, stitch | ~180 |
| `usecase/dto.go` | Входные структуры юзкейсов | ~51 |
| `transport/http/server.go` | Роутер и сборка middleware | ~60 |
| `transport/http/handlers.go` | По хендлеру на эндпоинт | ~180 |
| `transport/http/dto.go` | JSON-форматы запросов и ответов | ~118 |
| `transport/http/middleware.go` | request-ID, access-лог, bearer-авторизация | ~103 |
| `transport/http/errors.go` | Маппинг доменных ошибок в HTTP-коды | ~55 |
| `storage/postgres/task_repo.go` | SQL по задачам, включая атомарный claim | ~109 |
| `storage/postgres/job_repo.go` | SQL по job'ам | ~39 |
| `storage/postgres/tx.go` | `TxManager`: транзакция через контекст | ~65 |
| `infra/*.go` | Конфиг, пул, часы, сервер, reaper | ~240 |
| `cmd/coordinator/main.go` | **Composition root** — единственное место со всеми конкретными типами | ~73 |

---

## 3. Трасса запроса: `POST /tasks/claim`

Как воркер получает задачу. Четыре остановки, по одной на слой:

```
  ①  transport/http/handlers.go → handleClaim
        разбирает JSON, отдаёт usecase.ClaimTaskInput
        │
        ▼
  ②  usecase/task.go → ClaimTask.Execute
        сначала подчищает протухшие аренды, потом просит одну задачу
        через ПОРТ TaskRepository (реализацию не знает)
        │
        ▼
  ③  usecase/ports.go → TaskRepository.ClaimNext
        контракт: «атомарно выдай одну задачу»
        │
        ▼
  ④  storage/postgres/task_repo.go → claimNextSQL
        SELECT ... FOR UPDATE SKIP LOCKED + UPDATE одним запросом
```

Обратно поднимается `*domain.Task`, юзкейс сужает его до `domain.ClaimedTask`
(воркеру не отдаём `version`, `max_attempts` и чужие ошибки), хендлер
превращает в JSON. Пустая очередь — это `nil, nil` на шаге ② и `204` на ①.

**Трасса `POST /tasks/{id}/result`** такая же, но с одним отличием: решение
принимает **сущность**, а не юзкейс.

```
  handlers.go → CompleteTask.Execute → tx.WithinTx(
        GetForUpdate  →  task.CompleteWith(...)  ←── ЗДЕСЬ правила
                              │                      (чужая аренда? устаревший
        Update  ←─────────────┘                       attempt? повтор того же
        syncJobStatus                                 манифеста?)
  )
```

---

## 4. Куда добавлять новое

| Хочу… | Правлю |
| --- | --- |
| новое бизнес-правило (когда задачу можно повторить) | `domain/task.go` + тест рядом |
| новую операцию (отменить job) | `usecase/job.go` + порт в `ports.go`, если нужен новый запрос к БД |
| новый HTTP-эндпоинт | `transport/http/handlers.go` + маршрут в `server.go` + DTO в `dto.go` |
| новый SQL-запрос | `storage/postgres/*_repo.go` |
| новую настройку | `infra/config.go` + `.env.example` |
| поменять код ответа на ошибку | `transport/http/errors.go` |

**Правило при сомнении:** если код можно описать фразой «когда X, то Y» без
упоминания HTTP, SQL и конфигов — это `domain`. Если он оркеструет несколько
шагов и транзакцию — `usecase`. Если знает про JSON — `transport`, про SQL — `storage`.

---

## 5. Три вещи, которые надо понять один раз

**Порты объявляет потребитель.** `TaskRepository` описан в `usecase/ports.go`, а
реализован в `storage/postgres`. Поэтому `usecase` не импортирует `storage` —
стрелка зависимости смотрит внутрь, хотя вызов на рантайме идёт наружу.

**Транзакция едет в контексте.** `TxManager.WithinTx` кладёт `pgx.Tx` в контекст
по неэкспортируемому ключу; репозитории достают её через `conn(ctx, pool)`.
Благодаря этому юзкейс говорит «сделай это атомарно», ни разу не упомянув pgx.

**Атомарный claim нельзя разложить на шаги.** `ClaimNext` — один SQL-запрос,
потому что `SELECT` + отдельный `UPDATE` вернул бы гонку, при которой одну
задачу выдают двум воркерам. Поэтому `ClaimTask.Execute` выглядит тонким: там
нечего оркестровать, вся гарантия — внутри запроса.

---

## 6. Что уже работает, а что заглушка

Работает: слои и проводка, роутинг, авторизация, access-лог, маппинг ошибок,
транзакции, graceful shutdown, миграции, **весь domain с 12 юнит-тестами без БД**.

Заглушки (`ErrNotImplemented` → HTTP 501): методы репозиториев. SQL для двух
главных операций уже написан в `task_repo.go` — `claimNextSQL` и
`expireLeasesSQL`, осталось их подключить.
