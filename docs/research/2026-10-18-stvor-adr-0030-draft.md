# 0030. stvor-agent и расширение `teleport` каталога cream: бутстрап робота с зеркала портала

- **Status:** Proposed (Accepted — после выкладки среза на стенд: переустановленная `pve-robot-1` регистрируется одной
  командой портала с зеркала по пинам, узел несёт `space`, `env`, `stvor_agent=<версия>`; перенос робота в другое
  пространство и обратно завершается сверкой, узел и приложение меняют `space` вместе; `arm64-gate` и
  `workstation-gate` зелёные на новом пути; `stvorhost` (агент 3.0.1) по-прежнему «агент есть» и получает метки)
- **Date:** 2026-10-18
- **Deciders:** владелец (экосистема cream / cream-extensions / stvor-agent / bisquite / Stvor; stvor-agent на Go,
  `.deb` по архитектурам, служба; Teleport-логика — расширение `teleport`; без переходной совместимости; контракт
  `/robot-api/v1/labels` не меняется — 2026-10-18), контроллер (рулинги Р1 «метка `stvor_agent`» и Р2 «бутстрап»)
- **Tags:** backend, infra, security, teleport, robots
- **Supersedes:** [0016](0016-bisquite-agent-package-mirror.md) — целиком
- **Уточняет:** [0013](0013-robot-label-pull.md) (клиент меток), [0015](0015-teleport-downloads-mirror.md) и
  [0022](0022-teleport-download-versions.md) (новые файлы зеркала и манифеста), [0025](0025-workstation-personal-robot.md)
  (способ установки рабочей станции)

