# Инвентаризация: раздел агента bisquite-teleport на расширение `teleport` (cream) и `stvor-agent`

Дата: 2026-10-18. Источники (только чтение): `/home/debian/projects/stvor-agent` (HEAD `0aeb84e`, VERSION 3.1.0),
`/home/debian/projects/cream-extensions` (HEAD `0d153c0`, `extensions/debian/teleport-agent` 3.1.0),
`/home/debian/projects/bisquite` (спеки, гайды, примеры), `/home/debian/projects/stvor` (main `4ffcfc16`).
`/home/debian/projects/cream` — пустой репозиторий (ни одного коммита): контракта cream (`install/upgrade/configure/remove`,
`set/show`) в коде ещё нет, всё про cream ниже — допущения по образцу `lib/bisquite-conf`.

## 0. Что сейчас есть в агенте (размер)
- `src/bisquite-teleport` — 1623 строки bash, весь CLI. `lib/bisquite-conf` — 956 строк (копия библиотеки ручек из
  extensions). `src/knobs` (схема домена `teleport`, 14 ключей), `src/knobs.apply` (хук → `bisquite-teleport apply`),
  `src/pkg-setup` (183, postinst/prerm, включая переход с расширения 2.x), `debian/{control.in,postinst,prerm,postrm}`,
  9 юнитов в `src/systemd/`, `tools/{build-deb.sh,check.sh}`, 4 теста (`tests/test-*.sh`, ~1970 строк, ~410 проверок),
  `docs/portal-labels-api.md` (контракт v1), `.github/workflows/ci-release.yml` + release-please.
- Обёртка `cream-extensions/extensions/debian/teleport-agent`: `install.sh` (тарбол Teleport по `TELEPORT_VERSION/
  MIRROR/SHA256` + `.deb` агента), `agent.sh` (пин `AGENT_VERSION=3.1.0`, sha256 `475c19…98c1`, источник — релиз GitHub,
  `AGENT_MIRROR/URL/DEB`), `update.sh` (переустановка `.deb` на работающем роботе), `extension.yaml` (phase build,
  `provides: [teleport-node]`). Рядом: `tools/agent-bump.sh` + `.github/workflows/agent-bump.yml` (подъём пина по
  dispatch из релиза агента), `tools/test-teleport-agent.sh` (264 строки), `tools/test-conf.sh:505-524`.

## 1. Функциональная карта → куда уходит
Легенда: **T** — расширение `teleport` (cream), **S** — `stvor-agent` (Go-служба), **✗** — удаляется, **cream** — исполнитель.

### 1.1 Команды CLI `bisquite-teleport`
| Команда | Что делает | Куда | Обоснование |
|---|---|---|---|
| `join KEY=…` | token / bound_keypair, предпроверка `/webapi/find` + версия кластера, rollback `/var/lib/bisquite/teleport/rollback`, чистый `data_dir`, ожидание `host_uuid` 120 с, разбор отказов журнала, «ждёт сети» (`join-pending`), блокировка `.join.lock` | **T** | Регистрация в Teleport — общая задача, Stvor не нужен. Остаётся командой расширения (`teleport join …` или `cream configure teleport …` с секретом со stdin — форму решает контракт cream). |
| `join-retry` | тик повтора join без сети (таймер) | **T** | Часть регистрации; к порталу отношения не имеет. |
| `scrub-token` | стирание токена/секрета после `host_uuid` (path-юнит) | **T** | То же. |
| `render` | `/etc/teleport.yaml` из ручек + `apps.d` (ExecStartPre/ExecReload) | **T** | Рендер конфига — ядро расширения. Должен научиться «управляемым меткам» (§3). |
| `apply` | хук `knobs.apply`: render + `--no-block reload-or-restart` | **T** (хук `teleport.apply` cream) | Механика ручек cream. |
| `set KEY=…` | `bisquite-conf set --apply teleport …` | **cream** (`cream set teleport …`) + проверка `check_labels` в схеме/хуке **T** | Владелец решил: `cream set/show` вместо bisquite-conf. Запрет ключей `env/app/managed_by/os_logins/teleport.icon/<версия>` — правило схемы T. |
| `status` | ручки, служба, регистрация, секрет, метки портала, ноды и приложения | **делится**: T — регистрация/приложения/ручки; S — `stvor-agent status` (портал, токен «задан», ожидаемые/применённые метки, последний ответ) | Сейчас один вывод смешивает обе зоны. |
| `leave` | выкл. агент, стирает `/var/lib/teleport`, yaml, токены, секрет, **токен меток и состояние меток**, таймеры | **T** (регистрация); часть про метки → **S** (`stvor-agent disable`/`reset`) | После раздела T не должна знать о файлах S. Порядок «leave → S сам перестаёт ходить»: S при 401 и так тихо ждёт; явная команда S нужна для чистоты. |
| `os-logins` | значение динамической метки `os_logins` (NSS, uid≥1000+root, ≤255 знаков, `,...`) | **T** | Метка — справка о машине, а не решение портала; публикует её сам Teleport через `ssh_service.commands`, путь к команде пишет render T. Stvor лишь читает её из инвентаря (`inventory.parse_os_logins`). Делать её в S значило бы, что S правит `teleport.yaml` — нарушение границы. |
| `set-managed env=… space=…` | слить `space` в `TELEPORT_LABELS`, `env` в `TELEPORT_ENV`, **один restart** (не HUP) с подтверждением нового запуска (`InvocationID/MainPID/ActiveEnterTimestampMonotonic`), маркер `labels-restart-pending`, счётчик попыток | **делится**: механизм «записать управляемые метки + restart с подтверждением» → **T** (generic, без слова «портал»); политика (когда звать, пауза 3×/15 мин для тика, ручной вызов без паузы) → **S** | Restart/подтверждение — знание о юните Teleport, принадлежит T. «Портал — источник правды», revision, ETag — знание S. |
| `pull` | тик таймера: GET меток, ETag/304, `X-Applied-Revision`, сверка состояния с ожидаемым, вызов set-managed, отметка applied | **S** | Это и есть «представитель Stvor»: забрать желаемое, применить через T/cream, отчитаться. В Go — цикл службы (ticker 60 с ± 20 с джиттер), а не oneshot+timer. |
| `labels-enable URL=…` | токен со stdin → `/etc/bisquite/teleport/labels-token` (0600, `url=`/`token=`), ручка `TELEPORT_LABELS_URL`, включить таймер, сразу pull | **S** (`stvor-agent enable URL=…`, токен со stdin) | Токен и адрес — секрет Stvor; в ручках `teleport` им не место. |
| `version` | версия пакета | обе | |
| `help` | | обе | |

