# stvor-agent — публичный API репозитория. Go на хосте не нужен: всё в контейнере.
# Кэш модулей, сборки и линтера — в .cache/ (gitignored), бинари и .deb — в dist/.
#
# Версия — из VERSION=<v> (CI берёт её из тега релиза); без неё — 0.0.0-dev: версия
# .deb обязана начинаться с цифры, а бинарь и пакет несут одну и ту же строку.

GO_IMAGE       ?= golang:1.26.9
GOLANGCI_IMAGE ?= golangci/golangci-lint:v2.14.0
NFPM_IMAGE     ?= goreleaser/nfpm:v2.47.0
DEBIAN_IMAGE   ?= debian:13
SHELLCHECK_IMAGE ?= koalaman/shellcheck:v0.11.0
ACTIONLINT_IMAGE ?= rhysd/actionlint:1.7.12@sha256:b1934ee5f1c509618f2508e6eb47ee0d3520686341fec936f3b79331f9315667
GOVULNCHECK    ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0
MODULE         := github.com/iamletenkov/stvor-agent
VERSION        ?= 0.0.0-dev
ARCHES         ?= amd64 arm64
# Коммит сборки для `version` (без флага); вне git — пусто, бинарь печатает «unknown».
COMMIT         ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
LDFLAGS        := -s -w -buildid= -X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT)

# Пакет (спека §9.1, §9.2): версия .deb = VERSION; mtime в пакете — время коммита.
DEB_VERSION    ?= $(VERSION)
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)
# ARCH пусто — оба .deb; RELEASE_DIR — куда кладутся .deb и SHA256SUMS.
ARCH           ?=
DEB_ARCHES      = $(or $(ARCH),$(ARCHES))
RELEASE_DIR    ?= dist
# Живой dpkg -i/-r/--purge в debian (tools/check-deb-live.sh); repro-check — без него.
DEB_LIVE       ?= 1
# Подкаталог .cache/ под кеш сборки Go: repro-check собирает в разных пустых кешах.
GOCACHE_DIR    ?= go-build
# Замок тяжёлых прогонов — общий с cream: на хосте одна тяжёлая сборка за раз.
# Цель под замком (STVOR_AGENT_HEAVY_HELD) зовёт другую такую же без взаимной блокировки.
HEAVY_LOCK     ?= /tmp/cream-heavy.lock
HEAVY           = $(if $(STVOR_AGENT_HEAVY_HELD),,flock $(HEAVY_LOCK) env STVOR_AGENT_HEAVY_HELD=1)
# Сопровождающие скрипты пакета и инструменты упаковки — под shellcheck в make lint.
SH_FILES       := packaging/postinst packaging/prerm packaging/postrm \
	tools/check-deb.sh tools/check-deb-live.sh tools/repro-check.sh \
	tools/check-test-map.sh tools/check-contract-fixtures.sh \
	test/integration/cream-deb.sh test/integration/run.sh test/integration/inside.sh \
	test/e2e/run.sh test/e2e/inside.sh test/e2e/teleport-fake/teleport

UID := $(shell id -u)
GID := $(shell id -g)

# -buildvcs=false: в git worktree .git — файл-указатель наружу, VCS-штамп внутри
# контейнера не собирается; версия всё равно приходит через ldflags.
DOCKER_RUN = docker run --rm -u $(UID):$(GID) \
	-v "$(CURDIR):/src" -w /src \
	-v "$(CURDIR)/.cache:/cache" \
	-e HOME=/tmp -e GOCACHE=/cache/$(GOCACHE_DIR) -e GOMODCACHE=/cache/mod \
	-e GOLANGCI_LINT_CACHE=/cache/golangci -e GOFLAGS=-buildvcs=false \
	-e SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH)
GO = $(DOCKER_RUN) $(GO_IMAGE)
# Go-файлы модуля без кэшей (.cache/: модули, govulncheck) и dist/: gofmt по «.» зашёл бы в зависимости.
GO_FILES = find . \( -path ./.cache -o -path ./dist -o -path ./.scratchpad -o -path ./.git \) -prune -o -name '*.go' -print

