.PHONY: demo-ui demo-down demo-logs

# Convenient entry points from the repository root. Extra settings are passed
# through, for example: make demo-ui WORKERS=3
demo-ui:
	$(MAKE) -C coordinator demo-ui

demo-down:
	$(MAKE) -C coordinator demo-down

demo-logs:
	$(MAKE) -C coordinator demo-logs
