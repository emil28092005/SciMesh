COMPLETED
# Session Goal

давай теперь почистим проект от линего кода

## Plan

1. Аудит: ruff/pyflakes — неиспользуемые импорты по scimesh/ и tests/; grep — неиспользуемые функции/модули (после рефакторингов могли остаться мёртвые экспорты, например в descriptors/search/graph core и sdk/_validation).
2. Удаление мёртвого кода: неиспользуемые импорты, функции, дубли (например write_descriptor_shards/concatenate_descriptor_shards, если вытеснены дефолтами batch), устаревшие файлы-обёртки.
3. Проверка, что ничего публичного/API не сломано: pyright 0 ошибок, pytest зелёный, go test/vet, mkdocs build.
4. Финал: полный прогон, COMPLETED.

## Progress

Эта сессия (доп. задача): Go-агент доведён до паритета, Python-демон удалён.
- [x] Go-агент: token provider (static + worker-key exchange + 401 refresh на API/download/upload), CLEANUP_AFTER_SECONDS (очистка attempt-директорий), тесты auth (exchange/cache/reject/select/401-retry).
- [x] Python: удалены daemon.py, cli.py, config.py, coordinator.py, artifacts.py, auth.py, transport.py; `scimesh/worker/` = только task.py + runners.py (SDK-мост, allowlist из env) + models.py (ClaimedTask/RunResult); консольный скрипт scimesh-worker убран из pyproject.
- [x] Тесты: удалены test_worker_daemon.py, test_worker_auth.py; 208 pytest зелёные.
- [x] Демо/смок переведены на Go-агент (demo-ui.sh: build_agent + env; two-worker-smoke.sh: AGENT_BIN + TASK_RUNNER_JSON). `make smoke-two-worker` PASS: 4/4 шардов, worker-a=2, worker-b=2.
- [x] Документация: README, AGENTS.md, STATUS, handoff, mkdocs worker-integration/cli.
- [x] Верификация: ruff clean; 208 pytest; pyright 0 (scimesh+tests); go test 11 пакетов + vet; mkdocs 0 warnings.