.DEFAULT_GOAL := help
.PHONY: help cache build build-all test test-integration e2e e2e-unlocked lint actionlint contract-fixtures fmt man deb \
	deb-unlocked repro-check vuln ci ci-unlocked clean

help: ## список целей
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-17s %s\n", $$1, $$2}'

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

# Адаптер internal/teleport против настоящего cream (план, Task 15): пакет cream по пину
# test/integration/cream-pin.env (или CREAM_DEB=<путь> с предупреждением) ставится в
# контейнер debian:12 вместе с фиктивным расширением teleport. Пин пуст и CREAM_DEB не
# задан — пропуск со строкой, не ошибка. В ci не входит до Task 20.
test-integration: cache ## адаптер teleport против настоящего cream в контейнере debian:12
	@deb="$$(test/integration/cream-deb.sh)" || exit 1; [ -n "$$deb" ] || exit 0; \
	mkdir -p .cache/it; \
	$(GO) env CGO_ENABLED=0 go test -c -tags integration -o .cache/it/teleport.test ./internal/teleport && \
	test/integration/run.sh "$$deb" .cache/it/teleport.test

# Сквозной тест (план Task 20, спека §15.3): контейнер debian:12 с systemd, cream по пину
# test/integration/cream-pin.env, снимок каталога cream-extensions по пину
# test/e2e/catalog-pin.env (CREAM_EXTENSIONS_DIR, по умолчанию ../cream-extensions),
# teleport с поддельным агентом, .deb stvor-agent из dist/ (версия E2E_VERSION, только amd64)
# против test/e2e/fakeportal. Под замком: один тяжёлый прогон за раз. Лог —
# .scratchpad/test-runs/e2e-<время>/. В ci не входит — отдельный job CI.
E2E_VERSION    ?= 4.0.0
e2e: ## сквозной тест в контейнере debian:12 с systemd (под замком)
	$(HEAVY) $(MAKE) --no-print-directory e2e-unlocked

e2e-unlocked: cache
	@deb="$$(test/integration/cream-deb.sh)" || exit 1; \
	if [ -z "$$deb" ]; then echo "e2e: пин cream пуст — сквозной тест без cream невозможен"; exit 1; fi; \
	$(MAKE) --no-print-directory deb-unlocked VERSION=$(E2E_VERSION) ARCH=amd64 ARCHES=amd64 \
		DEB_LIVE=0 RELEASE_DIR=.cache/e2e && \
	$(GO) env CGO_ENABLED=0 go build -trimpath -o .cache/e2e/fakeportal ./test/e2e/fakeportal && \
	test/e2e/run.sh "$$deb" .cache/e2e/stvor-agent_$(E2E_VERSION)_amd64.deb $(E2E_VERSION) \
		.cache/e2e/fakeportal .scratchpad/test-runs/e2e-$$(date +%Y%m%d-%H%M%S)

fmt: cache ## gofmt -w по всему модулю
	$(GO) sh -c "$(GO_FILES) | xargs -r gofmt -w"

# Карта docs/test-map.md сверяется со списком Go-тестов (go test -list) —
# tools/check-test-map.sh: имена из карты существуют, пустых строк нет.
lint: cache actionlint contract-fixtures ## gofmt -l, go vet, golangci-lint, docs/test-map.md, shellcheck, actionlint, образцы контракта
	@out="$$($(GO) sh -c "$(GO_FILES) | xargs -r gofmt -l")"; if [ -n "$$out" ]; then echo "gofmt: не отформатированы (make fmt):"; echo "$$out"; exit 1; fi
	$(GO) go vet ./...
	$(GO) go vet -tags integration ./internal/teleport
	$(DOCKER_RUN) $(GOLANGCI_IMAGE) golangci-lint run ./...
	$(GO) go test -list . ./... > .cache/go-test-list.txt
	tools/check-test-map.sh .cache/go-test-list.txt
	docker run --rm -v "$(CURDIR):/mnt" -w /mnt $(SHELLCHECK_IMAGE) -x -S style $(SH_FILES)

