# Session Goal

выполни план на ночь (полный план ниже — автономное исполнение, пользователь спит)

## Ночная сессия — полный план (пользователь спит, 2026-08-03)

### 1. Баги (реальные, найденные в проде)
- [ ] **Пустое имя воркера**: регистрация принимает `name=""` (в БД пользователя было 15 таких). Фикс: валидация в `domain.NewWorker` (TrimSpace != "") + регрессионный тест. Частично начат — в `worker_test.go` сломан тест (`fixedTime` vs `testNow`), доделать.
- [ ] **`worker-agent --check` не видит venv**: проверяет только системный python3; после Install воркер работает через `~/.scimesh-worker/venv`. Выровнять с визардом (пробовать venv, если он есть).

### 2. Визуальный долг — рестайлинг старых страниц в дизайн-систему админки (#0b0e13, карточки, pill-статусы, кнопки)
- [ ] `new-job.html` (форма запуска вычислений, UIElement-поля сохранить)
- [ ] `job.html` (детали джоба: шарды, артефакты, прогресс, JS-логику сохранить)
- [ ] `workloads.html`
- [ ] `add-worker.html`
- [ ] `profile.html`
- [ ] Браузерная проверка каждой страницы (playwright).

### 3. Технический долг (паритет движков, обещанный планом)
- [ ] **Postgres integration-тесты для admin-методов**: `SetTrust`, `WorkloadSettings` (Get/List/Set), `ListJobsPaginated`, метрики (`JobCountsByDay`, `TaskStats`, `ArtifactSizeByKind`, `DatabaseSizeBytes`) — сейчас покрыт только sqlite. (build tag integration, docker postgres как в CI.)

### 4. Фичи из плана (v2-задел)
- [ ] **Статистика воркера в визарде**: статусная страница — claimed/completed/failed/heartbeat, парсинг из `worker.log` (обещано в docs/ui-admin-worker-plan.md §4).
- [ ] **Admin: prune артефактов** (danger zone): удаление артефактов completed-джобов старше N дней (строки + blob-файлы), освобождает место; кнопка в Settings с подтверждением.
- [ ] **Admin: удаление offline-воркеров** (устаревшие, как 15 мусорных) — кнопкой в Workers вместо SQL.

### 5. Гигиена
- [ ] **`pyproject.toml` версия 0.1.0 → setuptools_scm** (версия из git-тегов), убрать sed-костыль из release.yml; локальный `pip install -e .` покажет правильную версию.
- [ ] Синхронизация STATUS.md / README с фактическим состоянием (админка, визард, wheel, авто-открытие, restyle).

### 6. Верификация и релиз
- [ ] Полный гейт: `go test -race ./...`, golangci-lint `--build-tags=integration`, pytest.
- [ ] Браузерные проверки: админка (все секции), визард (install+start), рестайленные страницы.
- [ ] E2E quorum-флоу (untrusted воркер через worker key → джоб требует quorum) в Docker, если останется время.
- [ ] Релиз `v1.1.0-alpha.16` (бинарники + wheel), проверка ассетов.
- [ ] Обновить этот файл: прогресс по пунктам, COMPLETED в конце.

## Plan (предыдущая задача — выполнена)
1–7. Docker E2E пайплайна «install как человек → serve → визард → воркер → джоб» — выполнено, см. Progress ниже.

## Progress (утро: similarity-search-parallel)
- ✅ Новый workload `similarity-search-parallel@1.0.0` (отдельная версия, как просил пользователь): подкласс `SimilaritySearchSDKWorkload` + параллельное ядро `search_parallel/core.py` — fingerprinting+скоринг шарда через `ThreadPoolExecutor` (параметр `threads`, default CPU count); `pool.map` сохраняет порядок строк, поэтому merge идентичен последовательному (`_HeapEntry`) и результат **байт-в-байт** равен `similarity-search` при любом числе потоков.
- ✅ Тесты: байт-в-байт vs эталон для threads 1/2/4 с намеренными связями (изомеры, дубликаты), executor-прогон, валидация параметров, регистрация манифеста; 213 pytest зелёные.
- ✅ Экспортирован в каталог координатора (5 ворклоадов), Go-тесты зелёные; release v1.1.0-alpha.19 (бинарники + wheel `scimesh-1.1.0a19`).
- ✅ Распределённый E2E в Docker: воркер с wheel a19, джоб similarity-search-parallel (threads=4) → completed → результат байт-в-байт = локальному эталону.
- ✅ Бинарники на машине пользователя обновлены до alpha.19 (для появления ворклоада в UI нужен рестарт serve + переустановка рантайма воркера).
- Примечание: в CPython потоки не ускоряют чистый RDKit-путь (GIL), но структура готова к ядрам, отпускающим GIL (numpy и т.п.); при желании можно добавить процесс-пул как отдельный workload в будущей версии протокола.

