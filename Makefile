.PHONY: help check test lint deb ci

help:  ## список целей
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

check:  ## shellcheck и все тесты
	tools/check.sh

test:  ## только тесты tests/test-*.sh
	@rc=0; for t in tests/test-*.sh; do echo "== $$t"; bash "$$t" || rc=1; done; exit $$rc

lint:  ## только shellcheck
	tools/check.sh --lint-only

deb:  ## dist/bisquite-teleport-agent_<версия>_all.deb и dist/SHA256SUMS
	tools/build-deb.sh

ci: check deb  ## то же, что CI: shellcheck, тесты и сборка .deb
