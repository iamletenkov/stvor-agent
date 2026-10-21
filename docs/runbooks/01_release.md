# Выпуск версии

Кто: владелец репозитория (слияние релизного PR — только с его «да»). Теги руками не ставят,
`CHANGELOG.md` руками не правят.

Версию и `CHANGELOG.md` ведёт [release-please](https://github.com/googleapis/release-please)
(`.github/release_please/`, workflow `CI and Release`). В `main` уходят коммиты по Conventional Commits:
`fix:` поднимает patch, `feat:` — minor, `!` или `BREAKING CHANGE:` — major; `docs:`, `ci:`, `chore:`,
`build:` версию не трогают. После проверок на `main` release-please открывает (и дальше обновляет) PR
«chore(main): release X.Y.Z». Релиз затем:

1. собирает и прикладывает к релизу оба `.deb` и `SHA256SUMS` (job `release-artifacts`);
2. поднимает пин обёртки в cream-extensions (job `dispatch-extensions`) — §3;
3. поднимает пин на портале Stvor (job `dispatch-stvor`) — §4.

## 1. Релизный PR

```sh
gh run list -R iamletenkov/stvor-agent -b main -L 3      # main зелёный
gh pr list -R iamletenkov/stvor-agent --search 'chore(main): release'
gh pr checks <N> -R iamletenkov/stvor-agent              # проверки PR зелёные
gh pr view <N> -R iamletenkov/stvor-agent --json files   # только CHANGELOG.md и манифест версии
```

Номер версии считает release-please по коммитам с прошлого релиза: `fix:` — patch, `feat:` — minor,
`!`/`BREAKING CHANGE:` — major. Слияние (squash) создаёт тег `vX.Y.Z` и релиз GitHub.

## 2. Файлы релиза (.deb и SHA256SUMS)

Job `release-artifacts` workflow `CI and Release` собирает из тега `make deb VERSION=X.Y.Z`, сверяет
`stvor-agent version --short` с тегом и прикладывает три файла.

```sh
gh run watch -R iamletenkov/stvor-agent "$(gh run list -R iamletenkov/stvor-agent -b main -L 1 --json databaseId --jq '.[0].databaseId')"
gh release view vX.Y.Z -R iamletenkov/stvor-agent --json assets --jq '.assets[].name'
# ожидается: SHA256SUMS, stvor-agent_X.Y.Z_amd64.deb, stvor-agent_X.Y.Z_arm64.deb
d="$(mktemp -d)" && gh release download vX.Y.Z -R iamletenkov/stvor-agent -D "$d" && (cd "$d" && sha256sum -c SHA256SUMS)
```

Сборка упала — не откатывать релиз, а перезапустить: «Re-run failed jobs» в том же прогоне или
`gh workflow run ci-release.yml -R iamletenkov/stvor-agent --ref main -f tag=vX.Y.Z -f upload=true`
(без `upload=true` — сухой прогон сборки, файлы не загружаются).

## 3. Пин в cream-extensions

Обёртка `stvor-agent` каталога ставит пакет по пину `extensions/debian/stvor-agent/files/pin.env`
(версия и sha256 по архитектурам). Job `dispatch-extensions` запускает в cream-extensions `agent-bump.yml`,
только если у репозитория переменная `EXTENSIONS_BUMP_ENABLED=true`; иначе он пишет предупреждение и
пропускает шаг. Тогда подъём запускают руками:

```sh
gh variable list -R iamletenkov/stvor-agent                                  # есть ли EXTENSIONS_BUMP_ENABLED
gh workflow run agent-bump.yml -R iamletenkov/cream-extensions -f tag=vX.Y.Z  # PR с новым пином
# запасной путь — локально в чистой ветке cream-extensions:
#   gh release download vX.Y.Z -R iamletenkov/stvor-agent -p SHA256SUMS -D <каталог>
#   tools/agent-bump.sh vX.Y.Z <каталог>/SHA256SUMS && tools/check.sh
```

Проверка: PR в cream-extensions меняет `files/pin.env` (версия и обе суммы равны `SHA256SUMS` релиза)
и патч-версию обёртки в `extension.yaml`; после его слияния и релиза каталога
`git show origin/main:extensions/debian/stvor-agent/files/pin.env` показывает новую версию.

## 4. Портал Stvor

Портал раздаёт роботам `.deb` агента и снимок каталога со своего зеркала по собственным пинам
(`STVOR_AGENT_*` в `.env.example` Stvor). Job `dispatch-stvor` запускает в `iamletenkov/stvor`
`robot-pins-bump.yml` (`component=stvor-agent`): тот ставит версию и обе суммы из `SHA256SUMS` релиза
и открывает PR. Работает при переменной `STVOR_BUMP_ENABLED=true` и секрете `STVOR_DISPATCH_TOKEN`
(fine-grained PAT на stvor, Actions: Read and write); без секрета — предупреждение, релиз зелёный.
Руками: `gh workflow run robot-pins-bump.yml -R iamletenkov/stvor -f component=stvor-agent -f tag=vX.Y.Z`.
Выкладку на экземпляр делают в Stvor по его runbook 10 (§10.1); вся цепочка — его runbook
«цепочка выпусков cream».

Проверка на роботе после установки или обновления: `stvor-agent version --short` = `X.Y.Z`, через ≤ 5 минут
метка узла `stvor_agent=X.Y.Z` ([диагностика](../guides/diagnostics.md)).
