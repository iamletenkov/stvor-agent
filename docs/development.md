# Разработка

Нужны `docker` и `make`; Go на хосте не нужен — всё идёт в контейнере `golang:1.26.10`.

```bash
make help              # все цели
make build             # dist/stvor-agent под архитектуру хоста
make lint              # gofmt, go vet, golangci-lint, docs/test-map.md, shellcheck, actionlint, образцы контракта
make test              # go test -race ./...
make test-integration  # адаптер teleport против настоящего cream в debian:12 (пин test/integration/cream-pin.env)
make e2e               # сквозной тест в debian:12 с systemd: cream, каталог по пину test/e2e/catalog-pin.env, поддельный портал
make deb VERSION=4.0.1 # dist/stvor-agent_<версия>_{amd64,arm64}.deb и dist/SHA256SUMS (ARCH=amd64 — один)
make repro-check       # две сборки .deb с пустыми кешами — равные sha256
make vuln              # govulncheck
make man               # dist/man/stvor-agent.8; смотреть: man -l dist/man/stvor-agent.8
make ci                # всё, что гоняет CI: lint, test, build-all, deb, repro-check
```

## Тесты

- Go-тесты — table-driven; портал — `httptest.NewTLSServer`, `cream` — поддельный процесс-помощник.
- Файлы-образцы контракта с расширением `teleport` (`internal/teleport/testdata/*.json`) сверяет
  `make contract-fixtures` (входит в `make lint`): набор равен пину `tools/contract-fixtures.sha256` и
  копии в соседнем `cream-extensions` (`CREAM_EXTENSIONS_DIR`, по умолчанию `../cream-extensions`; без
  копии локально — пропуск с предупреждением, в CI — ошибка).
- `make deb` проверяет состав и поля пакета (`tools/check-deb.sh`) и ставит, удаляет и вычищает его живым
  `dpkg` с поддельным `systemctl` (`tools/check-deb-live.sh`).
- [test-map.md](test-map.md) — карта переноса проверок bash-агента 3.x в Go; её сверяет `make lint`.

## Как вносить изменения

- Ветка от свежего `origin/main`, в `main` — через PR со squash.
- Коммиты — [Conventional Commits](https://www.conventionalcommits.org/ru/): тип и область латиницей,
  текст по-русски. Версию и `CHANGELOG.md` ведёт release-please; выпуск — [runbook](runbooks/01_release.md).
- Тест пишется до кода. Перед PR — зелёный `make ci`.
- Контракт с машинами (имена пакета, бинаря, юнита, пути, CLI, `status --json`) и с порталом меняется
  только через ADR.

Регламент целиком — [AGENTS.md](../AGENTS.md).