### 1.2 Юниты systemd (`src/systemd/`)
| Юнит | Куда | Примечание |
|---|---|---|
| `teleport.service` (ExecStartPre=render, ExecReload=render+HUP) | **T** | Ставит расширение. |
| `bisquite-teleport-apps.{path,service}` (apps.d → reload) | **T** | Переименовать (`teleport-apps.*` или как назовёт cream). |
| `bisquite-teleport-token.{path,service}` (`host_uuid` → scrub) | **T** | `RemainAfterExit=yes` — урок AGX Orin, сохранить. |
| `bisquite-teleport-join.{service,timer}` (повтор join) | **T** | |
| `bisquite-teleport-labels.{service,timer}` (pull раз в минуту) | **S** → `stvor-agent.service` (долгоживущая, `Restart=always`) | Таймер не нужен: Go-служба с внутренним тиком. `ConditionPathExists=` токена → служба сама ждёт `enable`. |

### 1.3 Файлы
| Путь сейчас | Куда | Новое место (предложение; совместимости путей не делаем) |
|---|---|---|
| `/usr/sbin/bisquite-teleport`, `/usr/lib/bisquite-teleport/*` | T | каталог расширения cream |
| `/opt/bisquite/knobs/teleport{,.apply}`, `lib/bisquite-conf` | cream | схема домена `teleport` у cream; `lib/bisquite-conf` из пакета ✗ |
| `/etc/bisquite/teleport/config` (ручки) | T/cream | `/etc/cream/teleport/config` (по конвенции cream) |
| `/etc/bisquite/teleport/apps.d/` (публичный контракт для code-server, selkies) | T | новый путь + правка `teleport-app.sh` в code-server/selkies (`cream-extensions/extensions/debian/{code-server,selkies}/teleport-app.sh`, `tools/test-conf.sh:493-503,560-562`) |
| `/etc/teleport.yaml`, `/var/lib/teleport/` | T | без изменений (пути Teleport) |
| `/var/lib/bisquite/teleport/{rollback,registration-secret,join-pending,join-refused,.join.lock}` | T | `/var/lib/cream/teleport/…` |
| `/etc/bisquite/teleport/labels-token` (`url=`,`token=`, 0600 root) | S | `/etc/stvor-agent/token` (0600) + конфиг `/etc/stvor-agent/config` (URL) |
| ручка `TELEPORT_LABELS_URL` (`ro:`) | S | из схемы `teleport` ✗ |
| `/var/lib/bisquite/teleport/{labels-expected,labels-applied,labels-restart-attempts,.labels.lock,.pull.*}` | S | `/var/lib/stvor-agent/state.json` (атомарная запись) |
| `labels-restart-pending` | T (если вариант 1 §3) или S (вариант 2) | маркер «запись сделана, restart не подтверждён» должен жить у того, кто делает и запись, и restart |
| `/var/lib/bisquite/teleport/2.x-units/`, ссылка `/usr/local/sbin/bisquite-teleport`, перенос юнитов 2.x в `pkg-setup` | ✗ | роботов 2.x нет — переход не нужен |

### 1.4 Внешние контакты
| Куда | Кто | После |
|---|---|---|
| `https://<proxy>/webapi/find` (curl, 15 с) — предпроверка версии | join/join-retry | T |
| Teleport proxy (сам `teleport start`) | teleport.service | T |
| `journalctl -u teleport` (отказы join, «does not allow role App») | join, status | T |
| `getent passwd` | os-logins | T |
| `https://<портал>/robot-api/v1/labels` (curl `--proto =https`, 10/30 с, `--max-filesize 4096`, заголовки файлом `-H @file`) | pull | S (Go `net/http`: без редиректов, TLS по системным CA, лимит тела 4096) |
| `systemctl restart/show/is-active teleport.service` | set-managed/pull | T (вариант 1) или S (вариант 2) |
| GitHub releases (`.deb`) / зеркало Stvor `/dist/teleport/` | обёртка `install.sh/update.sh`, скрипт Stvor | обёртка `stvor-agent` в cream-extensions + скрипт Stvor |