## Progress (ночная сессия)
- ✅ **П.1 Пустое имя воркера**: `domain.NewWorker` нормализует/отклоняет пустое имя + `TestNewWorkerRejectsBlankName` (починен `fixedTime`→`testNow`).
- ✅ **П.2 `--check`**: пробует managed venv (если установлен) + реальный пробинг учётки (exchange ключа / claim-пробa) — `CheckAuth` + тесты; на машине пользователя: `✓ auth: credential accepted`, venv python, scimesh installed.
- ✅ **П.3 Рестайлинг**: единый CSS-partial `ui-base.html` (дизайн-система админки), все 5 страниц (new-job, job, workloads, add-worker, profile) переведены, проверены в браузере без console-ошибок.
- ✅ **П.4 Postgres integration**: admin-методы (SetTrust, ListJobsPaginated, TaskCounts, byDay/byWorkload, TaskStats, ArtifactSize, DB size, WorkloadSettings) + `ensureMigrated` для порядка запуска; весь suite зелёный.
- ✅ **П.5 Статистика воркера в визарде**: лог `task claimed` в агента + парсинг registered/claimed/completed/failed → `/api/status.stats` + карточки в статусной странице + тест.
- ✅ **П.6 Prune артефактов**: `JobRepository.ListCompletedBefore/Delete` (sqlite+postgres+memstore), usecase `PruneArtifacts` (каскад + blob-файлы), `POST /ui/admin/api/prune`, кнопка в Settings, тесты (sqlite+usecase); E2E: 200, freed bytes.
- ✅ **П.7 Удаление offline-воркеров**: `WorkerRepository.Delete` + `Admin.RemoveWorker` (только offline) + `POST /ui/admin/api/workers/{id}/remove` + кнопка в Workers + тест; E2E: 204, строка удалена.
- ✅ **П.8 setuptools_scm**: `dynamic = ["version"]`, CI-джоба wheel без sed (fetch-depth 0); локальная проверка: wheel на теге = `scimesh-1.1.0a16-py3-none-any.whl` (совпадает с Go-нормализацией); релизный ассет подтверждён.
- ✅ **П.9 Docs**: STATUS.md синхронизирован (админка, визард, wheel, CTX-19/20 implemented).
- ✅ **П.10 (доп.) Баг в serve-режиме**: worker-key exchange был недоступен снаружи (userservice на loopback) — добавлен прокси `POST /worker-tokens/exchange` на координаторе, `PublicUserserviceURL=""` + fallback на origin в add-worker. Проверено E2E.
- ✅ **П.10 Quorum E2E (Docker)**: 2 untrusted-воркера с разными ключами (alice/bob) → джоб completed 3/3, в task_results по 2 голоса от разных владельцев с одинаковым sha256 → результат байт-в-байт = локальному эталону.
- ✅ **Финальный гейт**: `go test -race ./...` ✅, golangci-lint 0 issues ✅, pytest 208 ✅, postgres integration ✅, Windows кросс-сборка ✅.
- ✅ **Релиз v1.1.0-alpha.16** (бинарники + wheel `scimesh-1.1.0a16`), все воркфлоу success; бинарники на машине пользователя обновлены до alpha.16.

## Progress (прошлая работа — выполнено)
- ✅ Релизы alpha.12–15: фикс версии визарда, venv task_runner, preflight через venv, MkdirAll при скачивании wheel, кнопка Install в шаблоне.
- ✅ Docker E2E: координатор+воркер контейнеры, установка install.sh, визард (config→install→start), воркер online, джоб completed, результат байт-в-байт = локальному эталону.
- ✅ На машине пользователя: визард alpha.15, правильный токен, venv из wheel, воркер emil-pc online, 15 пустых воркеров вычищены из БД.
- ✅ Гейт: race + lint + pytest 208.

## Completion
COMPLETED — ночной план выполнен полностью (10 пунктов + 2 найденных бага, включая E2E quorum на релизном коде). Все гейты зелёные, релиз v1.1.0-alpha.16 опубликован.

## Completion (предыдущая задача)
COMPLETED — пайплайн доведён до рабочего состояния и проверен на релизных артефактах v1.1.0-alpha.14.
