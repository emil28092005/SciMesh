.DEFAULT_GOAL := help

.PHONY: help demo-ui demo-down demo-logs

help:
	@printf '%s\n' \
	  'SciMesh developer commands:' \
	  '  make demo-ui                 Start the local UI pipeline demo with 2 workers.' \
	  '  make demo-ui WORKERS=3       Start the demo with 3 local workers.' \
	  '  make demo-logs               Follow coordinator logs for the demo.' \
	  '  make demo-down               Stop demo containers and workers.' \
	  '' \
	  'After make demo-ui: open http://localhost:18080/ui (operator / demo-ui-secret).'

# Convenient entry points from the repository root. Extra settings are passed
# through, for example: make demo-ui WORKERS=3
demo-ui:
	$(MAKE) -C coordinator demo-ui

demo-down:
	$(MAKE) -C coordinator demo-down

demo-logs:
	$(MAKE) -C coordinator demo-logs
