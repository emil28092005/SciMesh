COMPLETED
# Session Goal

пропиши абсолютно полную документацию scimesh в mkdocs.

## Plan

1. Каталог `mkdocs/` — самостоятельный источник сайта (docs_dir: mkdocs); проектные `docs/` не трогаем.
2. mkdocs.yml: docs_dir, полный nav (Home → SDK guides → API reference (все модули scimesh.sdk) → Documentation approach), edit_uri.
3. Контент:
   - index.md — лендинг: что такое SciMesh, архитектура, быстрый старт.
   - sdk/overview.md — концепция SDK (framework-only, core-batch-v1, безопасность, fail-closed).
   - sdk/authoring-workloads.md — полный гайд MapReduceWorkload (атрибуты, хуки, полный пример molwt-filter, регистрация, тесты).
   - sdk/cli.md — scimesh workload list/run/export + env.
   - sdk/worker-integration.md — воркер: claim/heartbeat/upload, SDK-мост, allowlist, ограничения v1.
   - api/ — автогенерация mkdocstrings для ВСЕХ модулей scimesh.sdk (batch, artifacts, manifest, workflow, plans, registry, verification, runtime, resources, conformance, execution, identity, integrity, schema, protocols) + index.md.
   - approach.md — зафиксированные правила написания документации.
4. Убрать тестовые страницы mkdocs из docs/ (docs/index.md, docs/api/).
5. make docs — проверить рендер всех страниц; итерации по ошибкам mkdocstrings.
6. pytest + pyright — зелёные; /ui/docs/ интеграция не ломается; README/STATUS обновить.

## Progress

- [x] mkdocs.yml: `docs_dir: mkdocs`, полный nav, edit_uri; тема Material + mkdocstrings (handlers.python.options, show_if_no_docstring: true).
- [x] index.md — лендинг (быстрый старт, карта сайта).
- [x] sdk/overview.md, sdk/authoring-workloads.md (полный гайд + molwt-filter пример + plan_tasks блоки), sdk/cli.md, sdk/worker-integration.md.
- [x] api/: 15 страниц `::: scimesh.sdk.<module>` + api/index.md (модульная карта) — ВСЕ модули SDK.
- [x] approach.md — правила: типы страниц, reference только из docstrings (Google style), терминология, код-блоки, ссылки, когда обновлять, сборка.
- [x] Удалены тестовые страницы mkdocs из docs/ (docs/index.md, docs/api/).
- [x] Устранена проблема mkdocstrings-python 2.x: `default_options` → `handlers.python.options`; `show_if_no_docstring: true` (иначе члены без docstrings не рендерились).
- [x] Добавлены Google-style docstrings ВСЕМ публичным членам scimesh.sdk без них (~50: artifacts, manifest, plans, workflow, execution, identity, protocols, registry, resources, runtime, verification, schema, conformance); починено подавление pyright в schema.py (перенесённый форматтером `# type: ignore`).
- [x] make docs --strict: 0 ошибок/предупреждений; все 16 API-страниц + 4 гайда + approach рендерятся (проверено по содержимому HTML).
- [x] pytest 256 passed; pyright scimesh/tests 0 errors.
- [x] README/STATUS/handoff обновлены (mkdocs/ сайт, /ui/docs/).
- Изменения НЕ закоммичены (коммит по запросу).