### 1.5 Статические метки ноды (render)
- `managed_by=bisquite-teleport-agent` — Stvor по ней выводит режим «агент есть» (`labels.label_mode`). После раздела
  T не должна ставить метку от имени Stvor. Предложение: T ставит `managed_by=cream-teleport` (или ничего), а S через
  управляемые метки — `stvor_agent=<версия>` (одна метка: и признак, и версия). Stvor переключает `label_mode` на неё.
- `bisquite_agent_version=<X.Y.Z>` — читает только фронт (`RobotLabels.tsx:96-101`). → ✗, заменяет `stvor_agent`.
- `env`, `space` (элемент `TELEPORT_LABELS`), `os_logins` (динамическая) — см. §2–§3.

## 2. Контракт со Stvor, который обязан сохранить stvor-agent
Источник истины — `stvor/backend/app/modules/teleport/robot_api.py`, `labels.py`; копия — `stvor-agent/docs/portal-labels-api.md`.

**Эндпоинт** `GET|HEAD https://<BASE_DOMAIN>/robot-api/v1/labels` (вне `/api`, без сессии/CSRF). Адрес на роботе —
ровно `^https://[a-z0-9.-]+(:[0-9]{1,5})?/robot-api/v1/labels$` (Stvor генерирует строже: `labels.LABELS_URL`).
- Запрос: `Authorization: Bearer <токен>` (токен — 43 знака `[A-Za-z0-9_-]`, `secrets.token_urlsafe(32)`, у Stvor
  в базе только sha256); `If-None-Match: "<ревизия>"` (в кавычках; сервер сравнивает `strip()`-нутое с `"rev"`);
  `X-Applied-Revision: <ревизия>` — агент шлёт **только** вместе с If-None-Match той же ревизии и только если ручки
  несут ожидаемые метки, restart подтверждён, агент активен. Сервер пишет `applied_revision` только если она равна
  текущей ожидаемой; `pulled_at` — не чаще 50 с (`PULL_RECORD_INTERVAL`). HEAD ничего не пишет.
- Ответы: `200` тело байт в байт `{"labels":{"env":"<v>","space":"<v>"},"revision":"<16 hex>"}` (компактный JSON,
  ключи ровно в этом порядке), заголовки `ETag: "<rev>"`, `Cache-Control: no-store`; `304` с тем же ETag; `401`
  `{"code":"robot_api.unauthorized",…}` + `WWW-Authenticate: Bearer` — любая причина (нет токена, перевыпущен,
  робот списан); `500 robot_api.labels_invalid` — ожидаемые метки не прошли `^[a-z0-9][a-z0-9-]{0,62}$`.
- Ревизия = `sha256("env=<env>\nspace=<space>\n")[:16]`. `env` — `TELEPORT_ENV` экземпляра, `space` — slug
  ожидаемого пространства (цель переноса или текущее).
- Частота: тик 1 мин (`OnBootSec=1min`, `OnUnitActiveSec=1min`, `RandomizedDelaySec=20s`). Traefik: 20/с на IP
  (burst 200) и **6/мин на значение `Authorization`** (burst 6) — Go-службе нельзя ретраить чаще (на 429 — ждать тик).
- Поведение клиента, на которое опирается Stvor: (а) сверяется **состояние**, а не ревизия — на каждом тике и на 304;
  (б) смена `space/env` — **restart** (не HUP), узел и **все приложения** переезжают вместе: сверка инвентаря Stvor
  завершает перенос, только когда метку цели несут узел и все его приложения (ADR-0013); (в) сеть/401/429/3xx/5xx —
  одна строка журнала, ничего не тронуто; (г) после 3 неподтверждённых restart подряд — не чаще раза в 15 мин;
  (д) редиректам не следует; токен не попадает в argv/журнал/status.
- Чего портал сделать не может (сохранить): только два значения из DNS-алфавита; не меняет кластер, токен, приложения,
  прочие метки; не задаёт `app/managed_by/os_logins/teleport.icon/<метку версии>`.

**Как скрипт установки Stvor зовёт агента сейчас** (`install_script.py`, `enrollment.py`, `labels.py`):
```sh
# способ 3 (скрипт, online/--offline), после teleport/install из тарбола и dpkg -i bisquite-teleport-agent_<v>_all.deb:
command -v bisquite-teleport || refuse …
printf '%s' "$SECRET" | bisquite-teleport join TELEPORT_PROXY=<proxy> TELEPORT_JOIN_METHOD=bound_keypair \
  TELEPORT_TOKEN=<имя токена> TELEPORT_REGISTRATION_SECRET=- TELEPORT_ENV=<env> TELEPORT_NODENAME=<имя> \
  TELEPORT_LABELS=space=<slug> [TELEPORT_FORCE=1]          # || refuse; ненулевой код = отказ кластера
printf '%s' "$LABELS_TOKEN" | bisquite-teleport labels-enable 'URL=https://<домен>/robot-api/v1/labels'  # отказ — предупреждение
# способ 4 (терминал робота на образе bisquite):
read -rs S && printf '%s' "$S" | sudo bisquite-teleport join …; unset S
read -rs T && printf '%s' "$T" | sudo bisquite-teleport labels-enable 'URL=…'; unset T
# способ 5 (firstboot-commands манифеста bisquite):
printf %s '<секрет>' | bisquite-teleport join …
printf '%s' '<токен>' | bisquite-teleport labels-enable 'URL=…' || true
# ручной запасной путь (карточка, «Применить вручную»):
sudo bisquite-teleport set-managed 'env=<env>' 'space=<slug>'
```
Отказы скрипта D5/R2 до чтения секрета опираются на имена: пакет `bisquite-teleport-agent` (`dpkg-query`),
`/etc/stvor/labels-token`, юнит `/etc/systemd/system/teleport.service`, `teleport.service.d/*.conf` без пакета агента.
`--offline` требует рядом `.deb` и `curl ca-certificates util-linux` (зависимости пакета).

