# Разработка

Нужны `docker` и `make`; Go на хосте не нужен — всё идёт в контейнере `golang:1.26.10`.

```bash
make help              # все цели
make build             # dist/stvor-agent под архитектуру хоста
make lint              # gofmt, go vet, golangci-lint, docs/test-map.md, shellcheck, actionlint, образцы контракта
make test              # go test -race ./...
make test-integration  # адаптер teleport против настоящего cream в debian:12 (пин test/integration/cream-pin.env)
make e2e               # сквозной тест в debian:12 с systemd: cream, каталог по пину test/e2e/catalog-pin.env, поддельный портал
                       # + телеметрия: токен от run → служба stvor-telemetry → пачка у портала; exposure юнита ≤ test/e2e/telemetry-security.env
                       # (systemd 252; другие версии systemd e2e не покрывает — см. «Бюджет телеметрии на машине»)
make e2e-openwrt       # корневая ФС OpenWrt 25.12 (openwrt-builder v0.3.1) в docker: установка из tar.gz,
                       # procd-скрипты через rc.common с поддельным ubus, поддельный cream (teleport-openwrt), телеметрия
make tarball           # dist/stvor-agent_<версия>_linux_{amd64,arm64}.tar.gz для OpenWrt (бинарь, init.d/, man, лицензии)
make deb VERSION=4.0.1 # dist/stvor-agent_<версия>_{amd64,arm64}.deb, оба tar.gz и общий dist/SHA256SUMS (ARCH=amd64 — один)
make repro-check       # две сборки .deb и tar.gz с пустыми кешами — равные sha256
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

## Бюджет телеметрии на машине

Бюджет службы телеметрии (спека Stvor §4: < 1 % CPU ядра, < 30 МБ RSS на Jetson Nano) меряется на живой машине
без установки пакета: `test/bench/telemetry` — тот же цикл `stvor-agent telemetry`, но с файлами в одном каталоге
(`-dir`; `-cpuprofile`, `-heapprofile` — профили для `go tool pprof`), портал — `test/e2e/fakeportal` (файл
`detail-until` в его каталоге включает режим 5 с). Порядок, команды и итоги замера на Nano (systemd 245 и 237) —
[research/2026-10-22-telemetry-budget-jetson.md](research/2026-10-22-telemetry-budget-jetson.md). Сжатие тела
пачки — `BenchmarkGzipper` в `internal/telemetry`. Образец `/proc` и `/sys` новой машины для
`internal/collect/testdata` снимает `tools/collect-fixture.sh <каталог>` (на машине, от обычного пользователя).

## Как вносить изменения

- Ветка от свежего `origin/main`, в `main` — через PR со squash.
- Коммиты — [Conventional Commits](https://www.conventionalcommits.org/ru/): тип и область латиницей,
  текст по-русски. Версию и `CHANGELOG.md` ведёт release-please; выпуск — [runbook](runbooks/01_release.md).
- Тест пишется до кода. Перед PR — зелёный `make ci`.
- Контракт с машинами (имена пакета, бинаря, юнита, пути, CLI, `status --json`) и с порталом меняется
  только через ADR.

Регламент целиком — [AGENTS.md](../AGENTS.md).
