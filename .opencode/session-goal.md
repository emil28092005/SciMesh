# Session Goal

возьми это себе как автономную задачу и доведи этот пайплайн до рабочего вручную состояния

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

## Progress (ночная сессия)
- (начало) Пустое имя воркера: валидация добавлена в `domain.NewWorker`, добавлен `TestNewWorkerRejectsBlankName`; в `worker_test.go` сломан тест из-за `fixedTime` vs `testNow` — на паузе, продолжить.

## Progress (прошлая работа — выполнено)
- ✅ Релизы alpha.12–15: фикс версии визарда, venv task_runner, preflight через venv, MkdirAll при скачивании wheel, кнопка Install в шаблоне.
- ✅ Docker E2E: координатор+воркер контейнеры, установка install.sh, визард (config→install→start), воркер online, джоб completed, результат байт-в-байт = локальному эталону.
- ✅ На машине пользователя: визард alpha.15, правильный токен, venv из wheel, воркер emil-pc online, 15 пустых воркеров вычищены из БД.
- ✅ Гейт: race + lint + pytest 208.

## Completion (предыдущая задача)
COMPLETED — пайплайн доведён до рабочего состояния и проверен на релизных артефактах v1.1.0-alpha.14.