**Метки, которые читает Stvor:** `space`, `env` (сверка переноса — `inventory.py`), `os_logins` (динамическая;
`inventory.parse_os_logins`, лимит 64 имени, хвост `,...`), `managed_by=bisquite-teleport-agent` (`labels.label_mode`
→ `bisquite|no_agent|unknown`), `bisquite_agent_version` (фронт). Гейты проверяют `{space, managed_by}` у узла.

## 3. Как stvor-agent применяет метки, если Teleport — отдельное расширение
Требования, которые нельзя потерять: (1) запись `env`+`space` и **один restart с подтверждением нового запуска**
(не HUP, не `--no-block` хук cream — по конвенции хук ручек обязан быть неблокирующим, `docs/extensions.md:463-470`);
(2) метки попадают и в узел, и во все приложения одним render; (3) прочие метки оператора не трогаются; (4) S должна
узнать «действует ли» (ручки = ожидаемые, restart подтверждён, служба активна), чтобы слать `X-Applied-Revision`;
(5) маркер «записано, restart должен» переживает падение между записью и restart.

### Вариант 1 (рекомендую): «управляемые метки» как понятие расширения `teleport` + синхронная команда
- В схеме `teleport` новая ручка `TELEPORT_MANAGED_LABELS` (`ro:list:k=v`, пишет только команда расширения; `cream set`
  отказывает) — метки внешнего управляющего. render: `TELEPORT_LABELS` ∪ managed (managed побеждает по ключу; в managed
  разрешён `env`, перекрывающий `TELEPORT_ENV`), копируется в узел и в каждое приложение.
- Команда расширения (форма зависит от cream: `teleport set-managed …` / `cream run teleport set-managed …`):
  `set-managed env=prod space=sp-1 stvor_agent=1.0.0` — проверка DNS-алфавита, запрет зарезервированных ключей, маркер
  pending, запись, `systemctl restart` + подтверждение (`InvocationID/MainPID/ActiveEnter…`), коды: 0 — применено или
  без изменений, 1 — не подтверждён (маркер остаётся), 2 — неверный ввод. Плюс `managed-status --json`
  (`{"managed":{…},"pending":bool,"active":bool}`) — для решения S об `X-Applied-Revision`.
- S: хранит ожидаемое/ревизию/ETag/счётчик попыток (политика 3×/15 мин), зовёт `set-managed`, читает `managed-status`.
- Плюсы: restart-семантика и подтверждение — у владельца юнита; нет read-modify-write `TELEPORT_LABELS` чужим процессом
  (сейчас `labels_with_space` сливает `space` в строку оператора — гонка с `cream set`); операторский `cream set teleport
  TELEPORT_LABELS=space=x` больше не «откатывается» тиком — ключи разведены; T остаётся общей (managed — не «портал»,
  годится для любого управляющего). Метка признака агента (`stvor_agent=<версия>`) едет тем же каналом.
- Минусы: у T два новых публичных интерфейса (ручка + 2 команды), их надо держать стабильными; S зависит от версии T
  (пин минимальной версии в обёртке `stvor-agent`: `requires: [teleport>=X]`).

### Вариант 2: только общий интерфейс cream + restart силами stvor-agent
- S: `cream set --no-apply teleport TELEPORT_ENV=… TELEPORT_LABELS=<слитое>` (или `TELEPORT_MANAGED_LABELS`, если её
  всё же завести), затем сам `systemctl restart teleport.service` через D-Bus/`systemctl` и подтверждение; маркер pending
  и попытки — в состоянии S; «действует ли» — `cream show teleport` + `systemctl is-active`.
- Плюсы: T не знает про управляемые метки (кроме, возможно, ручки), меньше кода в расширении; S полностью на Go.
- Минусы: S знает имя юнита и правило «restart, не HUP» чужого расширения; два компонента рестартуют одну службу
  (оператор `cream set` → reload, S → restart) без общей блокировки; при слиянии в `TELEPORT_LABELS` — гонка с
  оператором и откат его правок (как сейчас); `--no-apply` должен существовать в cream (не гарантировано).

Отвергнуто: drop-in `labels.d/stvor.conf` + path-юнит у T (как `apps.d`) — асинхронно, S не узнаёт момент и успех
restart, `X-Applied-Revision` пришлось бы выводить косвенно; path-юнит по правилу cream дал бы reload, а не restart.

**Рекомендация — вариант 1.** Он сохраняет все пять требований внутри одной границы и делает T переиспользуемым.

