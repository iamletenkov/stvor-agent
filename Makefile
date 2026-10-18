# stvor-agent — публичный API репозитория. Go на хосте не нужен: всё в контейнере.
# Кэш модулей, сборки и линтера — в .cache/ (gitignored), бинари и .deb — в dist/.
#
# Пока в репозитории живёт bash-агент 3.x (до его удаления по плану), его проверки
# и сборка пакета — цели legacy-*, и make ci гоняет Go и 3.x вместе.

GO_IMAGE       ?= golang:1.25.11
GOLANGCI_IMAGE ?= golangci/golangci-lint:v2.14.0
MODULE         := github.com/iamletenkov/stvor-agent
VERSION        ?= $(shell tr -d '[:space:]' < VERSION 2>/dev/null || echo dev)
ARCHES         ?= amd64 arm64
# Коммит сборки для `version` (без флага); вне git — пусто, бинарь печатает «unknown».
COMMIT         ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
LDFLAGS        := -s -w -buildid= -X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT)

UID := $(shell id -u)
GID := $(shell id -g)

# -buildvcs=false: в git worktree .git — файл-указатель наружу, VCS-штамп внутри
# контейнера не собирается; версия всё равно приходит через ldflags.
DOCKER_RUN = docker run --rm -u $(UID):$(GID) \
	-v "$(CURDIR):/src" -w /src \
	-v "$(CURDIR)/.cache:/cache" \
	-e HOME=/tmp -e GOCACHE=/cache/go-build -e GOMODCACHE=/cache/mod \
	-e GOLANGCI_LINT_CACHE=/cache/golangci -e GOFLAGS=-buildvcs=false
GO = $(DOCKER_RUN) $(GO_IMAGE)

.DEFAULT_GOAL := help
.PHONY: help cache build build-all test lint fmt legacy-check legacy-deb ci clean

help: ## список целей
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-13s %s\n", $$1, $$2}'

cache:
	@mkdir -p .cache dist

build: cache ## dist/stvor-agent под архитектуру хоста (статический, CGO_ENABLED=0)
	$(GO) env CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/stvor-agent ./cmd/stvor-agent

# После сборки amd64-бинарь запускается в контейнере: version --short обязан
# совпасть с VERSION — так проверяется, что ldflags дошли до бинаря.
build-all: cache ## dist/stvor-agent-linux-{amd64,arm64} (статические, -trimpath) и сверка версии
	@set -e; for arch in $(ARCHES); do \
		echo "== linux/$$arch"; \
		$(GO) env CGO_ENABLED=0 GOOS=linux GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" -o dist/stvor-agent-linux-$$arch ./cmd/stvor-agent; \
	done
	@got="$$($(GO) dist/stvor-agent-linux-amd64 version --short)"; \
	if [ "$$got" != "$(VERSION)" ]; then echo "version --short = '$$got', ожидалось '$(VERSION)'"; exit 1; fi; \
	echo "== version --short: $$got"; \
	echo "== version: $$($(GO) dist/stvor-agent-linux-amd64 version)"

# -race требует cgo: тесты собираются с CGO_ENABLED=1 (gcc есть в образе golang),
# релизный бинарь — без cgo.
test: cache ## go test -race ./...
	$(GO) env CGO_ENABLED=1 go test -race -count=1 ./...

fmt: cache ## gofmt -w по всему модулю
	$(GO) gofmt -w .

# Карта docs/test-map.md сверяется со списком Go-тестов (go test -list) —
# tools/check-test-map.sh: имена из карты существуют, пустых строк нет.
lint: cache ## gofmt -l, go vet, golangci-lint, сверка docs/test-map.md с Go-тестами
	@out="$$($(GO) gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt: не отформатированы (make fmt):"; echo "$$out"; exit 1; fi
	$(GO) go vet ./...
	$(DOCKER_RUN) $(GOLANGCI_IMAGE) golangci-lint run ./...
	$(GO) go test -list . ./... > .cache/go-test-list.txt
	tools/check-test-map.sh .cache/go-test-list.txt

legacy-check: ## 3.x: shellcheck и tests/test-*.sh (tools/check.sh)
	tools/check.sh

legacy-deb: ## 3.x: dist/bisquite-teleport-agent_<версия>_all.deb и dist/SHA256SUMS
	tools/build-deb.sh

ci: lint test build-all legacy-check legacy-deb ## всё, что гоняет CI: Go (lint, test, build-all) и проверки 3.x

clean: ## удалить dist/ (кэш .cache/ сохраняется)
	rm -rf dist
