# stvor-agent — регламент для агентов и людей

**stvor-agent** (бывший bisquite-teleport-agent) — служба портала Stvor на машине (робот, стенд, сервер): Go-бинарь
`/usr/bin/stvor-agent`, юнит `stvor-agent.service`, пакеты `stvor-agent_<версия>_{amd64,arm64}.deb`. Забирает с портала
метки `env`/`space` и применяет их через расширение `teleport` каталога cream. Лицензия PolyForm Noncommercial (не
менять без решения владельца). Что умеет и как ставится — [README.md](README.md); состояние — [docs/STATUS.md](docs/STATUS.md).

**Курс выполнен:** агент переписан на Go (спека [docs/specs/2026-10-18-stvor-agent-go.md](docs/specs/2026-10-18-stvor-agent-go.md),
план [docs/plans/2026-10-18-stvor-agent-go.md](docs/plans/2026-10-18-stvor-agent-go.md)), регистрация в Teleport уехала в
расширение `teleport`. Контракт с машинами 4.x — имена пакета, бинаря, юнита, пути `/etc/stvor-agent`,
`/var/lib/stvor-agent`, CLI и `status --json` — меняется только через ADR. Bash-агент 3.x удалён из дерева (Task 19
плана); его код — в тегах `v3.*`, исправления 3.1.x выпускаются из своей ветки, не из `main`.

## Место в экосистеме
- **Stvor** — приватный портал (identity & access hub); агент ходит в его robot-API ([docs/portal-labels-api.md](docs/portal-labels-api.md)).
- **cream** — исполнитель расширений (Go); агент зовёт `cream run teleport managed-status|set-managed`.
- **cream-extensions** — каталог: расширение `teleport` (регистрация, `teleport.yaml`, управляемые метки) и обёртка
  `stvor-agent`, которая ставит этот пакет по пину версии и sha256 (пин поднимает `agent-bump` после релиза).
- **bisquite** — собирает образы с расширениями.

## Язык
Общение, документация, README — **по-русски**. Код, комментарии в коде, имена файлов и веток — **по-английски**. Коммиты и заголовки PR — Conventional Commits: тип и скоуп латиницей, текст **по-русски** (`fix(portal): 304 без запомненных меток не сбрасывает состояние`). Заголовок ≤ 100 символов, строка тела ≤ 100. Заголовок PR = заголовок squash-коммита.

## Карта репозитория
```
cmd/stvor-agent/   — точка входа: os.Exit(cli.Run(...))
internal/cli/      — подкоманды, KEY=VALUE, коды выхода, status
internal/portal/   — клиент /robot-api/v1/labels: запрос, строгий разбор тела, классификация ответа
internal/reconcile/— один тик: сверка ожидаемого с фактическим, политика перезапусков
internal/daemon/   — цикл службы, джиттер, backoff, сигналы, блокировка, настройки из окружения
internal/teleport/ — адаптер `cream run teleport …`, разбор JSON; testdata/ — файлы-образцы контракта с CE
internal/token/, internal/state/, internal/atomicfile/ — файл токена, state.json, атомарная запись и чтение без ссылок
internal/systemd/  — вызовы systemctl; internal/version/ — версия из ldflags
packaging/         — юнит stvor-agent.service (+ unit_test.go), nfpm.yaml, postinst/prerm/postrm, copyright
test/integration/  — адаптер против настоящего cream в debian:12 (`make test-integration`)
test/e2e/          — сквозной тест в debian:12 с systemd: cream, каталог по пину, fakeportal (`make e2e`)
tools/             — check-test-map, check-contract-fixtures (+ пин contract-fixtures.sha256), check-deb, check-deb-live, repro-check
docs/              — README.md (индекс), STATUS.md, portal-labels-api.md, test-map.md, runbooks/, adr/, specs/, plans/, research/
.claude/           — агенты, skills, правила, хуки Claude Code
Makefile           — публичный API (`make help`): lint, actionlint, contract-fixtures, test, build-all, deb, repro-check, vuln, test-integration, e2e, ci
```

## Как работать
1. **Классифицируй задачу** вслух: *тривиальная* → правка → `make lint` → коммит; *bounded* (1–3 файла) → короткий дизайн → тест первым → reviewer → PR; *архитектурная* (контракт с порталом или с `teleport`, пути и имена на машине, формат `state.json`/`status --json`, упаковка) → спека → skeptic → план с `## Progress Tracker` → TDD → reviewer → PR. Сомневаешься — путь тяжелее.
2. **Спека до плана, план до кода;** трудно откатываемое — ADR. Новый документ — строка в `docs/README.md`.
3. **TDD:** table-driven Go-тест пишется до кода; внешнее (файлы, `systemctl`, `cream`, портал, часы) — через интерфейсы.
4. **Docker-first:** Go — только в контейнере `golang:1.26.10` через `make`. Хост общий: один `make ci` за раз (замок тяжёлых прогонов общий с cream).

## Definition of Done
Подтверждено **выводом**: `make ci` (lint, test, build-all, deb, repro-check) зелёный; README/`docs/` обновлены; чекбокс плана отмечен в том же коммите.

## Git и выпуск
- Ветки `feature/`, `fix/`, `chore/`, `docs/`, `ci/` от свежего `origin/main`; в `main` — PR со squash.
- Версию и `CHANGELOG.md` ведёт **release-please** (`release-type: go`, тег `vX.Y.Z`); релиз прикладывает оба `.deb` и `SHA256SUMS` и запускает подъём пина в cream-extensions. Теги руками не ставят — skill `/release`, [runbook 01](docs/runbooks/01_release.md).
- `git add <пути>`, никогда `git add -A`. Хуки lefthook (`lefthook install`) не обходить: `--no-verify` запрещён.

## Гигиена и секреты
- Временное — только `.scratchpad/` (gitignored); сборка — `dist/`, кеши — `.cache/`.
- Токены и секреты регистрации — только в файлах 0600 на машине, окружении и секретах GitHub; не в аргументах процессов, логах, коде, доках, коммитах. `.mcp.json` — из `.mcp.json.example`, ключи из env.