## Контекст и проблема
Агент bisquite (`bisquite-teleport-agent` 3.x, ADR-0016) — один `_all.deb` с bash-CLI, который и регистрирует узел в
Teleport, и представляет портал на роботе (pull меток ADR-0013). Владелец разделил его (2026-10-18): Teleport-логика
уходит в расширение `teleport` каталога **cream-extensions**, исполняемое **cream** (Go, `install/upgrade/configure/
remove`, `cream set/show`, `cream run <ext> <действие>`); представитель портала — **stvor-agent**, статический
Go-бинарь, `.deb` **по архитектуре** (amd64, arm64), долгоживущая служба. Спека агента —
[stvor-agent на Go и расширение `teleport`](https://github.com/iamletenkov/stvor-agent/blob/main/docs/specs/2026-10-18-stvor-agent-go.md), контракт исполнителя —
[спека cream](https://github.com/iamletenkov/cream/blob/main/docs/specs/2026-10-18-cream-core.md).

Для портала это значит: на чистый хост теперь надо доставить не один пакет, а четыре вещи — cream, снимок каталога
расширений, stvor-agent и (как сейчас) тарбол Teleport, — в том числе роботу без интернета. Имена файлов, адреса
зеркала, пины в `.env`, поля API и команды на роботе — внешний контракт (закладки, офлайн-наборы, образы bisquite,
автоматизация); каждый пункт решения ADR-0016 меняется (имя пакета, одна сумма → суммы по архитектурам, ещё три файла,
другой скрипт). Вторая сила — признак «агент есть» в инвентаре: метку `managed_by=bisquite-teleport-agent` расширение
`teleport` ставить больше не будет, а `stvorhost` с агентом 3.0.1 переустанавливать без владельца нельзя.

## Рассмотренные варианты
**Как ставить на чистый хост (способы «Скрипт» и «Без интернета»):**
- **A — зеркало портала раздаёт cream, снимок каталога, stvor-agent и тарбол по пинам `.env`; скрипт ставит cream,
  подключает снимок как источник и ставит расширения `teleport` и `stvor-agent` (выбран, рулинг Р2).** Плюсы: на
  роботе портала и на образе bisquite — одна и та же установка Teleport и агента, `cream set/show/upgrade` есть везде;
  цепочка «пин → сверка на зеркале → сверка на роботе» та же. Минусы: три новых группы пинов, офлайн-набор из пяти
  файлов, портал зависит от выпусков cream и каталога.
- **B — скрипт Stvor ставит `teleport` и `stvor-agent` без cream** (распаковка расширения руками, `.deb` агента).
  Минусы: два способа жизни робота — на роботе портала нет `cream set/upgrade`, расширение исполняется не тем, для
  кого написано; stvor-agent зовёт `cream run` — без cream ему нечем применять метки.
- **C — робот качает всё с GitHub сам.** Минусы: роботы в закрытой сети; версии расходятся с порталом (отвергнуто ещё
  в ADR-0016).
- **D — apt-репозиторий на портале.** Минусы: ключ подписи `Release`, индекс, жизненный цикл — вне объёма (как в
  ADR-0016).
- **E — `/dist/cream/` отдельным префиксом зеркала.** Плюсы: имена честнее. Минусы: второй том, второй класс зеркала,
  второй маршрут и второй набор проверок ради имени; `/dist/teleport/` уже «зеркало всего, что нужно роботу».

**Рабочая станция (ADR-0025):**
- **W1 — статус-кво:** скрипт Stvor ставит Teleport из тарбола и сам пишет `teleport.yaml`, `/etc/teleport-secret`,
  юнит. Плюсы: на личном компьютере ничего лишнего. Минусы: в портале остаётся второй установщик Teleport со своим
  рендером конфига и юнитом, без повтора join без сети (ноутбук, NAT, сон), без отката, без стирания секрета — то,
  что расширение `teleport` уже умеет и тестирует.
- **W2 — cream + расширение `teleport`, без stvor-agent и без токена меток (выбран).** Плюсы: один путь установки
  Teleport на всех хостах портала; станция получает `join-retry` (повтор регистрации, пока нет сети), откат и
  стирание секрета; скрипт портала перестаёт писать `teleport.yaml` и юнит в любом режиме. Минусы: на личный компьютер
  ставится cream (исполнитель от root) и снимок каталога; офлайн-набор станции — четыре файла.
- **W3 — как робот, со stvor-agent.** Отвергнут: станцию нельзя перенести (ADR-0025), метки с портала ей не нужны, а
  токен меток — лишний секрет на личной машине.

**Признак агента в инвентаре:**
- **K1 — только новая метка `stvor_agent`.** Минусы: `stvorhost` станет «без агента — нужна перерегистрация», хотя
  pull у него работает.
- **K2 — `stvor_agent` или старая `managed_by=bisquite-teleport-agent`, старое распознавание снимается отдельной
  задачей после переустановки `stvorhost` (выбран, Р1).**

## Решение
Вариант A для бутстрапа, W2 для рабочей станции, K2 для признака агента.

1. **Пины** (`.env`, технический блок, не секреты; `x-runtime-env` в `compose.yaml`; проверка — `RuntimeSettings` и
   `env-preflight`):
   | Группа | Переменные | Правило |
   |---|---|---|
   | stvor-agent | `STVOR_AGENT_VERSION`, `STVOR_AGENT_SHA256_AMD64`, `STVOR_AGENT_SHA256_ARM64` | версия ⇔ сумма amd64; arm64 — необязательна, только при версии |
   | cream | `CREAM_VERSION`, `CREAM_SHA256_AMD64`, `CREAM_SHA256_ARM64` | то же |
   | каталог | `CREAM_EXTENSIONS_VERSION`, `CREAM_EXTENSIONS_SHA256` | версия ⇔ сумма |
   | зависимости | — | cream ⇔ каталог (одно без другого бесполезно); stvor-agent ⇒ cream и каталог; нарушение — отказ старта и `env-preflight` |

   Версии — ровно `X.Y.Z` ASCII (`AGENT_VERSION_PATTERN`), суммы — 64 hex в нижнем регистре. `BISQUITE_AGENT_VERSION`
   и `BISQUITE_AGENT_SHA256` удаляются (в `.env` экземпляра безвредны, `extra="ignore"`; runbook 10 говорит их
   удалить). Источники для `fetch-teleport`: `STVOR_AGENT_SOURCE`, `CREAM_SOURCE`, `CREAM_EXTENSIONS_SOURCE` (база URL
   релиза GitHub или локальный каталог; заменяют `BISQUITE_AGENT_SOURCE`).
   **Доступность:** робот — только когда заданы все три группы; архитектуры робота — те, у которых заданы
   **все** три суммы: `TELEPORT_AGENT_SHA256_<ARCH>`, `CREAM_SHA256_<ARCH>`, `STVOR_AGENT_SHA256_<ARCH>`.
   Рабочая станция — cream и каталог; её архитектуры — `TELEPORT_AGENT_SHA256_<ARCH>` ∧ `CREAM_SHA256_<ARCH>`. Не
   хватает — способ недоступен с прежним текстом `agent_unavailable` (у станции — новым `cream_unavailable`).

2. **Раскладка на диске:** плоско в `teleport/dist/`: `cream_<v>_<arch>.deb`, `cream-extensions-<v>.tar.gz`,
   `stvor-agent_<v>_<arch>.deb`; рядом с каждым — метка `<файл>.sha256` (пин, с которым файл сверен). Кладёт
   `scripts/fetch-teleport-tarball.sh` (`make fetch-teleport`, из `make up`) **только после сверки** с пином; файл на
   диске, чья сумма ≠ пину, перекачивается и сверяется заново, а при неудаче метка удаляется (`WARN`, `make up` идёт
   дальше). Файлы не удаляются (как в ADR-0022 п. 8).

3. **Адреса** (анонимно, без сессии и CSRF; строгие шаблоны без нормализации; `attachment`, `Cache-Control: no-cache`):
   | Шаблон пути под `/dist/teleport/` | Отдаётся, только если | `Content-Type` |
   |---|---|---|
   | `cream_(?P<version>V)_(?P<arch>amd64\|arm64)\.deb` | версия = `CREAM_VERSION`, сумма арх. задана, метка = пину | `application/vnd.debian.binary-package` |
   | `cream-extensions-(?P<version>V)\.tar\.gz` | версия = `CREAM_EXTENSIONS_VERSION`, метка = пину | `application/gzip` |
   | `stvor-agent_(?P<version>V)_(?P<arch>amd64\|arm64)\.deb` | версия = `STVOR_AGENT_VERSION`, сумма арх. задана, метка = пину | `application/vnd.debian.binary-package` |

   `V` = `[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}`. Всё прочее — 404: чужая версия, архитектура без пина, метка ≠ пину,
   сама метка `.sha256`, прежнее имя `bisquite-teleport-agent_<v>_all.deb`. Тарбол Teleport и путь
   `binaries/teleport/<ver>/<тарбол>` (его берёт расширение `teleport` как `TELEPORT_MIRROR`) — без изменений
   (ADR-0015, 0022).

4. **Снимок каталога** — ассет релиза `cream-extensions` (`cream-extensions-<v>.tar.gz`, собранный воспроизводимо
   из тега, плюс `SHA256SUMS`), не автоархив GitHub: пересборка той же версии с другой суммой запрещена (п. 9).

5. **API** (`make gen-api`):
   - `EnrollmentInfoOut.agent_package` → `bootstrap_files: [{key: "cream"|"cream_extensions"|"stvor_agent", arch:
     "amd64"|"arm64"|null, version, filename, url, sha256, size}]` (пусто — группа не задана); `agent_install_command`
     и `agent_offline_install_command` — прежние имена, новое содержимое.
   - Манифест загрузок: ключ `bisquite_agent` → `stvor_agent` (по файлу на архитектуру), новые `cream` (по
     архитектурам) и `cream_extensions` (`arch: null`); у всех `build: null` (версия своя, не версия сборки Teleport,
     ADR-0022 п. 6). `TeleportBuildOut.agent_version` → `stvor_agent_version`, новые `cream_version`,
     `cream_extensions_version`.
   - `LabelTokenOut.bisquite_enable_command` → `enable_command` (`stvor-agent enable URL=…`),
     `bisquite_firstboot_line` → `firstboot_line`; `LabelPullOut.mode`: `bisquite | no_agent | unknown` →
     `agent | no_agent | unknown`, новое поле `agent_version`.
   - `/robot-api/v1/labels` — **без изменений** (запрос, ответы, заголовки, лимиты Traefik).

6. **Скрипт установки** (один на хост, `install_script.py`; встроенный режим «одна команда» и ручной — как сейчас).
   Робот:
   1. проверки (ОС, архитектура из таблицы п. 1, root) и отказы до чтения секрета: пакет `bisquite-teleport-agent`
      (перерегистрация по runbook 10: сначала удалить пакет), пакеты `teleport`/`teleport-ent`, `host_uuid` или
      `/etc/systemd/system/teleport.service` при отсутствии `teleport` в `cream list --json`; отказ про
      `/etc/stvor/labels-token` удаляется;
   2. секрет и токен меток (stdin или встроены);
   3. `cream_<v>_<arch>.deb` с зеркала → `sha256sum -c` копии в закрытом каталоге root → `dpkg -i`;
   4. `cream source add stvor --archive https://<домен>/dist/teleport/cream-extensions-<v>.tar.gz --sha256 <пин>
      --replace --sync`;
   5. `cream install teleport TELEPORT_VERSION=<кластер> TELEPORT_SHA256=<пин арх.>
      TELEPORT_MIRROR=https://<домен>/dist/teleport` (бинарь и юниты; повтор с той же версией пуст, после сбоя —
      повторяет хук сам), затем `printf '%s' "$SECRET" | cream run teleport join TELEPORT_PROXY=…
      TELEPORT_JOIN_METHOD=bound_keypair TELEPORT_TOKEN=<имя> TELEPORT_REGISTRATION_SECRET=- TELEPORT_ENV=…
      TELEPORT_NODENAME=… TELEPORT_LABELS=space=<slug> [TELEPORT_FORCE=1]` — ненулевой код `join` = отказ кластера;
   6. `cream install stvor-agent STVOR_AGENT_VERSION=<v> STVOR_AGENT_SHA256=<пин арх.>
      STVOR_AGENT_MIRROR=https://<домен>/dist/teleport` — ненулевой код = отказ; затем `printf '%s' "$LABEL_TOKEN" |
      stvor-agent enable 'URL=https://<домен>/robot-api/v1/labels'` — ненулевой код = предупреждение (пакет стоит,
      включение не удалось; как прежний отказ `labels-enable`).

   Портал передаёт расширениям **свои** пины явно — `.env` экземпляра остаётся источником правды о версиях на роботе,
   пин внутри каталога — умолчание для образов bisquite. `--offline`: те же шаги, файлы — рядом со скриптом
   (`TELEPORT_TARBALL=…`, `--archive file:///<путь>`, `STVOR_AGENT_DEB=…`, `dpkg -i` локального cream), без `apt-get` и `curl`
   к зеркалу; нужны `curl`, `ca-certificates`, `util-linux` (R3 ADR-0016 остаётся). Скрипт **не пишет** ни
   `teleport.yaml`, ни `/etc/teleport-secret`, ни юнит — ни в одном режиме.
   Рабочая станция — шаги 1–5 с `TELEPORT_OS_LOGINS=0` в `join` (личный компьютер не публикует список логинов — как
   сейчас), без шага 6; `--no-start` → `TELEPORT_DEFER=1` в `join`, команда «Когда будете готовы» → `sudo cream run teleport join-retry`.
   Способ «Файл teleport.yaml вручную» (только станция) остаётся на `render_teleport_yaml`.

7. **Признак агента** (`labels.label_mode`): `agent`, если у узла **командная** метка `stvor_agent` вида `X.Y.Z`
   **или** статическая `managed_by=bisquite-teleport-agent`; иначе `no_agent`; нет снимка — `unknown`. Версия для
   карточки — `stvor_agent`, иначе `bisquite_agent_version`.
   `stvor_agent` — динамическая метка Teleport (`ssh_service.commands`, команда `stvor-agent version --label`, период
   5 мин; её включает расширение `teleport` при установке `stvor-agent`, обновление агента Teleport не перезапускает).
   В `tctl get nodes` она лежит в `spec.cmd_labels.stvor_agent.result`, а не в `metadata.labels`. Сейчас
   `integrations/teleport/parsing.py` уже разбирает `cmd_labels` в `NodeInfo.cmd_labels`, но
   `inventory.robot_observed` кладёт в снимок `effective_labels` (статические ∪ командные ∪ immutable, командные
   поверх статических; первые 32 ключа по алфавиту), и `label_mode` читает этот слитый `observed.labels` — так же
   сейчас читается `os_logins`. Для `stvor_agent` этого мало: распознавание обязано смотреть именно `cmd_labels`
   (снимок несёт их отдельно, например `observed.cmd_labels`): статическая метка `stvor_agent` от оператора
   признаком агента не считается; значение строго `^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$` — до первого исполнения
   команды результат пуст, при сбое команды Teleport пишет в метку текст ошибки, у выключенного агента
   (`stvor-agent disable`) вывод пуст; обрезка до 32 ключей не должна терять метку. Метка появляется не позже
   периода после установки агента. Ручная команда карточки: при `stvor_agent` — `sudo stvor-agent apply env=… space=…`,
   при старой метке — прежняя `sudo bisquite-teleport set-managed …`. Гейты проверяют у узла `space` и командную `stvor_agent` (ожидание — до периода метки).
   Старое распознавание (обе ветки `managed_by`/`bisquite_agent_version` и старая ручная команда) удаляется отдельной
   задачей **после переустановки `stvorhost` с согласия владельца**.

8. **Рабочая станция** (уточнение ADR-0025): cream + `teleport`, без stvor-agent; `stvor-agent` и токен меток для
   станции — 409 `teleport.workstation_unsupported` (как было для агента bisquite).

9. **Жизненный цикл адресов** — как ADR-0016: смена версии — новый адрес, прежний — 404; пересборка той же версии
   запрещена (новая сумма = новая версия); TLS зеркала доверия не несёт — его несёт пин.

**Сверка с ADR-0015/0016/0022 (противоречий нет, есть уточнения):**
| Правило | Как соблюдено |
|---|---|
| 0016 п. 1 «оба пина или ни одного» | обобщено до групп и зависимостей между группами (п. 1) |
| 0016 п. 3 «строгий шаблон, версия в имени, чужое — 404» | три шаблона п. 3; архитектура — в имени, как у тарбола |
| 0016 п. 6 «пересборка той же версии запрещена» | п. 9; поэтому снимок каталога — воспроизводимый ассет, не автоархив (п. 4) |
| 0015 п. 2 «привязка к пину через метку» | метка `<файл>.sha256` у каждого файла (сильнее 0016: раньше зеркало верило имени) |
| 0015 п. 3 «метки и `SHA256SUMS` по HTTP не раздаются» | `.sha256` — 404 |
| 0022 п. 4 «версия в пути — версия экземпляра» | касается сборок Teleport; у cream, каталога и агента своя версия, `build: null` (п. 5) |
| 0015/0022 «`TELEPORT_AGENT_*` — бинарь Teleport» | имена `STVOR_AGENT_*` рядом с `TELEPORT_AGENT_*` — путаница; объяснено в `.env.example`, переименование `TELEPORT_AGENT_*` — вне объёма |

## Обоснование
Один исполнитель (cream) и одно расширение `teleport` на всех хостах — образ bisquite, робот портала, рабочая
станция — убирают из портала второй установщик Teleport и делают поддержку робота одинаковой (`cream show/set/upgrade`,
`stvor-agent status`). Цепочка целостности не ослабевает, а усиливается: каждый файл сверяется с пином при загрузке
на зеркало, зеркало отдаёт только файл с меткой, равной пину, робот сверяет ещё раз до `dpkg -i`/распаковки.
Портал передаёт пины расширениям явно, поэтому каталог может жить своим релизным циклом, не меняя того, что ставится
на роботы портала. Рабочая станция получает повтор регистрации без сети — ровно то, чего не хватало ноутбукам
(ADR-0025: NAT, смена сети, сон), — ценой cream на личном компьютере, который и так управляется от root скриптом
портала. Двойное распознавание агента — дешёвый способ не трогать `stvorhost` до решения владельца.

## Последствия
- **Положительные:** установка Teleport — одна реализация с тестами (расширение `teleport`); портал перестаёт
  рендерить `teleport.yaml` и юнит в скрипте; робот и станция получают `join-retry`, откат, стирание секрета;
  stvor-agent — служба с backoff и `status --json`; зеркало сверяет каждый файл с пином меткой.
- **Отрицательные:** 8 переменных пинов вместо 2 и три источника в `fetch-teleport`; офлайн-набор робота — 5 файлов
  (скрипт, тарбол, cream, снимок, агент), станции — 4; портал зависит от выпусков cream и cream-extensions; файлы
  не-Teleport живут под `/dist/teleport/`; на личный компьютер ставится cream; признак агента в инвентаре
  появляется с задержкой до 5 мин после установки (период командной метки), установка агента — один плавный reload
  (HUP) агента Teleport без разрыва сессий, обновление агента Teleport не трогает; подписи релизов по-прежнему нет; переходного пути нет — `stvorhost` остаётся на 3.0.1 до переустановки.
- **Действия:** срез ниже; ADR-0016 → `Superseded by 0030` (при Accepted); строки-уточнения в ADR-0013 (клиент меток —
  `stvor-agent`, признак `stvor_agent`, `LabelPullOut.mode = agent`), ADR-0015 и 0022 (новые файлы и ключи манифеста),
  ADR-0025 (установка cream + `teleport`, `--no-start` = `TELEPORT_DEFER`); `docs/adr/README.md`; спека и план среза.
  Внешние предусловия (вне репозитория Stvor): cream 1.0.0 (MVP спеки cream, §17), выпуск cream-extensions с
  `teleport` 4.x, обёрткой `stvor-agent` и ассетом-снимком, выпуск stvor-agent 4.0.0. До среза портал по-прежнему
  пинит `bisquite-teleport-agent` 3.1.0 (ADR-0016): выпуск stvor-agent 4.0.0 его зеркало не задевает.

## Связанные ADR / документы
- [Спека stvor-agent](https://github.com/iamletenkov/stvor-agent/blob/main/docs/specs/2026-10-18-stvor-agent-go.md) — агент и расширение `teleport`
- [Спека cream](https://github.com/iamletenkov/cream/blob/main/docs/specs/2026-10-18-cream-core.md) — формат расширения, исполнитель, MVP
- `.scratchpad`-описи 2026-10-18: раздел агента и система расширений (войдут в `docs/research/` спекой среза)
- [ADR-0016](0016-bisquite-agent-package-mirror.md) — заменяется
- [ADR-0013](0013-robot-label-pull.md) — контракт меток (не меняется), клиент меток
- [ADR-0015](0015-teleport-downloads-mirror.md), [ADR-0022](0022-teleport-download-versions.md) — зеркало и манифест
- [ADR-0025](0025-workstation-personal-robot.md) — рабочая станция
- [ADR-0004](0004-teleport-integration.md) — форк Teleport, `bound_keypair`
- [runbook 10](../runbooks/10_robot-enrollment.md) — регистрация роботов

---

## Набросок среза Stvor «stvor-agent и cream»
Вход для спеки и плана среза. **Старт среза** — после внешних выпусков: cream 1.0.0 (MVP спеки cream; допущения C1–C14 спеки stvor-agent сверены с ним),
cream-extensions (`teleport`, обёртка `stvor-agent`, ассет-снимок), stvor-agent 4.0.0. Исполнители: до трёх
параллельно — бэкенд (задачи 1–2), фронт (3), гейты и документы (4–5); задачи 1 и 2 — один владелец
(`settings.py`, `agent_deb.py`, `install_script.py` связаны).

1. **Пины и зеркало.** `RuntimeSettings`: три группы пинов и правила п. 1 (валидаторы, свойства `stvor_agent`,
   `cream`, `cream_extensions` по архитектурам, таблицы архитектур робота и станции); удалить `bisquite_agent_*`;
   `.env.example`, `compose.yaml`, `scripts/env-preflight.sh`; `scripts/fetch-teleport-tarball.sh` — три загрузки по
   архитектурам с метками `.sha256` и `--self-test`; `agent_deb.py` → шаблоны трёх файлов; `TeleportMirror` в
   `main.py` — раздача по меткам; манифест загрузок и схемы API (п. 5), `make gen-api`. Тесты: `test_settings.py`,
   `test_teleport_mirror.py`, `tests/teleport/test_downloads.py`.
2. **Скрипт установки и команды.** `install_script.py` (робот — шаги п. 6, станция — W2, `--offline`, отказы D5/R2/R3
   по-новому), `install_code.py`, `enrollment.py` (join → `cream install teleport` + `cream run teleport join`), `labels.py`
   (`enable_command`, `firstboot_line`, `label_mode` по п. 7 — по `cmd_labels`, `manual_command` по виду метки), `inventory.py` (снимок несёт `cmd_labels` отдельно,
   п. 7), `service.py`, `errors.py` (`cream_unavailable`); тесты `tests/teleport/*` (install_script, enrollment_render/api, install_code_api,
   label_enable, label_token, os_logins, secret) и `tests/resources/*`.
3. **Фронт и i18n.** Способы регистрации (ключи `script_bisquite`/`bisquite_join` → `script_agent`/`image_join`, старые
   ключи в `?method=` открывают способ по умолчанию), тексты «агент bisquite» → «stvor-agent», «образ bisquite» →
   «образ с расширениями cream `teleport` и `stvor-agent`»; `LabelTokenDialog`, `InstallStep`, `RobotLabels` (версия из
   `stvor_agent`, иначе `bisquite_agent_version`), «Загрузки» (cream, снимок, агент по архитектурам), режим `agent`;
   моки, vitest, e2e `robots.spec.ts`, `workstation.spec.ts`, `qa-tour.spec.ts`.
4. **Гейты и стенд.** `robots-gate` фазы 12–14 (контейнер с cream + `teleport` + `stvor-agent`, проверка
   `space` и командной `stvor_agent` с ожиданием периода метки), `arm64-gate`, `workstation-gate` (новый путь станции, `TELEPORT_DEFER`), `stand-seed`
   (переустановка `pve-robot-1`, разбор новой команды join), `smoke-stack.sh`, `check-teleport-pin.sh`,
   `_typos.toml`; живая приёмка: перенос робота с приложением на `pve-robot-1` (повтор приёмки ADR-0013).
5. **Документы.** ADR-0030 (статус по факту), ADR-0016 → Superseded, уточнения в 0013/0015/0022/0025,
   `docs/adr/README.md`; runbook 10 (§1 способы, §3 токен меток и `stvor-agent status`, §6 диагностика —
   `cream run teleport status`, `journalctl -u stvor-agent`, §7 гейты, §10 «stvor-agent и cream» вместо «Пакет агента
   bisquite», обновление вручную `cream upgrade`), `teleport/README.md` («Контракт с cream и stvor-agent»), README
   модуля `teleport`, `backend/README.md`, `frontend/README.md`, `docs/REUSE-MAP.md`, `docs/STATUS.md`, описи — в
   `docs/research/`.
6. **Финальная интеграция + `make ci`** — сквозной прогон после схождения веток, `ci-full`, выкладка на стенд,
   критерий Accepted ADR-0030.
7. **Отложенная (не в срезе; после переустановки `stvorhost` с согласия владельца):** удалить распознавание
   `managed_by=bisquite-teleport-agent` и `bisquite_agent_version`, старую ручную команду и их тесты.
