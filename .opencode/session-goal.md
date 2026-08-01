COMPLETED
# Session Goal

адаптируй код под sdk, где это необходимо. при надобности доработай SDK. главное чтобы workloads можно было дописывать не трогая остальной код программы, так как он в будущем будет закрытым. Workloads - в первую очередь пользовательские скрипты, поэтому sdk должен полностью покрывать необходимый функционал.

## Plan

1. SDK: добавить высокоуровневый каркас MapReduceWorkload (scimesh/sdk/batch.py) — манифест/стейджи/definition собираются автоматически, планировщик/раннер/редуктор — общий скелет с хуками (partition_input, compute_shard, parse/validate_partial_keys, reduce_partials, domain_validate).
2. Рефакторинг: descriptor-batch, similarity-search, similarity-graph переписать на базовый класс (поведение/байты не меняются — парность покрыта тестами).
3. Worker (закрываемый код): обобщить SciMeshRunner — загрузка ворклоадов из конфига/дискавери (allowlist), инвентарь из конфига воркера, fail-closed для неподдерживаемых форм; конфиг: SCIMESH_CAPABILITIES, SCIMESH_WORKLOAD_ALLOWLIST.
4. CLI: добавить общий `scimesh workload list|run` (generic SDK-инструмент, без workload-специфичной логики) — пользовательские скрипты можно запускать локально без правки остального кода.
5. Тесты: test_sdk_batch.py (каркас + хуки + fail-closed), тесты воркера на не-search ворклоаде, CLI-тесты; регрессия парности.
6. Документация: workload-sdk.md (авторский гайд на базе MapReduceWorkload), handoff, STATUS.
7. Полный прогон pytest, финальная верификация.

## Progress

- [x] `scimesh/sdk/batch.py`: `MapReduceWorkload` — identity/parameters/ports + 3 научных хука; сборка манифеста, map/reduce стейджей, workflow, pinned handlers, exact-artifact verifier; хуки: domain_validate, resolved_parameters, resolved_parameters_for_plan, plan_tasks, parse/validate_partial_keys, map_stage_inputs; экспортирован из scimesh.sdk.
- [x] descriptor-batch, similarity-search, similarity-graph переписаны на MapReduceWorkload; парность с локальными reference сохранена (тесты byte-identical зелёные). `query_id`-резолюция переехала в run_search_shard (ворклоад сам валидирует параметры).
- [x] Worker обобщён: SciMeshRunner принимает definitions+inventory+runtime, `for_worker(config)` грузит ворклоады через allowlist-дискавери (entry points) или built-in fallback; fail-closed для map-стейджей не по v1-контракту (single input); параметры таски проходят насквозь, валидация в ворклоаде. Конфиг: SCIMESH_CAPABILITIES, SCIMESH_WORKLOAD_ALLOWLIST (JSON {distribution,name,version,digest}); парсер вынесен в SDK (`workload_allowlist_from_json`).
- [x] CLI: `scimesh workload list|run` (generic; SCIMESH_WORKLOAD_ALLOWLIST поддерживается; runtime строится из discovered-ворклоадов); зарегистрирован как ворклоад-модуль.
- [x] `default_sdk_registry(allowlist=...)` и `default_sdk_runtime(workload_capabilities=..., environment_digests=...)` в library.
- [x] Тесты: test_sdk_batch.py (5), test_cli_workload.py (6, включая end-to-end allowlisted custom workload), worker: generic execution (descriptor-batch), v1-contract rejection (graph), for_worker discovery, config parsing.
- [x] Документация: workload-sdk.md (раздел "Authoring a workload" + worker/CLI), handoff, STATUS, README.
- [x] Финальная верификация: 249 passed; scimesh workload list/run работают; scimesh.sdk не импортирует workloads (grep чист).
- Изменения НЕ закоммичены (по AGENTS.md коммит только по явной просьбе).
