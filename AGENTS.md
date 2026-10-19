# stvor-agent — регламент для агентов и людей

**stvor-agent** (бывший bisquite-teleport-agent) — служба на машине (робот, стенд, сервер), представитель портала Stvor. Сейчас — bash: CLI `bisquite-teleport`, юниты systemd, схема настроек, пакет `bisquite-teleport-agent_<версия>_all.deb`; умеет регистрацию в Teleport, метки с портала, `apps.d`. Лицензия PolyForm Noncommercial (не менять без решения владельца).

**Курс:** переписать на Go (один статический бинарь, linux/amd64 и linux/arm64); регистрация в Teleport уезжает в расширение `teleport`, агент остаётся представителем портала. До спеки имена пакета, бинаря, юнитов и пути (`bisquite-teleport`, `/etc/bisquite/teleport`, `/var/lib/...`) **не меняются** — это контракт с установленными машинами. Состояние — [docs/STATUS.md](docs/STATUS.md).

## Место в экосистеме
- **Stvor** — приватный портал (identity & access hub); агент ходит в его robot-API ([docs/portal-labels-api.md](docs/portal-labels-api.md)).
- **cream-extensions** — каталог расширений; расширение `teleport-agent` ставит этот пакет по пину версии и sha256 (пин поднимается автоматически после релиза).
- **cream** — исполнитель расширений (Go); **bisquite** — собирает образы с расширениями.

## Язык
Общение, документация, README — **по-русски**. Код, комментарии в коде, имена файлов и веток — **по-английски**. Коммиты и заголовки PR — Conventional Commits: тип и скоуп латиницей, текст **по-русски** (`fix(join): повтор не стирает секрет до успеха`). Заголовок ≤ 100 символов, строка тела ≤ 100. Заголовок PR = заголовок squash-коммита.

## Карта репозитория
```
src/          — bisquite-teleport (CLI), knobs, pkg-setup, systemd/ (юниты), lib/
lib/          — bisquite-conf (копия библиотеки настроек из cream-extensions)
debian/       — control.in, postinst/prerm/postrm
tests/        — test-*.sh на подменённом корне (без root, systemd и сети)
tools/        — check.sh (shellcheck + тесты), build-deb.sh (воспроизводимый .deb)
docs/         — README.md (индекс), STATUS.md, portal-labels-api.md, adr/, specs/, plans/, research/
.claude/      — агенты, skills, правила, хуки Claude Code
Makefile      — публичный API (`make help`): lint, test, check, deb, ci
```

## Как работать
1. **Классифицируй задачу** вслух: *тривиальная* → правка → `make lint` → коммит; *bounded* (1–3 файла) → короткий дизайн → тест первым → reviewer → PR; *архитектурная* (переписывание на Go, контракт с порталом, пути и имена на машине, вынос Teleport в расширение) → спека → skeptic → план с `## Progress Tracker` → TDD → reviewer → PR. Сомневаешься — путь тяжелее.
2. **Спека до плана, план до кода;** трудно откатываемое — ADR. Новый документ — строка в `docs/README.md`.
3. **TDD:** тест (`tests/test-*.sh`, позже — Go table-driven) пишется до кода.
4. **Docker-first:** Go — только в контейнере `golang:1.26.9` через `make`. Хост общий: один `make ci` за раз.

## Definition of Done
Подтверждено **выводом**: `make ci` (shellcheck, тесты, сборка `.deb`) зелёный; README/`docs/` обновлены; чекбокс плана отмечен в том же коммите.

## Git и выпуск
- Ветки `feature/`, `fix/`, `chore/`, `docs/`, `ci/` от свежего `origin/main`; в `main` — PR со squash.
- Версию (`VERSION`) и `CHANGELOG.md` ведёт **release-please**; релиз прикладывает `.deb` и `SHA256SUMS` и запускает подъём пина в cream-extensions. Теги руками не ставят — skill `/release`.
- `git add <пути>`, никогда `git add -A`. Хуки lefthook (`lefthook install`) не обходить: `--no-verify` запрещён.

## Гигиена и секреты
- Временное — только `.scratchpad/` (gitignored); сборка — `dist/`.
- Токены и секреты регистрации — только в файлах 0600 на машине, окружении и секретах GitHub; не в аргументах процессов, логах, коде, доках, коммитах. `.mcp.json` — из `.mcp.json.example`, ключи из env.
