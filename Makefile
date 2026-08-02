.DEFAULT_GOAL := help

.PHONY: help agent coordinator demo-ui demo-down demo-logs smoke-two-worker docs docs-serve

help:
	@printf '%s\n' \
	  'SciMesh developer commands:' \
	  '  make agent                   Build the Go worker agent (coordinator/bin/worker-agent).' \
	  '  make coordinator             Build the coordinator server as a static binary' \
	  '                              (coordinator/bin/coordinator).' \
	  '  make demo-ui                 Start the local UI pipeline demo with 2 Go worker agents.' \
	  '  make demo-ui WORKERS=3       Start the demo with 3 workers.' \
	  '  make demo-logs               Follow coordinator logs for the demo.' \
	  '  make demo-down               Stop demo containers and workers.' \
	  '  make smoke-two-worker        E2E: two Go agents process 4 shards and the' \
	  '                              result must match the local CLI reference.' \
	  '  make docs                    Build the MkDocs site into site/.' \
	  '  make docs-serve              Serve the MkDocs site at http://localhost:8000.' \
	  '' \
	  'After make demo-ui: open http://localhost:18080/ui (operator / demo-ui-secret).'

# Convenient entry points from the repository root. Extra settings are passed
# through, for example: make demo-ui WORKERS=3
agent:
	$(MAKE) -C coordinator agent

coordinator:
	$(MAKE) -C coordinator coordinator

demo-ui:
	$(MAKE) -C coordinator demo-ui

demo-down:
	$(MAKE) -C coordinator demo-down

demo-logs:
	$(MAKE) -C coordinator demo-logs

smoke-two-worker:
	./scripts/two-worker-smoke.sh

docs:
	.venv/bin/mkdocs build

docs-serve:
	.venv/bin/mkdocs serve
