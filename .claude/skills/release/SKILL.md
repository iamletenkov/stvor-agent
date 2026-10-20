---
name: release
description: Как выходит версия агента 4.x — release-please, сборка .deb amd64/arm64 к релизу и подъём пина в cream-extensions; что проверить перед слиянием релизного PR.
---
# /release

Теги и `CHANGELOG.md` руками не трогают. Версия 4.x в бинаре и пакете — из тега через `-ldflags`, CI сверяет.

1. PR сливается squash-ем; заголовок PR = заголовок коммита в `main` (`feat(…)` → minor, `fix(…)` → patch, `!` → major; текст по-русски).
2. На push в `main` после зелёных job'ов `check` (`make ci`), `vuln`, `integration` job **Release Please** (`.github/workflows/ci-release.yml`, конфиг — `.github/release_please/`, `release-type: go`, `package-name: stvor-agent`) ведёт PR «chore(main): release X.Y.Z».
3. Первый Go-выпуск — **4.0.0**: его задаёт `"release-as": "4.0.0"` в конфиге (без него по коммитам `feat` вышло бы 3.2.0). Сразу после выхода 4.0.0 — PR, убирающий `release-as`: job `check` краснеет, пока тег `v<release-as>` существует, а release-please с ним предлагает тот же номер снова. Манифест — версия последнего релиза **из `main`** (3.1.0): 3.1.1 вышел тегом с ветки `fix/3.1.1-version-check` вне `main`, release-please его не ищет.
4. Слияние релизного PR → тег `vX.Y.Z` и опубликованный релиз → `release-artifacts` собирает из тега `make deb VERSION=X.Y.Z` (время коммита тега), сверяет `stvor-agent version --short` с тегом и `sha256sum -c SHA256SUMS`, прикладывает `stvor-agent_X.Y.Z_amd64.deb`, `stvor-agent_X.Y.Z_arm64.deb`, `SHA256SUMS`.
5. `dispatch-extensions` запускает `agent-bump.yml` в cream-extensions (секрет `EXTENSIONS_DISPATCH_TOKEN`) **только при переменной репозитория `EXTENSIONS_BUMP_ENABLED=true`**; иначе — предупреждение и пропуск. Перенацеленный `agent-bump` уже в `main` cream-extensions; пока переменная не включена — `gh workflow run agent-bump.yml -R iamletenkov/cream-extensions -f tag=vX.Y.Z` или `tools/agent-bump.sh` ([runbook 01](../../../docs/runbooks/01_release.md) §3).
6. Упала сборка файлов — «Re-run failed jobs» или ручной запуск workflow с `tag=vX.Y.Z` и `upload=true`, не откат. Без `upload` — сухой прогон сборки. Теги 3.x этим workflow не пересобираются.
7. Перед слиянием релизного PR: `gh pr checks <N>` зелёный, `gh run list -L 3` — main зелёный, changelog без мусора. Публикация релиза — только с прямого «да» владельца.
