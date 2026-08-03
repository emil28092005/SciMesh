# Session Goal

возьми это себе как автономную задачу и доведи этот пайплайн до рабочего вручную состояния

## Plan
1. Выпустить v1.1.0-alpha.12 с фиксом версии визарда (agent.Version до dispatch setup).
2. Поднять Docker-сеть с двумя контейнерами (ubuntu:24.04): координатор и отдельная машина воркера.
3. В контейнере координатора — установка через `install.sh` как человек (SCIMESH_AUTO_START=0), затем `coordinator serve --addr 0.0.0.0:8080 --workers 0`.
4. В контейнере воркера — установка `install.sh | bash -s worker`, затем `worker-agent setup` (headless), визард через API: config → test → install runtime (wheel из релиза + rdkit) → start.
5. Проверить: воркер online в админке (admin-сессия через curl).
6. Запустить реальный джоб: загрузить TSV, дождаться completion, скачать результат, проверить корректность.
7. Финальный гейт: go tests + lint + pytest; COMPLETED в session-goal.md.

## Progress (дополнение: финальная доводка на машине пользователя)
- Обнаружен и исправлен баг: кнопка Install в шаблоне визарда не применялась (python replace молча не сработал) — кнопка добавлена по-настоящему, проверена в браузере.
- Обнаружен баг: DownloadWheel/handleInstallRuntime не создавали ~/.scimesh-worker (конфиг сохраняется на шаге 4) → "open ...: no such file or directory" — исправлено MkdirAll в обоих местах; выпущен v1.1.0-alpha.15.
- На машине пользователя всё доведено до конца: визард alpha.15 с кнопкой Install, правильный токен (мой предыдущий токен в конфиге был неверным — видим только префикс), venv установлен из wheel релиза, preflight зелёный через venv, воркер emil-pc online в админке.
- Почищены 15 мусорных пустых воркеров из БД.

## Progress
- ✅ Релиз v1.1.0-alpha.12 (фикс agent.Version до dispatch setup — визард мог скачать wheel своей версии).
- ✅ Docker E2E на ubuntu:24.04, два контейнера в сети scimesh-e2e-net.
- ✅ Координатор: install.sh (SCIMESH_AUTO_START=0) → `coordinator serve --addr 0.0.0.0:8080 --workers 0`; admin-пароль и worker.token получены.
- ✅ Воркер: install.sh -s worker → визард headless: /api/config (URL+token+work_dir+name) → /api/runtime/install (скачал wheel scimesh-1.1.0aXX из релиза + rdkit, ~20s) → /api/start; task_runner автоматически указан на venv python.
- ✅ Воркер online в /ui/admin/api/workers со всеми 4 capabilities.
- ✅ Джоб similarity-search: TSV 25 молекул, 3 шарда → completed за ~10s → результат **байт-в-байт идентичен** локальному эталону (`scimesh similarity-search`).
- Найденные и исправленные в ходе E2E баги (все выпущены):
  - alpha.12: agent.Version не ставился до ветки setup → 409 при install;
  - alpha.13: визард не подставлял venv python в task_runner (падало `python not found`) — теперь гарантируется на сервере (save + start);
  - alpha.14: preflight /api/test проверял системный python3 вместо venv — теперь проверяет venv;
  - задокументированы X11-библиотеки RDKit для headless (libxrender1 и др.).
- ✅ Финальный гейт: go test -race ./... зелёный, golangci-lint 0 issues, pytest 208 passed.
- ✅ Контейнеры удалены.

## Completion
COMPLETED — пайплайн «install как человек → coordinator serve → worker-agent setup → воркер регистрируется → реальный джоб завершается с байт-в-байт корректным результатом» доведён до рабочего состояния и проверен на релизных артефактах v1.1.0-alpha.14.