## 4. Тесты и CI агента сейчас и что переиспользовать
| Что есть | Объём | Судьба |
|---|---|---|
| `tests/test-labels.sh` — set-managed, pull, labels-enable, юниты таймера; подменённый корень `BISQUITE_TELEPORT_ROOT`, поддельные curl/systemctl/dpkg/teleport | ~101 проверка, 16 разделов | **Главная спецификация S.** Сценарии (ETag/304, X-Applied-Revision только при «действует», строгий разбор тела и NUL, 401/429/5xx = код 0, расхождение ручек → применить, pending → restart, 3×/15 мин, токен не в argv/журнале, другой URL ≠ URL токена → не слать, права файла токена ≠0600/не root → отказ, смена портала сбрасывает expected) переносятся в Go table-тесты: `httptest.Server` + фейк интерфейса T (`set-managed/managed-status`) + фейковые часы. Часть про restart/подтверждение — в тесты T (вариант 1). |
| `tests/test-join.sh` — join без сети, разбор отказов, 0600, тик повтора, идемпотентность, блокировка, status | ~99 | **Целиком в T** (bash остаётся bash; перенос в репозиторий cream-extensions вместе с CLI). |
| `tests/test-conf.sh` — `lib/bisquite-conf`, сверка с EnvironmentFile systemd | ~159 | → в репозиторий cream (библиотека ручек уходит туда); у агента не нужен. |
| `tests/test-package.sh` — состав/права/воспроизводимость `.deb`, postinst/prerm/postrm, переход с 2.x | ~54 | Идея воспроизводимости и проверок maintainer-скриптов → S (сборка `.deb` Go-бинаря: `dpkg-deb` как сейчас или nfpm); сценарии 2.x ✗. |
| `tools/check.sh` (shellcheck + тесты), `make check/test/lint/deb` | | S: `go vet`, `staticcheck`/`golangci-lint`, `go test -race`; T — shellcheck в cream-extensions `tools/check.sh`. |
| `tools/build-deb.sh` — `dpkg-deb`, `SOURCE_DATE_EPOCH`, `gzip -n`, `SHA256SUMS` | | Переиспользовать почти как есть (архитектура станет `amd64`/`arm64` — Go-бинарь не `_all`; **два .deb и две суммы** → Stvor и обёртка уже умеют `AGENT_SHA256_<ARCH>`, а Stvor — нет, §5). `CGO_ENABLED=0 -trimpath -buildvcs=false` для воспроизводимости. |
| `ci-release.yml`: check → release-please → сборка из тега + проверка `VERSION`=тег → upload → `dispatch-extensions` в `bisquite-extensions/agent-bump.yml` (PAT `EXTENSIONS_DISPATCH_TOKEN` до 16.10.2027) | 204 строки | Сохранить схему; цель dispatch → `cream-extensions`, расширение `stvor-agent`; `HOME_REPO` → `iamletenkov/stvor-agent`; переименовать артефакт. Release-please манифест — сбросить на 0.1.0/1.0.0 (новый пакет). |
| cream-extensions: `tools/agent-bump.sh`, `agent-bump.yml`, `tools/test-teleport-agent.sh` | 264 строки тестов | Переиспользовать для обёртки `stvor-agent` (пин версии + sha256 по архитектурам). |
| Stvor: `robots-gate` фазы 12/13/14, `arm64-gate`, `stand-seed` | | Живые e2e — главный приёмочный контур; фаза 12 гоняет агент в контейнере с поддельным systemctl (`BISQUITE_TELEPORT_SYSTEMD=1`) — S нужен эквивалент (`stvor-agent pull --once` + переменная/флаг фейкового T). |

## 5. Правки в Stvor (`/home/debian/projects/stvor`)
Всего строк с «bisquite» вне `.git`/`node_modules`: 1464 (бо́льшая часть — история в `docs/plans`, их не правим).

**Настройки/окружение**
- `.env.example:160-164` — блок «Пакет агента bisquite»: `BISQUITE_AGENT_VERSION/SHA256` → `STVOR_AGENT_VERSION` и
  `STVOR_AGENT_SHA256_AMD64/_ARM64` (Go-бинарь — по архитектуре); плюс, по решению §7-В1, пины cream и архива расширений.
- `compose.yaml:59-60` (`x-runtime-env`) — те же имена.
- `backend/app/settings.py:65-90,193-201,444-478` — `AgentPackage`, `agent_deb_name` (`bisquite-teleport-agent_<v>_all.deb`
  → `stvor-agent_<v>_<arch>.deb`), поля `bisquite_agent_*`, валидатор пары, свойство `bisquite_agent` → `stvor_agent`
  (становится mapping по arch, как `teleport_agent_sha256`).
- `scripts/env-preflight.sh:59,90-101,255` — проверка пары пинов (→ по архитектурам).
- `scripts/fetch-teleport-tarball.sh:18-20,289-350` (`fetch_agent_deb`, `BISQUITE_AGENT_SOURCE`, URL релиза
  `github.com/iamletenkov/bisquite-teleport-agent`), self-test `:557-561,613-758` — имена, источник
  `github.com/iamletenkov/stvor-agent`, по файлу на архитектуру.
- `Makefile:162` (help `fetch-teleport`), `:180` (help `arm64-gate`) — тексты.