actionlint: ## actionlint по .github/workflows (образ закреплён по digest, как в CI)
	docker run --rm -v "$(CURDIR):/repo" -w /repo $(ACTIONLINT_IMAGE) -no-color

# Файлы-образцы контракта с расширением teleport (план, Task 19), без сети: набор
# internal/teleport/testdata/*.json равен пину tools/contract-fixtures.sha256 и копии в
# соседнем cream-extensions (CREAM_EXTENSIONS_DIR, по умолчанию ../cream-extensions).
contract-fixtures: ## сверка файлов-образцов контракта с пином и с cream-extensions
	tools/check-contract-fixtures.sh

# Оба .deb (или один — ARCH=amd64|arm64) из готовых dist/stvor-agent-linux-* и
# man-страницы $(RELEASE_DIR)/man/stvor-agent.8.gz (tools/genman): nfpm,
# затем в debian — состав и поля (tools/check-deb.sh) и живые dpkg -i/-r/--purge
# от root (tools/check-deb-live.sh, корень репозитория — только чтение).
deb: ## dist/stvor-agent_<v>_<arch>.deb (ARCH=amd64|arm64, по умолчанию оба) и SHA256SUMS (под замком)
	$(HEAVY) $(MAKE) --no-print-directory deb-unlocked

deb-unlocked: build-all
	@mkdir -p $(RELEASE_DIR)
	$(GO) go run ./tools/genman -out $(RELEASE_DIR)/man -version $(DEB_VERSION)
	@set -e; for a in $(DEB_ARCHES); do \
		echo "== nfpm stvor-agent_$(DEB_VERSION)_$$a.deb"; \
		$(DOCKER_RUN) -e ARCH=$$a -e DEB_VERSION=$(DEB_VERSION) -e MAN_DIR=$(RELEASE_DIR)/man $(NFPM_IMAGE) \
			package -f packaging/nfpm.yaml -p deb -t $(RELEASE_DIR)/stvor-agent_$(DEB_VERSION)_$$a.deb; \
		$(DOCKER_RUN) $(DEBIAN_IMAGE) sh tools/check-deb.sh $(DEB_VERSION) $$a $(RELEASE_DIR); \
		if [ "$(DEB_LIVE)" = 1 ]; then \
			docker run --rm -v "$(CURDIR):/src:ro" -w /src $(DEBIAN_IMAGE) \
				sh tools/check-deb-live.sh $(DEB_VERSION) $$a $(RELEASE_DIR); \
		fi; \
	done
	cd $(RELEASE_DIR) && sha256sum -- $(foreach a,$(DEB_ARCHES),stvor-agent_$(DEB_VERSION)_$(a).deb) > SHA256SUMS
	@cat $(RELEASE_DIR)/SHA256SUMS

repro-check: ## две сборки обоих .deb с одним SOURCE_DATE_EPOCH и пустыми кешами — sha256 совпадают (под замком)
	$(HEAVY) env MAKE="$(MAKE)" sh tools/repro-check.sh

# go run …@версия собирает govulncheck вне модуля: go.mod/go.sum не меняются,
# модули инструмента — в отдельном .cache/vuln-mod.
# Страницу рендерит internal/cli из той же таблицы команд, что --help (tools/genman).
man: cache ## man-страница stvor-agent.8 (и .gz) в dist/man; смотреть: man -l dist/man/stvor-agent.8
	$(GO) go run ./tools/genman -out dist/man -version $(DEB_VERSION) -plain

vuln: cache ## govulncheck по модулю (версия закреплена в GOVULNCHECK)
	$(DOCKER_RUN) -e GOMODCACHE=/cache/vuln-mod -e GOFLAGS="-buildvcs=false -mod=mod" \
		$(GO_IMAGE) go run $(GOVULNCHECK) ./...

ci: ## всё, что гоняет CI: lint, test, build-all, deb, repro-check (под замком)
	$(HEAVY) $(MAKE) --no-print-directory ci-unlocked

ci-unlocked: lint test deb-unlocked repro-check

clean: ## удалить dist/ (кэш .cache/ сохраняется)
	rm -rf dist
