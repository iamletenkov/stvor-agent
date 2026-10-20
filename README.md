# stvor-agent

Агент портала Stvor на машине (робот, стенд, сервер): долгоживущая служба systemd, которая приводит
машину к состоянию, объявленному порталом. В версии 4.x это метки узла Teleport `env` и `space`: служба
раз в минуту забирает их с [`GET /robot-api/v1/labels`](docs/portal-labels-api.md), отдаёт расширению
`teleport` каталога cream (`cream run teleport set-managed`), дожидается подтверждённого перезапуска
агента Teleport и сообщает порталу применённую ревизию (`X-Applied-Revision`).

- Один статический Go-бинарь `/usr/bin/stvor-agent` (только стандартная библиотека), пакеты
  `stvor-agent_<версия>_amd64.deb` и `_arm64.deb`. Входящих портов не открывает.
- Регистрацией в Teleport, `teleport.yaml`, приложениями и `os_logins` занимается расширение
  [`teleport`](https://github.com/iamletenkov/cream-extensions/blob/main/extensions/debian/teleport/README.md);
  агент о них не знает. Ставит пакет обёртка каталога
  [`stvor-agent`](https://github.com/iamletenkov/cream-extensions/blob/main/extensions/debian/stvor-agent/README.md).
- Машины: Debian ≥ 12 и Ubuntu ≥ 20.04, `amd64` и `arm64` (юниту нужен systemd ≥ 245).
- Признак и версию агента портал видит по динамической метке узла `stvor_agent=<версия>`: Teleport раз в
  5 минут исполняет `stvor-agent version --label`. Обновление агента Teleport не перезапускает.
- Зависит от [cream](https://github.com/iamletenkov/cream) `>= 1.0.0` и расширения `teleport`
  `>=4.0.0 <5` (обёртка каталога требует `teleport >=4.0.2`: с этой версии метку `stvor_agent`
  применяет само расширение).

Устройство и решения — [спека](docs/specs/2026-10-18-stvor-agent-go.md), безопасность —
[SECURITY.md](SECURITY.md), изменения — [CHANGELOG.md](CHANGELOG.md), вся документация —
[docs/README.md](docs/README.md).

## Runbook'и

| Runbook | Когда |
|---|---|
| [01 — выпуск версии](docs/runbooks/01_release.md) | release-please, `.deb` и `SHA256SUMS` к релизу, подъём пина в cream-extensions и на портале |
| [02 — диагностика на роботе](docs/runbooks/02_robot-diagnostics.md) | `status --json`, журнал службы, файл токена, метка `stvor_agent` в Teleport |
| [03 — перевыпуск токена и переезд робота](docs/runbooks/03_token-reissue-and-move.md) | токен утёк или отозван, смена домена портала, замена машины, вывод из работы |

## Установка

### Через cream (обычный путь)

Так ставит машину скрипт портала Stvor; секреты идут только через stdin:

```sh
cream source add stvor --archive https://<домен>/dist/teleport/cream-extensions-<v>.tar.gz \
    --sha256 <пин> --replace --sync
cream install teleport TELEPORT_VERSION=18.10.0 TELEPORT_SHA256=<пин arch> \
    TELEPORT_MIRROR=https://<домен>/dist/teleport
printf '%s' "$SECRET" | cream run teleport join TELEPORT_PROXY=<прокси> \
    TELEPORT_JOIN_METHOD=bound_keypair TELEPORT_TOKEN=<имя токена> TELEPORT_REGISTRATION_SECRET=- \
    TELEPORT_ENV=<env> TELEPORT_NODENAME=<имя> TELEPORT_LABELS=space=<slug>
cream install stvor-agent STVOR_AGENT_VERSION=<v> STVOR_AGENT_SHA256=<пин arch> \
    STVOR_AGENT_MIRROR=https://<домен>/dist/teleport
printf '%s' "$TOKEN" | stvor-agent enable URL=https://<домен>/robot-api/v1/labels
```

- У `join` один секрет — `TELEPORT_REGISTRATION_SECRET=-` со stdin: для метода `token` это сам
  join-токен, для `bound_keypair` — секрет регистрации; `TELEPORT_TOKEN` — несекретное имя токена.
- `cream install stvor-agent` сверяет sha256 `.deb` архитектуры машины и ставит пакет; юнит включает
  пакет, но служба не стартует, пока нет `/etc/stvor-agent/token`. Токена обёртка не принимает. Без
  явных версии и суммы берётся пин каталога.
- `stvor-agent enable` читает токен меток со stdin, пишет `/etc/stvor-agent/token`, включает и
  запускает службу и сразу делает один синхронный `sync`.
- Офлайн: `--archive file:///<каталог>/cream-extensions-<v>.tar.gz`, `TELEPORT_TARBALL=…`,
  `STVOR_AGENT_DEB=…`. В образе bisquite расширения уже стоят, на первой загрузке остаются `join` и
  `stvor-agent enable`.

### Без обёртки каталога

Пакет можно поставить и голым `.deb` из релиза GitHub, если cream и расширение `teleport` на машине уже
есть (без них служба работает, но применять метки некуда):

```sh
v=4.0.0; arch="$(dpkg --print-architecture)"
curl -fLO "https://github.com/iamletenkov/stvor-agent/releases/download/v$v/stvor-agent_${v}_$arch.deb"
curl -fLO "https://github.com/iamletenkov/stvor-agent/releases/download/v$v/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS
sudo apt install "./stvor-agent_${v}_$arch.deb"
```

Так не появится метка `stvor_agent`: её drop-in кладёт обёртка каталога. Настройки службы без cream —
умолчания (файл `/etc/cream/stvor-agent/config` необязателен).

`postinst` включает юнит только при первой установке (обновление не отменяет `disable` оператора) и
перезапускает службу при обновлении, если агент включён. Без токена служба не стартует
(`ConditionPathExists=/etc/stvor-agent/token`).

## Команды

Аргументы — `KEY=VALUE` (повтор ключа — отказ); все команды, кроме `version`, `help` и `status`, требуют
root.

| Команда | Что делает | Коды выхода |
|---|---|---|
| `stvor-agent run` | цикл службы (`ExecStart` юнита) | 0 — штатная остановка |
| `stvor-agent enable URL=<адрес>` | токен со stdin → `/etc/stvor-agent/token` (атомарно, 0600); другой адрес — сброс `state.json`; `systemctl enable` и `restart` службы; затем синхронный `sync` | 0; 1 — ввод, запись или `systemctl`; 2 — использование. Отказ сети при первом `sync` — 0 со строкой: служба догонит |
| `stvor-agent disable` | `systemctl disable --now`, удалить токен и `state.json`. Teleport не вызывается: управляемые `env`/`space` остаются, метка `stvor_agent` пустеет за ≤ 5 мин | 0; 1 — что-то не удалось |
| `stvor-agent sync` | один тик сейчас, без паузы 3×/15 мин | 0 — метки действуют, узел ещё не зарегистрирован или ошибка сети/HTTP (служба повторит); 1 — ответ не по контракту, перезапуск не подтверждён, токен битый, агент не включён |
| `stvor-agent apply env=<v> space=<v>` | ручной запасной путь: те же значения в `teleport` без портала и без паузы; `state.json` не меняет — следующий тик вернёт решение портала | 0; 1 — `set-managed` не выполнен или не подтверждён; 2 — ввод вне алфавита `[a-z0-9][a-z0-9-]{0,62}` |
| `stvor-agent status [--json]` | токен (только класс: `ok`, `absent`, `bad_mode`, `bad_owner`, `no_access`, `corrupt`), `state.json`, `managed-status` расширения; в сеть не ходит | 0 — включён; 1 — не включён или запуск не от root (`enabled: null`) |
| `stvor-agent version [--short\|--label]` | версия, коммит и сборка; `--short` — `X.Y.Z`; `--label` — `X.Y.Z`, если есть файл токена, иначе пустая строка (команда метки `stvor_agent`; файл не читает — только `stat`) | 0 (`--label` — всегда) |
| `stvor-agent help` | справка | 0 |

Неизвестная команда или лишний аргумент — код 2. CLI-команды ждут блокировку агента
(`/run/stvor-agent.lock`) до 120 с; служба берёт её без ожидания и при занятой пропускает тик.

Включение с клавиатуры, чтобы токен не попал в историю оболочки:

```sh
read -rs T && printf '%s' "$T" | sudo stvor-agent enable URL=https://<портал>/robot-api/v1/labels; unset T
```

## Как работает служба

- Первый тик — через случайные 0–20 с после старта, дальше сон `интервал + U[0, 20 с)`; интервал —
  `STVOR_AGENT_INTERVAL` (60 по умолчанию, 60–600 с).
- Неудача (сеть, TLS, тайм-аут, 3xx, 429, 5xx, тело не по контракту) — backoff
  `min(интервал · 2^min(n,3), 600 с)`; успех (200/304) сбрасывает счётчик; 401 — сон 600 с (ускоряет
  только `enable`). Раньше своего тика служба не повторяет.
- Сверяется **состояние**, а не ревизия: на каждом тике метки в `teleport` сравниваются с последним
  ответом портала; расхождение (pending, неактивный агент, чужой перезапуск) исправляется.
- После трёх неподтверждённых перезапусков подряд при активном агенте Teleport перезапуск — не чаще раза
  в 15 минут. `sync` и `apply` паузой не ограничены.
- `SIGHUP` — тик сейчас (`systemctl kill -s HUP stvor-agent`); `SIGTERM` — дождаться конца текущего тика
  (`TimeoutStopSec=120`) и выйти.

## Настройки

Домен cream `stvor-agent`, файл `/etc/cream/stvor-agent/config` (служба читает его как
`EnvironmentFile`); правка — `sudo cream set stvor-agent KEY=…`, хук `apply` перезапускает службу.

| Ключ | Умолчание | Что |
|---|---|---|
| `STVOR_AGENT_INTERVAL` | `60` | период опроса портала, секунды, 60–600 (меньше не даёт лимит портала: 6 запросов в минуту на токен) |
| `STVOR_AGENT_LOG_LEVEL` | `info` | `info` или `debug` (тайминги и строка на каждый тик без изменений) |

Значение вне границ — строка журнала и умолчание.

### Прокси

По умолчанию у службы нет переменных прокси. Если портал доступен только через прокси — drop-in юнита:

```sh
sudo systemctl edit stvor-agent.service
# [Service]
# Environment=HTTPS_PROXY=http://proxy.example.org:3128
# Environment=NO_PROXY=localhost,127.0.0.1
sudo systemctl restart stvor-agent.service
journalctl -u stvor-agent -n 20     # следующий опрос: event=poll outcome=ok|not_modified
```

Дочерним процессам (`cream`) прокси не передаётся: их окружение — только `PATH` и `LANG`.

## Файлы

| Путь | Права | Что |
|---|---|---|
| `/usr/bin/stvor-agent` | 0755 | бинарь |
| `/usr/lib/systemd/system/stvor-agent.service` | 0644 | юнит: `Type=exec`, `Restart=always`, `KillMode=mixed`, hardening ([SECURITY.md](SECURITY.md)) |
| `/etc/stvor-agent/token` | 0600 root, каталог 0700 | `url=<адрес>` и `token=<токен>`; пишет только `enable`. Другие права или владелец — отказ |
| `/var/lib/stvor-agent/state.json` | 0600, каталог 0700 (`StateDirectory=`) | ожидаемые метки и ревизия, применённая ревизия, попытки перезапуска, последний опрос. Руками правленый файл не принимается — служба начинает с пустого состояния |
| `/run/stvor-agent.lock` | — | одна операция агента за раз |
| `/etc/cream/stvor-agent/config` | 0644 | настройки (домен cream), необязателен |

Чужие файлы, которые агент сам не пишет: `/etc/cream/teleport/config` (ручку `TELEPORT_MANAGED_LABELS`
пишет `set-managed` расширения), `/etc/cream/teleport/command-labels.d/stvor_agent` (drop-in метки,
владелец — обёртка каталога).

`apt remove` выключает и останавливает службу; `apt purge` удаляет `/etc/stvor-agent` и
`/var/lib/stvor-agent`. Метки Teleport и регистрация узла при этом остаются — их снимает
`cream run teleport leave`.

## Журнал

Одна строка на событие в journald (`SyslogIdentifier=stvor-agent`): сообщение по-русски, ключи латиницей
(`event=poll outcome=not_modified status=304`, `event=apply result=unconfirmed`,
`event=skip reason=paused`). Токена, тел ответов и тел ошибок портала в журнале нет.

```sh
journalctl -u stvor-agent -f
```

## Диагностика

```sh
sudo stvor-agent status            # таблица; --json — схема 1 для скриптов
sudo stvor-agent sync              # тик сейчас, итог одной строкой и кодом
journalctl -u stvor-agent -n 50
cream run teleport status          # регистрация, ручки, управляемые и командные метки
cream run teleport managed-status  # то, что видит агент (JSON)
```

| Признак | Что значит |
|---|---|
| `токен: absent`, служба `inactive` | агент не включён — `stvor-agent enable` |
| `токен: bad_mode` / `bad_owner` / `corrupt` | файл токена не 0600 root или испорчен — `enable` заново; токен не отправляется |
| `outcome=unauthorized` | портал отозвал токен — выпустить новый в карточке робота и `enable` |
| `outcome=rate_limited`, `network`, `http_error` | ошибка портала или сети — служба повторит с backoff |
| `outcome=bad_body` | ответ не по [контракту](docs/portal-labels-api.md) — метки не тронуты |
| `result=deferred` | узел не зарегистрирован в Teleport — метки применятся после `join` |
| `result=unconfirmed`, `reason=paused` | агент Teleport не подтвердил перезапуск; после трёх раз — пауза 15 мин (`sync` её обходит) |
| «в расширении teleport нет действий managed-status/set-managed» | стоит `teleport` старше 4.0.0 — обновить расширение |

Пошагово — [runbook 02](docs/runbooks/02_robot-diagnostics.md); токен и переезд —
[runbook 03](docs/runbooks/03_token-reissue-and-move.md).

## Отличия от 3.x

4.x — новая реализация, а не обновление: переходного пакета и миграции нет.

| `bisquite-teleport-agent` 3.x (bash) | `stvor-agent` 4.x (Go) |
|---|---|
| пакет `bisquite-teleport-agent_<v>_all.deb`, CLI `bisquite-teleport` | `stvor-agent_<v>_<arch>.deb`, `/usr/bin/stvor-agent` |
| регистрация, `teleport.yaml`, `apps.d`, `os_logins`, `leave` | расширение `teleport` каталога cream (`cream run teleport …`) |
| `labels-enable`, таймер `bisquite-teleport-labels.timer`, `pull` | `stvor-agent enable`, служба `stvor-agent.service`, `sync` |
| метки портала — правка `TELEPORT_LABELS`/`TELEPORT_ENV` | управляемые метки `TELEPORT_MANAGED_LABELS` через `set-managed` |
| `/etc/bisquite/teleport/labels-token` | `/etc/stvor-agent/token` (тот же формат) |
| метки `managed_by=bisquite-teleport-agent`, `bisquite_agent_version` | динамическая метка `stvor_agent` |

Контракт `/robot-api/v1/labels` не изменился. `bisquite-teleport-agent` 3.1.x поддерживается, пока
портал Stvor не перейдёт на 4.x (он пинит 3.1.x со своего зеркала); 3.1.1 — исправление безопасности
(см. [SECURITY.md](SECURITY.md#311-версия-кластера-в-арифметике-bash)). Bash-код 3.x из `main` удалён
(задача 19 [плана](docs/plans/2026-10-18-stvor-agent-go.md)); он живёт в тегах `v3.*`.

## Разработка

Go на хосте не нужен: всё идёт в контейнере `golang:1.26.9` через `make` (`make help` — список целей).

```bash
make lint              # gofmt, go vet, golangci-lint, docs/test-map.md, shellcheck, actionlint, образцы контракта
make test              # go test -race ./...
make test-integration  # адаптер teleport против настоящего cream в debian:12 (пин test/integration/cream-pin.env)
make e2e               # сквозной тест в debian:12 с systemd (каталог cream-extensions по пину test/e2e/catalog-pin.env)
make ci                # всё, что гоняет CI (под общим замком тяжёлых прогонов)
make deb VERSION=4.0.0 # dist/stvor-agent_<версия>_{amd64,arm64}.deb и dist/SHA256SUMS (ARCH=amd64 — один)
make repro-check       # две сборки .deb с пустыми кешами — равные sha256
make vuln              # govulncheck по модулю
```

Пакет 4.x собирает nfpm (`goreleaser/nfpm:v2.47.0`, `packaging/nfpm.yaml`): `make repro-check`
показал побайтную воспроизводимость (mtime — `SOURCE_DATE_EPOCH`, время коммита), поэтому возврат
к `dpkg-deb` не нужен. `make deb` проверяет состав и поля в `debian:13` (`tools/check-deb.sh`) и
ставит, удаляет и вычищает пакет живым `dpkg` с поддельным `systemctl` (`tools/check-deb-live.sh`).

Тесты Go — table-driven; внешнее (файлы, `systemctl`, `cream`, портал, часы) подменяется интерфейсами:
портал — `httptest.NewTLSServer`, `cream` — поддельный процесс-помощник. Перенос проверок
`tests/test-labels.sh` 3.x в Go — [docs/test-map.md](docs/test-map.md) (итоговая). Файлы-образцы контракта с
расширением `teleport` (`internal/teleport/testdata/*.json`) сверяет `make contract-fixtures` (входит в `make lint`):
набор равен пину `tools/contract-fixtures.sha256` и копии в соседнем `cream-extensions` (`CREAM_EXTENSIONS_DIR`,
по умолчанию `../cream-extensions`; без копии локально — пропуск с предупреждением, в CI — ошибка).

### Как выпустить версию

Версию и `CHANGELOG.md` ведёт [release-please](https://github.com/googleapis/release-please)
(`.github/release_please/`), workflow `CI and Release`:

1. Сливаешь в `main` коммиты по [Conventional Commits](https://www.conventionalcommits.org/ru/):
   `fix:` поднимает patch, `feat:` — minor, `!` или `BREAKING CHANGE:` — major;
   `docs:`, `ci:`, `chore:` версию не трогают. Тип и область — латиницей, текст заголовка и тела —
   на русском; подробнее — [AGENTS.md](AGENTS.md).
2. После проверок на `main` release-please открывает (и дальше обновляет) PR «chore(main): release
   X.Y.Z» с секцией `CHANGELOG.md`.
3. Владелец сливает этот PR. release-please создаёт тег `vX.Y.Z` и релиз, затем job
   `release-artifacts` собирает из тега `stvor-agent_<v>_amd64.deb`, `stvor-agent_<v>_arm64.deb` и
   `SHA256SUMS` и прикладывает их к релизу.
4. Job `dispatch-extensions` запускает в `iamletenkov/cream-extensions` workflow `agent-bump.yml`: тот
   ставит версию и обе суммы в пин обёртки `stvor-agent` и открывает PR. Работает при переменной
   репозитория `EXTENSIONS_BUMP_ENABLED=true` (иначе — предупреждение и подъём руками). Токен — секрет
   `EXTENSIONS_DISPATCH_TOKEN` этого репозитория (fine-grained PAT на cream-extensions, Actions: Read
   and write); истёк — job релиза покраснеет.
5. Job `dispatch-stvor` запускает в `iamletenkov/stvor` workflow `robot-pins-bump.yml` — PR с
   `STVOR_AGENT_*` в `.env.example` портала. Выключатель — `STVOR_BUMP_ENABLED=true`, токен —
   `STVOR_DISPATCH_TOKEN` (PAT на stvor, Actions: Read and write); без секрета — предупреждение.
   Включить/выключить любой подъём: `gh variable set <ИМЯ> --body true` / `gh variable delete <ИМЯ>`.
   Пошагово с проверками — [runbook 01](docs/runbooks/01_release.md).

Теги руками не ставят. Если сборка файлов упала, релиз уже виден без них: «Re-run failed jobs» в том же
прогоне или ручной запуск с `tag` (без `upload=true` — сухой прогон):

```bash
gh workflow run ci-release.yml --ref main -f tag=vX.Y.Z -f upload=true
```

## Лицензия

[PolyForm Noncommercial 1.0.0](LICENSE), см. [NOTICE](NOTICE). Коммерческое
использование — по отдельной лицензии правообладателя.
