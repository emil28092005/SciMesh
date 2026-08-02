COMPLETED
# Session Goal

давай теперь почистим проект от линего кода

## Plan

1. Аудит: ruff/pyflakes — неиспользуемые импорты по scimesh/ и tests/; grep — неиспользуемые функции/модули (после рефакторингов могли остаться мёртвые экспорты, например в descriptors/search/graph core и sdk/_validation).
2. Удаление мёртвого кода: неиспользуемые импорты, функции, дубли (например write_descriptor_shards/concatenate_descriptor_shards, если вытеснены дефолтами batch), устаревшие файлы-обёртки.
3. Проверка, что ничего публичного/API не сломано: pyright 0 ошибок, pytest зелёный, go test/vet, mkdocs build.
4. Финал: полный прогон, COMPLETED.

## Progress

- [x] Аудит: ruff F401/F811/F841 — 26 неиспользуемых импортов; vulture — кандидаты проверены grep'ом.
- [x] Удалено:
  - 25 неиспользуемых импортов (ruff --fix) + 1 неиспользуемая локальная переменная в тесте;
  - мёртвые функции descriptors/core.py `write_descriptor_shards`/`concatenate_descriptor_shards` (вытеснены дефолтами MapReduceWorkload; ссылки только в собственном `__init__`) + их экспорты;
  - мёртвые атрибуты `MapReduceWorkload._resources/_execution` (записывались, нигде не читались);
  - мёртвый `CancellationFlag.cancel` (0 использований);
- [x] `scripts/two-worker-smoke.sh` (рабочий E2E, но без точки входа) подключён как `make smoke-two-worker` — не мёртвый, а доступный.
- [x] Проверено: ruff clean; pytest 260 passed; pyright 0 ошибок (scimesh+tests); go test 11 пакетов + vet; mkdocs build без warnings.
- [x] Изменения не закоммичены.
