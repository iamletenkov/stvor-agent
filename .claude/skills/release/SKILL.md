---
name: release
description: Как выходит версия агента — release-please, сборка .deb к релизу и автоподъём пина в cream-extensions; что проверить перед слиянием релизного PR.
---
# /release

Теги, `VERSION` и `CHANGELOG.md` руками не трогают.

1. PR сливается squash-ем; заголовок PR = заголовок коммита в `main` (`feat(…)` → minor, `fix(…)` → patch, `!` → major; текст по-русски).
2. На push в `main` после зелёного job `Check` job **Release Please** (`.github/workflows/ci-release.yml`, конфиг — `.github/release_please/`, `release-type: simple`, версия в `VERSION`) ведёт PR «chore(main): release X.Y.Z».
3. Слияние релизного PR → тег `vX.Y.Z` и опубликованный релиз → `release-artifacts` прикладывает `bisquite-teleport-agent_X.Y.Z_all.deb` и `SHA256SUMS` (сборка воспроизводимая) → `dispatch-extensions` запускает `agent-bump.yml` в cream-extensions (секрет `EXTENSIONS_DISPATCH_TOKEN`, PAT с ограниченным сроком — см. README).
4. Упала сборка файлов — «Re-run failed jobs» или ручной запуск workflow с `release_tag`, не откат.
5. Перед слиянием релизного PR: `gh pr checks <N>` зелёный, `gh run list -L 3` — main зелёный, changelog без мусора.

Имя пакета `bisquite-teleport-agent` меняется только по спеке переписывания на Go.