**Зеркало и API**
- `backend/app/modules/teleport/agent_deb.py:18` — regex `AGENT_DEB` → `stvor-agent_(?P<version>…)_(?P<arch>amd64|arm64)\.deb`.
- `backend/app/main.py:110-127,155-204,390` — раздача `.deb` (`TeleportMirror(agent=…)`), комментарии; путь «раскладки
  bisquite» `/dist/teleport/binaries/teleport/<ver>/` — нужен расширению `teleport` (`TELEPORT_MIRROR`), оставить, текст
  «bisquite» → «cream».
- `backend/app/modules/teleport/downloads.py:253-325` — файл манифеста `key="bisquite_agent"` (`arch: null`) → `stvor_agent`
  по архитектурам; `TeleportBuildOut.agent_version`.
- `backend/app/modules/teleport/schemas.py:49-101,133-238` — `AgentPackageOut` (по arch), `LabelTokenOut.bisquite_enable_command`
  /`bisquite_firstboot_line` → `enable_command`/`firstboot_line`; `LabelMode = Literal["bisquite",…]` → `"agent"`;
  описания полей. Затем `make gen-api` → `frontend/openapi.json`, `frontend/src/api/generated/types.gen.ts`.
- `backend/app/modules/teleport/errors.py:49-60` — тексты `agent_unavailable`, `workstation_unsupported`.
- `backend/app/modules/teleport/service.py:198-200`, `install_code.py:13,149,172,236` — `settings.bisquite_agent`.

**Команды на робот**
- `install_script.py` (весь шаблон `_TEMPLATE` и докстринг): установка (см. §7-В1: либо `dpkg -i cream`+`cream install
  teleport`+`cream install stvor-agent`, либо тарбол как сейчас + `.deb` stvor-agent), выбор `.deb` по `HOST_ARCH`
  (сейчас один `_all`), отказы D5 (`installed bisquite-teleport-agent` → новые имена пакетов; `/etc/stvor/labels-token`
  отказ можно удалить — клиентов `stvor-labels` больше нет нигде), `command -v bisquite-teleport`, команда join (§2),
  `labels-enable` → `stvor-agent enable`, тексты «агент bisquite», `--offline`-зависимости.
- `enrollment.py:1-14,112-200` — `_join` (`bisquite-teleport join` → команда расширения `teleport`), докстринги;
  `join_arguments` — ключи `TELEPORT_*` сохраняются, если T их сохранит (желательно: тогда тесты рендера почти не меняются).
- `labels.py:58-60,72-78,113-140,230-231` — `BISQUITE_MANAGED_BY`, `label_mode` (→ по метке `stvor_agent`), `manual_command`
  (`bisquite-teleport set-managed` → `teleport set-managed`/`stvor-agent apply` — решить), `render_bisquite_enable_command`,
  `render_bisquite_firstboot_line`, комментарии `LABEL_TOKEN`/`LABEL_VALUE`. Контракт `robot_api.py` — **без изменений**
  (только докстринг).
- `inventory.py:44` — комментарий про `os_logins` bisquite.
- `backend/app/modules/resources/{models.py:52,244,313, robots.py:1382-1390}`, `alembic/versions/0006_slice3_robots.py:6` —
  только комментарии (миграцию не трогать).

**Тесты backend** (14 файлов): `tests/teleport/{test_install_script,test_enrollment_render,test_enrollment_api,
test_install_code_api,test_label_enable,test_label_token,test_os_logins,test_secret,test_downloads}.py`,
`tests/{test_settings,test_teleport_mirror}.py`, `tests/resources/{test_robot_connect_links,test_robot_invariants,
test_robot_login_access}.py`; `test_systemd_units.py` — юнит рабочей станции, не трогается.

**Фронт** (29 файлов `src`, 3 e2e): `src/i18n/ru.json` ~60 строк (`robots.pull.mode.bisquite`, `bisquite_version`,
`dialog.tabs.bisquite/firstboot`, `enrollment.methods.script_bisquite|bisquite_join|firstboot|offline`, группы
`no_bisquite/bisquite_image`, бейдж `needs_bisquite`, `downloads.bisquite_agent`, ошибки); `features/teleport/
{enrollmentMethods.ts (ключи способов `script_bisquite`, `bisquite_join`, группы), MethodPanel.tsx, InstallCommand.tsx,
TeleportFiles.tsx, downloadFiles.ts}`, `features/resources/{RobotLabels.tsx:96-184 (метка версии → `stvor_agent`),
LabelTokenDialog.tsx (вкладки, поля ответа), RobotEnrollPage.tsx, enroll/InstallStep.tsx, enroll/useEnrollContext.ts}`,
`ui/code-block.tsx`, моки `src/test/mock-{robots,downloads}.ts`, ~15 тестов `*.test.ts(x)`, e2e
`robots.spec.ts:11-12,119-126,267`, `workstation.spec.ts:12,96`, `qa-tour.spec.ts`. Способы 4–5 («образ bisquite») —
переформулировать в «образ с расширениями cream `teleport` и `stvor-agent`».

