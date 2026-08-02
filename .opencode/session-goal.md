# Session Goal

выполни полностью намеченный сейчас план. автономно

## Plan
1. Фаза 1 — SQLite-хранилище: `coordinator/internal/storage/sqlite` (все порты, TxManager, миграции, `SCIMESH_DB=sqlite|postgres`, тесты, CI-джоба без postgres).
2. Фаза 2 — Встроенный userservice: перенос `users/internal/*` в `coordinator/internal/userservice/` (sqlite-хранилище), запуск на 127.0.0.1 внутреннем порту, BootstrapAdmin на первом запуске.
3. Фаза 3 — `coordinator serve` (data-dir, всё-в-одном, --workers N, --open) + subcommand `coordinator agent` (переиспользование internal/agent).
4. Фаза 4 — Управляемая установка Python-рантайма (venv в data-dir, кнопка/автопроверка, TASK_RUNNER в venv) + install.sh/install.ps1 + ассеты релиза.
5. Документация: mkdocs quick-start, README, PLAN.md (CTX-17 done, новый CTX-18), STATUS.md.
6. Проверка: полный E2E «чистая машина» (`coordinator serve --workers 2` без postgres), все тесты/lint/vet зелёные.

## Progress
- ВСЕ ФАЗЫ ГОТОВЫ. Фаза 1 (9883def), Фаза 2-3 (1473bbe), Фаза 4-5 (215325a).
- Фаза 2 (userservice) ГОТОВА: перенос users/internal в coordinator/internal/userservice (auth/domain/usecase/transport/memstore + sqlite storage с тестами), embedded.go (Serve на 127.0.0.1:18081, BootstrapAdmin, миграции).
- Фаза 3 (serve+agent) ГОТОВА (1473bbe): `coordinator serve --data-dir ~/.scimesh --workers N --open` (секреты 0600, admin генерируется, venv-бустрап, embedded userservice, spawn локальных агентов через `coordinator agent`), `coordinator agent` subcommand (DefaultCapabilities). E2E полный: serve → login (userservice) → molwt-filter джоб через агента → результат byte-точный. golangci 0 issues.
- Осталось: Фаза 4 (install.sh/install.ps1 + релиз-ассеты), Фаза 5 (docs: mkdocs/README/PLAN/STATUS), финальная верификация.