**Гейты и скрипты**: `scripts/robots-gate.sh` (65 строк: фазы 12–14, `lbl_agent`, `BISQUITE_TELEPORT_SYSTEMD`, проверка
`{space,managed_by}` `:344-346,1379-1432`), `scripts/arm64-gate.sh` (22: `:99-100` имя `.deb`, `:139,522`),
`scripts/stand-seed.sh:20-22,215,259 (разбор токенов `bisquite-teleport join`),386-387`, `scripts/workstation-gate.sh:18,529`
(`dpkg -s bisquite-teleport` → новые пакеты), `scripts/smoke-stack.sh:170-191`, `scripts/check-teleport-pin.sh:87-115`,
`scripts/teleport-gate.sh:523`, `scripts/lint-repo.sh:410` (комментарий), `_typos.toml:4` (добавить `cream`).

**Документы**: новый **ADR-0030** «stvor-agent и расширение `teleport` каталога cream» — `Supersedes 0016` (имя пакета,
regex зеркала, пины, поля API, способ установки), уточняет ADR-0013 (клиент `bisquite-teleport pull` → `stvor-agent`,
HTTP-контракт v1 без изменений, метка-признак `stvor_agent`), строки-уточнения в ADR-0025 (стр. 16,36,64,73) и
ADR-0022 (стр. 60,104), ADR-0016 → `Superseded by 0030`; `docs/adr/README.md`. Спека перехода `docs/specs/2026-10-1x-stvor-agent.md`
+ план. `docs/runbooks/10_robot-enrollment.md` — §1 таблица способов (стр. 17-25), §3.2-3.3 (вкладки, `bisquite-teleport
status`, юнит `bisquite-teleport-labels`), §6 диагностика, §7 гейт, §10 целиком («Пакет агента bisquite» → «stvor-agent»),
§15; исторические §8/§12 не трогать. `teleport/README.md:31-39,123-241,276-298` («Контракт с bisquite» → «Контракт с
cream/stvor-agent»), `backend/app/modules/teleport/README.md:6-29,115,152,171,195,200`, `backend/README.md`,
`frontend/README.md`, `docs/README.md`, `docs/REUSE-MAP.md`, `docs/STATUS.md`, `docs/runbooks/01_dev-stand.md`.

**Не путать:** `TELEPORT_AGENT_VERSION` и `TELEPORT_AGENT_SHA256_<ARCH>` в Stvor — это **бинарь Teleport** (агент =
нода), а не bisquite; переименовывать не обязательно, но рядом с `STVOR_AGENT_*` путаница вырастет (кандидат на отдельное
переименование в `TELEPORT_NODE_*`, вне объёма).

**Объём:** backend ~12 модулей + 14 тестовых файлов, фронт ~30 файлов + 3 e2e + i18n, ~10 скриптов/гейтов, ~10 документов +
ADR + спека + план. По опыту среза 14 (сопоставимый объём) — 1 срез, 5–7 задач, 2–3 параллельных исполнителя
(backend+скрипт установки / фронт+i18n / гейты+доки), плюс живая приёмка на `pve-robot-1` (stand-seed) и гейты 12–14/arm64.
Основной риск объёма — не Stvor, а готовность cream (§7-В1).

## 6. Правки в bisquite (`/home/debian/projects/bisquite`) — только перечень
- `docs/specs/2026-09-14-teleport-agent.md` (372 стр.; CLI, пути `/usr/local/sbin/bisquite-teleport`, `os-logins`,
  bound_keypair, метки с портала стр. 214-300, `update.sh`) — пометить как историю/перенаправить на T и stvor-agent.
- `docs/plans/2026-09-14-teleport-agent.md` — история, только ссылка.
- `docs/guides/device.md:1036-1047` — «расширение teleport-agent 2.4.0» и строка `bisquite-teleport join` в
  `firstboot-commands`.
- `docs/specs/2026-09-14-guest-layout-bisquite.md:17,94` (`/var/lib/bisquite/teleport`), `docs/specs/2026-09-15-extension-knobs-library.md:11,147`
  (+ план `:10,42`; домен `teleport`, `bisquite-teleport set` → `cream set`), `docs/specs/2026-09-15-jetson-nano-robot-chain.md:49,103,111`.
- VMFILE: `examples/build/jetson/nano/jetson-nano-robot.vmfile:111-118`, `examples/build/jetson/orin/jetson-orin-robot.vmfile:71-76`,
  `examples/build/amd64/ubuntu2404/robot-ws.vmfile:81`, `examples/dst/amd64/build/dst-robot-ws.vmfile:62`
  (`EXTENSION teleport-agent TELEPORT_VERSION=18.10.0` → `EXTENSION teleport …` + `EXTENSION stvor-agent …`).
- Манифесты устройств/деплоя (строки `bisquite-teleport join` и `labels-enable` в `firstboot-commands`):
  `examples/device/robot-ws.yml:74-88`, `examples/device/jetson-nano/jetson-nano-robot-{a02,b01}.yml:117-124`,
  `examples/device/jetson-agx-orin/jetson-orin-robot.yml:110-117`, `examples/dst/jetson/device/robot.yml:83-94`,
  `examples/dst/amd64/device/robot-ws.yml:64-75`, `examples/deploy/robot-ws.yml:76-86`, `examples/dst/amd64/deploy/robot-ws.yml:82`.
- README примеров: `examples/build/jetson/nano/README.md:24,103,225`, `examples/build/amd64/ubuntu2404/README.md:234`,
  `examples/dst/amd64/README.md:35,94`, `examples/dst/jetson/README.md:146`.
- Код: `src/bisquite/infrastructure/deploy/proxmox/adapter.py:343` (комментарий), `tests/test_proxmox_adapter.py:167` (строка-пример).
- Смежное (переименование каталога, не агента): подмодуль `bisquite-extensions` (`.gitmodules`, `tools/extensions-bump.sh`,
  `src/bisquite/domains/build/models/extensions.py`, `pyproject.toml`, `Makefile`, ~15 доков) → `cream-extensions`.
- `CHANGELOG.md` — историю не править.

## 7. Риски и открытые вопросы
**В1 (блокер). Как cream и расширения попадают на чистый хост (способы 3 и 6 Stvor).** Сейчас скрипт Stvor сам ставит
Teleport из тарбола зеркала (форк amd64 по пину!) и один `.deb`. Если S применяет метки через T, на хосте нужны cream +
расширение `teleport` + `stvor-agent`. Варианты: (а) зеркало Stvor раздаёт `.deb` cream и архив расширений по пинам, скрипт
делает `cream install teleport TELEPORT_MIRROR=https://<домен>/dist/teleport TELEPORT_SHA256=<пин форка> … && cream install
stvor-agent AGENT_MIRROR=…` (ещё 2–3 пина в `.env`, офлайн-набор растёт); (б) скрипт Stvor ставит T и S без cream (тогда
«cream set» на таком роботе нет — два способа жизни). Нужен рулинг владельца и минимальный контракт cream до начала среза.
**В2. Контракт cream не существует** (репозиторий пуст): форма команд расширения (`cream run teleport set-managed`?),
`--no-apply`, `ro:`-ручки, порядок `requires`/версий. Вариант 1 §3 требует от cream «команды расширения» и ro-ручек.
**В3. Teleport-форк и `TELEPORT_SHA256`.** Тарбол форка ≠ `.sha256` CDN: при установке через T скрипт обязан передать пин
форка явно (обёртка уже умеет `TELEPORT_SHA256`, но по одной сумме — на arm64 официальный тарбол, у amd64 форк; передавать по
архитектуре хоста).
**В4. Архитектурно-зависимый `.deb`.** Go-бинарь ломает «один `_all.deb`» (ADR-0016, regex, манифест «все архитектуры»,
`arch: null`, тексты «все архитектуры»): пины и раздача — по архитектурам.
**В5. Метка-признак агента.** `managed_by=bisquite-teleport-agent` объявлена в агенте «частью контракта, не
переименовывается» — после раздела её никто не ставит. `stvorhost` (агент 3.0.1, трогать нельзя) останется со старой меткой:
при переключении `label_mode` его карточка покажет «без агента — нужна перерегистрация», хотя pull у него продолжит работать
(HTTP-контракт неизменен). Решить: временно считать старую метку `agent` (только для stvorhost) или принять.
**В6. Атомарность «узел + приложения»** — держится только если managed-метки render копирует и в приложения (сейчас
так); при варианте 2 и любом drop-in легко потерять — сверка переноса в Stvor зависнет (алерт `stvor-robot-move-stalled`).
**В7. Гонка двух процессов на одном юните** (оператор `cream set` → HUP, S → restart) — нужна общая блокировка у T
(сейчас `.labels.lock` + `.join.lock` в одном скрипте). Вариант 1 решает это внутри T.
**В8. Секреты.** Токен меток сейчас не попадает в argv (curl `-H @file`); Go это упрощает, но вызов T (вариант 1) не
должен получать токен вообще — передаются только значения меток. Секрет регистрации идёт только в T, S его не видит.
**В9. Повтор приёмки.** ADR-0013 Accepted по живому переносу на stvorhost; после раздела нужен новый живой перенос
(узел + приложение) на `pve-robot-1` — stand-seed (`:386-387`) и манифест `SEED_PVE_DIR` на PVE (вне репозитория) тоже
переписываются; `robots-gate` фаза 12 опирается на поддельный systemctl агента.
**В10. Утрата проверок при переписывании.** ~100 проверок `test-labels.sh` и ~100 `test-join.sh` — регрессионный щит
(строгий разбор тела, NUL, права файла токена, URL токена ≠ URL ручки, R3-маркер). Перенос без таблицы соответствия
«bash-проверка → Go-тест» почти гарантированно потеряет часть.
**В11. Обновления.** Канал обновлений отложен; но `update.sh` обёртки (переустановка `.deb` по пину) исчезает вместе с
`teleport-agent` — до появления канала обновление S/T на роботе = `cream upgrade` вручную; runbook 10 §10.4 переписать.
**В12. Имена.** Внутри cream: расширение `teleport` vs способность `teleport-node` (`provides`), юниты (`teleport-apps.path`?),
каталоги `/etc/cream/teleport` — согласовать до кода, т.к. `apps.d` — публичный контракт для code-server/selkies.
**Открытые вопросы владельцу:** В1 (как ставится cream на чистый хост), выбор варианта §3, имя метки-признака
(`stvor_agent=<версия>`?), судьба `stvorhost` в UI (В5), остаётся ли ручная команда «Применить вручную» и чья она
(`teleport set-managed` или `stvor-agent apply`), служба S — daemon (рекомендую) или oneshot+timer.
