# bisquite-teleport-agent

Агент Teleport на машине (робот, стенд, сервер): SSH-нода и веб-приложения
без входящих портов — Teleport сам держит обратный туннель к прокси на 443.
Пакет даёт CLI `bisquite-teleport`, юниты systemd и схему настроек; бинарь
`teleport` ставится отдельно.

Что умеет:

- `join` — регистрация одной командой (методы `token` и `bound_keypair`),
  проверка версии кластера до остановки агента, откат к прежней регистрации,
  стирание токена или секрета после регистрации;
- `/etc/teleport.yaml` собирается из `/etc/bisquite/teleport/config` на каждом
  старте службы: правка руками не расходится со `status`;
- веб-приложения на петле, объявленные другими пакетами в
  `/etc/bisquite/teleport/apps.d` (контракт ниже), и приложения оператора;
- динамическая метка `os_logins` — логины ОС машины;
- метки `space` и `env` с портала, реализующего контракт
  [`GET /robot-api/v1/labels`](docs/portal-labels-api.md) (например, Stvor).

Поддерживаются Debian 12 (bookworm), Debian 13 (trixie), Ubuntu 24.04 LTS
и 22.04 LTS, `amd64` и `arm64` (на `amd64` все четыре проверены установкой
пакета и регистрацией ноды в контейнерах); пакет архитектурно
независимый (`_all.deb`: bash, юниты, схема). Проект вырос из расширения
`teleport-agent` репозитория
[cream-extensions](https://github.com/iamletenkov/cream-extensions) (бывший bisquite-extensions);
расширение 3.x — тонкая обёртка, которая ставит этот пакет в образ bisquite.

## Установка

1. **Teleport.** Агенту нужен один бинарь `teleport` в `/usr/local/bin/teleport`
   (`tsh`, `tctl`, `tbot` ноде не нужны). Версия — **не новее кластера** и не
   старше на мажор; для `bound_keypair` — не ниже 18.8.0. Например, из тарбола
   с CDN (или со своего зеркала — со сверкой по закреплённому sha256):

   ```bash
   V=18.10.0; A=$(dpkg --print-architecture)
   curl -fLO https://cdn.teleport.dev/teleport-v$V-linux-$A-bin.tar.gz
   echo "$(curl -fsSL https://cdn.teleport.dev/teleport-v$V-linux-$A-bin.tar.gz.sha256 | cut -d' ' -f1)  teleport-v$V-linux-$A-bin.tar.gz" | sha256sum -c -
   sudo tar -xzf teleport-v$V-linux-$A-bin.tar.gz -C /usr/local/bin --strip-components=1 teleport/teleport
   ```

2. **Пакет агента** — из релиза GitHub (или с зеркала портала), со сверкой
   суммы:

   ```bash
   V=3.0.1
   curl -fLO https://github.com/iamletenkov/stvor-agent/releases/download/v$V/bisquite-teleport-agent_${V}_all.deb
   curl -fLO https://github.com/iamletenkov/stvor-agent/releases/download/v$V/SHA256SUMS
   sha256sum -c SHA256SUMS
   sudo apt install ./bisquite-teleport-agent_${V}_all.deb
   ```

   Зависимости — `bash`, `curl`, `ca-certificates`, `util-linux`, `coreutils`;
   systemd рекомендован (без него агент только пишет настройки).

Установка ничего не регистрирует и агент не запускает: `teleport.service`
включает `join`. Поэтому пакет можно ставить в образ — копии образа не станут
одной нодой.

### Переход с расширения teleport-agent 2.x

Машина, прошитая образом bisquite с расширением `teleport-agent` 2.x,
переходит на пакет установкой `.deb` — без новой регистрации и без
перезапуска агента. `postinst`:

- переносит копии юнитов 2.x из `/etc/systemd/system` (они перекрывали бы
  юниты пакета и вызывали `/usr/local/sbin/bisquite-teleport`) в
  `/var/lib/bisquite/teleport/2.x-units/`; ссылки включения
  (`multi-user.target.wants/teleport.service`, `paths.target.wants/…`,
  `timers.target.wants/…`) переводит на юниты пакета — что было включено,
  остаётся включённым;
- `/usr/local/sbin/bisquite-teleport` становится ссылкой на
  `/usr/sbin/bisquite-teleport` (старые вызовы и уже собранный
  `teleport.yaml` работают);
- схема домена `teleport` (`/opt/bisquite/knobs/teleport{,.apply}`) —
  из пакета; ссылка `/usr/local/sbin/bisquite-conf` образа bisquite не
  меняется;
- **не трогает** `/etc/bisquite/teleport/config` (ни байта, даже умолчания
  не дописывает), `/var/lib/teleport` (регистрация), токен меток,
  `/etc/teleport.yaml` и работающий агент. Следующий старт агента собирает
  `teleport.yaml` уже новым CLI.

Юнит `teleport.service` в `/etc/systemd/system`, написанный не расширением
(например, скриптом установки портала), пакет не трогает и предупреждает: он
перекрывает юнит пакета.

Каталог расширения `/opt/bisquite/teleport-agent` остаётся на диске, но
больше ни на что не указывает.

## Подключение

Одна команда на машине — руками или из первой загрузки (cloud-init
`runcmd`, bisquite `firstboot-commands`):

```bash
echo '<токен>' | sudo bisquite-teleport join TELEPORT_PROXY=teleport.example.org TELEPORT_TOKEN=- TELEPORT_ENV=dev
```

`TELEPORT_TOKEN=-` читает токен со stdin: `echo` встроен в shell и в argv не
попадает, а аргумент `TELEPORT_TOKEN=<токен>` виден в `/proc/<pid>/cmdline`
всё время ожидания регистрации.

**Токен** — `tctl tokens add --type=node,app --ttl=<срок>`. Токен без роли
`app`: SSH-нода регистрируется, а `app_service` бесконечно повторяет «token
does not allow role App» — приложения не публикуются. Добавить роль к готовой
регистрации без нового токена нельзя; `join` и `status` об этом пишут, лечится
новым токеном `node,app` и `join … TELEPORT_FORCE=1`. После регистрации токен
стирается из конфига. Утёкший до истечения срока токен позволяет поднять
чужого агента — ответ на это срок и стирание, а не проверки на машине.

### Подключение по bound_keypair

Для кластера ≥ 18.8 (агент — тоже): токен `bound_keypair` выдаётся на одну
машину, секрет регистрации одноразовый, а после первой регистрации агент
подтверждает себя ключом, который он сам создал и держит в `/var/lib/teleport`.
Утечка секрета после регистрации ничего не даёт; утечка до неё — одна нода.

```bash
echo '<секрет регистрации>' | sudo bisquite-teleport join TELEPORT_PROXY=teleport.example.org \
    TELEPORT_JOIN_METHOD=bound_keypair TELEPORT_TOKEN=<имя токена> TELEPORT_REGISTRATION_SECRET=- TELEPORT_ENV=dev
```

Токен на стороне кластера (`tctl create`):

```yaml
kind: token
version: v2
metadata:
  name: robot-01                 # это TELEPORT_TOKEN
spec:
  roles: [Node, App]             # App — иначе приложения не публикуются
  join_method: bound_keypair
  bound_keypair:
    onboarding:
      registration_secret: "…"   # или пусто — Teleport сгенерирует сам
      must_register_before: "…"  # необязательно: срок первой регистрации
    recovery:
      mode: standard
      limit: 1
```

Секрет — `TELEPORT_REGISTRATION_SECRET=-` (stdin), значением (хуже, виден в
argv) или файлом `TELEPORT_REGISTRATION_SECRET_FILE=<путь>` (файл `join` не
удаляет). Требования — 8–1024 печатных знаков без пробелов.

- Секрет не ручка: в `/etc/bisquite/teleport/config` его нет, `status` пишет
  только «задан». Он лежит в `/var/lib/bisquite/teleport/registration-secret`
  (0600, каталог 0700), `teleport.yaml` ссылается на путь, а не на значение.
- После регистрации (`host_uuid`) секрет стирается, и блок `bound_keypair:`
  уходит из `teleport.yaml`. Имя токена и метод остаются: при новой системной
  роли или ротации CA агент переподключается связанным ключом без секрета.
- Ключ живёт в `/var/lib/teleport`. Повтор `join` при той же регистрации
  ничего не трогает. Стирают ключ только `leave`, `join … TELEPORT_FORCE=1` и
  `join` в другой прокси — после них нужен новый секрет регистрации.
- `join` без `TELEPORT_JOIN_METHOD` — это `token`, даже если машина была
  подключена по `bound_keypair`: метод идёт от команды, а не от истории.

## Команды

```
bisquite-teleport join KEY=VALUE…   зарегистрироваться (первая загрузка, переезд)
bisquite-teleport set KEY=VALUE…    env, метки, приложения — без переподключения
bisquite-teleport leave             агент выключен, регистрация стёрта
bisquite-teleport status            что настроено и что опубликовано
bisquite-teleport labels-enable URL=…    метки с портала: токен со stdin, таймер
bisquite-teleport pull              один тик: забрать метки и применить (зовёт таймер)
bisquite-teleport set-managed env=… space=…   метки портала одним перезапуском
bisquite-teleport join-retry        один тик повтора join, ждущего сети (зовёт таймер)
bisquite-teleport version           версия пакета
```

### join

| Ключ | Обязателен | Что |
|---|---|---|
| `TELEPORT_PROXY` | да | `host[:port]`, без порта — 443 |
| `TELEPORT_JOIN_METHOD` | нет | `token` (по умолчанию) или `bound_keypair` |
| `TELEPORT_TOKEN` | да | `token`: токен или `-` (stdin); `bound_keypair`: имя токена |
| `TELEPORT_REGISTRATION_SECRET` | для `bound_keypair` | секрет регистрации или `-` (stdin); stdin читает только один ключ |
| `TELEPORT_REGISTRATION_SECRET_FILE` | вместо предыдущего | путь к файлу с секретом |
| `TELEPORT_ENV` | фактически да | метка `env`; по ней роли кластера обычно и дают доступ |
| `TELEPORT_CA_PIN` | нет | `sha256:…` из `tctl status`; у прокси с публичным сертификатом TLS проверяется и без него |
| `TELEPORT_NODENAME` | нет | по умолчанию короткое имя хоста |
| `TELEPORT_LABELS` | нет | `site=lab,robot=r1`; ключи `env`, `app`, `managed_by`, `bisquite_agent_version`, `os_logins`, `teleport.icon` — отказ |
| `TELEPORT_FORCE` | нет | `1` — переподключиться к тому же кластеру заново |

Что делает `join`, по порядку:

1. **Уже зарегистрирован в этом же прокси** — ничего не стирает, применяет
   метки и выходит. Повтор первой загрузки (клон носителя, `cloud-init clean`)
   не убивает рабочую ноду просроченным токеном.
2. **Предпроверка до остановки агента:** `https://<прокси>/webapi/find` с
   проверкой TLS, версия агента против `server_version`; для `bound_keypair` —
   агент не ниже 18.8.0 и секрет задан. Если машина была зарегистрирована,
   недоступный прокси — отказ, прежнее подключение цело. Если не была (сети
   ещё нет) — параметры и секрет сохраняются, включаются повторы, код 0 —
   см. [Первая загрузка без сети](#первая-загрузка-без-сети).
3. Прежнее состояние — в `/var/lib/bisquite/teleport/rollback`, чистый
   `/var/lib/teleport` (кешированная CA прежнего кластера иначе даёт
   `no authorities for hostname`), конфиг, старт.
4. Ждёт `host_uuid` до 120 с (`BISQUITE_TELEPORT_JOIN_TIMEOUT`) и читает
   журнал агента. Отказ кластера — код 1, повторов нет. Не дождался, а прежняя
   регистрация была — **возвращает её** и завершается ошибкой; не было —
   агент продолжает попытки, включаются повторы, код 0.

Одновременно идёт один `join`: ручной запуск, строка первой загрузки и тик
повтора берут блокировку `/var/lib/bisquite/teleport/.join.lock` (тик при
занятой — пропускается, `join` ждёт её до 3 минут).

### Первая загрузка без сети

Строка подключения из карточки робота Stvor исполняется в первую загрузку,
когда сети или прокси может ещё не быть:

```bash
printf %s '<секрет>' | bisquite-teleport join TELEPORT_PROXY=… TELEPORT_JOIN_METHOD=bound_keypair \
    TELEPORT_TOKEN=… TELEPORT_REGISTRATION_SECRET=- TELEPORT_ENV=… TELEPORT_NODENAME=… TELEPORT_LABELS=space=…
```

`join` различает «ещё нет» и «нет»:

| Что | Как узнаёт | Что делает | Код |
|---|---|---|---|
| Сеть или прокси недоступны | `https://<прокси>/webapi/find` не ответил: DNS (curl 5/6), соединение (7), таймаут (28), TLS (35, 60 и др. — часы новой машины ещё не сверены), ошибка HTTP балансировщика (22) | сохраняет параметры и секрет, включает повторы, агент не стартует | 0 |
| Прокси отвечает, регистрация не подтвердилась за 120 с, отказа в журнале нет | `host_uuid` не появился | агент работает и пробует сам, повторы включены и следят за ним | 0 |
| Отказ кластера | строка в журнале `teleport.service` с начала попытки (ниже) | агент выключен, секрет стёрт, причина записана, повторов нет | 1 |
| Зарегистрирован | `/var/lib/teleport/host_uuid` | секрет стёрт, `teleport.service` включён, повторы выключены | 0 |

**Как отличить отказ от сети.** Служба join Teleport 18 (`lib/join`) отдаёт
агенту текст отказа, и агент пишет его в журнал на каждой попытке (не реже
раза в 90 с): `Failed to establish connection to cluster. … error:[…]` или
`Can not join the cluster, the token is expired or not found`. Сетевые ошибки
там другие — `connection refused`, `no such host`, `i/o timeout`, TLS — и
отказом не считаются. Отказ — только эти тексты:

| Текст агента | Причина в `status` |
|---|---|
| `a valid registration secret is required` | секрет неверен **или истёк** (`must_register_before`) — кластер нарочно не различает |
| `token expired or not found` | токена нет или он истёк: робот удалён на портале или секрет перевыпущен |
| `no recovery attempts remaining`, `failed to complete challenge`, `invalid challenge document`, `join state verification failed` | токен уже использован: ключ связан с другой машиной (робот подключён) или потерян |
| `does not allow role`, ``is not for `bound_keypair` method``, `ambiguous token name`, `bad backend state` | токен не подходит этой машине |

Неизвестный текст — не отказ: машина продолжает ждать (последняя строка
агента видна в `status`). Ложный «отказ» оставил бы робота без связи до
приезда человека, лишний повтор стоит одного запроса раз в минуту.

**Где что лежит, пока ждёт.** Параметры — в `/etc/bisquite/teleport/config`
(0600), секрет — в `/var/lib/bisquite/teleport/registration-secret` (0600,
каталог 0700), ход повторов — `/var/lib/bisquite/teleport/join-pending`
(0600: `since`, `attempts`, `last`, `error`; секрета там нет).

**Повторы.** `bisquite-teleport-join.service` (oneshot, `After=` и
`Wants=network-online.target`, `ConditionPathExists=…/join-pending`) включён в
`multi-user.target` — запускается в каждую загрузку после сети;
`bisquite-teleport-join.timer` — раз в минуту (`OnCalendar=minutely`, разброс
до 30 с). Тик (`join-retry`): уже зарегистрирован — стирает секрет и
выключает повторы, ничего не перезапуская; прокси недоступен — +1 попытка и
выход 0; отвечает — стартует агента (если он не работает) и ждёт до 120 с
регистрации или отказа. Агент, зарегистрировавшийся сам между тиками,
заканчивает повторы и через `bisquite-teleport-token.path`.

**Отказ.** `/var/lib/bisquite/teleport/join-refused` (0600: `at`, `reason`,
`detail` — строка агента), `teleport.service` выключен (иначе он повторял бы
отказ вечно), секрет стёрт — он либо истрачен, либо неверен.

**Секрет истёк или отказ — что делать.** В карточке робота Stvor выдать
новый секрет («Новый секрет») и выполнить новую строку подключения на
машине — в терминале или через RustDesk (`sudo` перед `bisquite-teleport`,
если не от root). Новый `join` снимает и ожидание, и отказ. Если робот уже
был подключён с другой машины (ключ связан), Stvor добавляет к строке
`TELEPORT_FORCE=1` для нового поколения секрета.

`status`:

```
Регистрация: ждёт сети — попыток 4 с 2026-10-14 09:12:03
  последняя попытка: 2026-10-14 09:15:31 — прокси teleport.example.org:443 недоступен: имя не разрешается — DNS (curl 6)
  повтор: при загрузке и раз в минуту (bisquite-teleport-join.timer: active)

Регистрация: отказ кластера — секрет регистрации неверен или истёк (2026-10-14 09:20:44)
  ответ агента: ERRO Failed to establish connection to cluster. … a valid registration secret is required
  повторов нет: новый секрет — в карточке робота Stvor («Новый секрет»), затем выполните строку подключения снова (вручную или через RustDesk)
```

Без systemd (сборка образа, chroot) `join` только пишет конфиг, как и раньше:
агент стартует при загрузке.

### set

`TELEPORT_ENV`, `TELEPORT_NODENAME`, `TELEPORT_LABELS`, `TELEPORT_APPS`,
`TELEPORT_APPS_DISABLE`, `TELEPORT_APPS_ICONS`, `TELEPORT_APPS_DISCOVERY`,
`TELEPORT_OS_LOGINS`, `TELEPORT_CA_PIN`. Применяет к работающему агенту
через reload (HUP — мягкий перезапуск Teleport, открытые SSH-сессии живут).
`TELEPORT_PROXY`, `TELEPORT_JOIN_METHOD` и `TELEPORT_TOKEN` — только `join`.

`bisquite-teleport set` — то же, что `sudo bisquite-conf set --apply teleport …`:
значения проверяет схема `knobs` (каждое значение ложится в YAML в двойных
кавычках, поэтому шаблоны исключают кавычки, `$` и переводы строки), пишет
библиотека `bisquite-conf`, а хук `knobs.apply` (`bisquite-teleport apply`)
собирает `teleport.yaml` и ставит reload в очередь без ожидания — `set`
бывает и внутри `cloud-final`.

### leave

Выключает агент, таймер меток и повторы join, стирает `/var/lib/teleport`,
`teleport.yaml`, токены, секрет регистрации, токен меток, состояние меток и
ожидание или отказ join. Запись ноды в
кластере сама не удаляется — у ноды нет на это прав; она исчезнет по
истечении heartbeat.

## Метки с портала

Портал — источник правды о том, в каком пространстве машина. Он управляет
ровно двумя метками: `space` (один элемент `TELEPORT_LABELS`) и `env`
(`TELEPORT_ENV`); прочие метки оператора не трогаются. Контракт, формат
ответа и поведение агента — [docs/portal-labels-api.md](docs/portal-labels-api.md).

```bash
read -rs T && printf '%s' "$T" | sudo bisquite-teleport labels-enable URL=https://<портал>/robot-api/v1/labels; unset T
```

Из первой загрузки — **второй** командой после `join`, со своим `|| true`:
отказ портала не должен ронять регистрацию.

`labels-enable`, `set-managed` и `pull` идут под одной блокировкой
(`flock` на `/var/lib/bisquite/teleport/.labels.lock`).

**Перезапуск, а не reload.** Смена `space`/`env` — `systemctl restart
teleport.service` с подтверждением нового запуска (`InvocationID`,
`MainPID` или `ActiveEnterTimestampMonotonic`). HUP оставил бы открытыми
SSH-сессии людей прежнего пространства, а приложения уже проверялись бы по
новым меткам. Перезапуск рвёт открытые сессии, машина недоступна 5–10 секунд,
узел и приложения переезжают одновременно.

## Метка os_logins

Нода публикует динамическую метку `os_logins` — список логинов ОС, под
которыми на машину вообще можно войти (`os_logins=robot,root`). **Метка —
справка, а не право:** кто и под каким логином входит, решают роли Teleport.

Считает её `bisquite-teleport os-logins`, агент запускает команду сам раз в
5 минут (`ssh_service.commands`, `period: 5m0s`):

- источник — NSS (`getent passwd`); `root` и учётки с uid ≥ 1000, кроме
  `nobody`; shell не `nologin`, `false`, `true`, `sync`, `shutdown`, `halt`;
- имя вне алфавита метки (`[A-Za-z0-9._-]`, до 32 знаков) пропускается, а не
  искажается;
- дубли убраны, порядок — `LC_ALL=C`; длиннее 255 знаков — обрезается по
  целому имени и заканчивается `,...`;
- код всегда 0: на ненулевом коде Teleport кладёт в метку текст ошибки.

Выключить — `sudo bisquite-teleport set TELEPORT_OS_LOGINS=0`. Задать
`os_logins` через `TELEPORT_LABELS` нельзя: динамическая метка побеждает
статическую, и значение оператора молча терялось бы.

## Приложения

| Откуда | Кто пишет | Адрес |
|---|---|---|
| `/etc/bisquite/teleport/apps.d/<имя>.conf` | другие пакеты и расширения | только петля |
| `TELEPORT_APPS=имя=порт,имя=URI` | оператор | любой; `порт` — `http://127.0.0.1:<порт>` |
| `TELEPORT_APPS_DISABLE=имя,…` | оператор | не публиковать |
| `TELEPORT_APPS_ICONS=имя=иконка,…` | оператор | иконка в веб-морде |

Одно имя в обоих — побеждает `TELEPORT_APPS`. `TELEPORT_APPS_DISCOVERY=0`
игнорирует `apps.d` целиком. Приложение публикуется как `<имя>-<нода>` с
адресом `<имя>.<нода>.<хост прокси>`.

### Контракт apps.d

Публичный интерфейс агента для других пакетов (так объявляют себя
`code-server` и `selkies` из bisquite-extensions). Файл
`/etc/bisquite/teleport/apps.d/<имя>.conf`:

```
NAME=code-server
URI=https://127.0.0.1:9002
ICON=laptop
```

Два обязательных ключа и один необязательный. Файл пропускается целиком
(с предупреждением в журнале), если:

- владелец не root, права шире `0644` или это символическая ссылка;
- есть другой ключ или строка не `KEY=VALUE` — разбор без `source`;
- `NAME` не метка DNS (`[a-z0-9-]`, из него строится адрес);
- `URI` не `http(s)://127.0.0.1|localhost|[::1][:порт][/путь]`;
- `ICON` не `[A-Za-z0-9._-]`;
- имя уже объявлено другим файлом (побеждает первый по алфавиту).

**Метку доступа объявление задать не может.** Приложение получает метки ноды
плюс `app=<NAME>`; кому оно видно, решает `TELEPORT_ENV` оператора. Файл в
`apps.d` не открывает приложение чужому `env`. Единственная метка, которую
задаёт объявление, — `teleport.icon` (иконка из набора, вшитого в прокси;
опечатка молчит — прокси угадывает иконку по имени).

Изменение в `apps.d` подхватывает `bisquite-teleport-apps.path` — reload
агента без участия человека. https на петле публикуется с
`insecure_skip_verify`; приложению на петле переписывается заголовок `Host`
на публичный адрес (иначе приложения, сверяющие Origin WebSocket с `Host`,
отвечают 403).

## teleport.yaml

`/etc/teleport.yaml` генерируется на каждом старте службы из
`/etc/bisquite/teleport` и руками не правится. Перед генерацией `render`
сверяет весь файл ручек со схемой: правка руками с кавычкой или `$` — отказ
старта, а не испорченный YAML.

- `ssh_service.listen_addr: 127.0.0.1:3022` — наружу порт не нужен;
- метки ноды: `TELEPORT_LABELS`, `managed_by=bisquite-teleport-agent`,
  `bisquite_agent_version=<версия пакета>`, `env`;
- `join_params` — пока есть токен (`token`) или секрет (`bound_keypair`);
- `commands` с `os_logins` — `["/usr/sbin/bisquite-teleport", "os-logins"]`.

## Файлы

| Путь | Права | Что |
|---|---|---|
| `/usr/local/bin/teleport` | 0755 | агент Teleport — ставится отдельно |
| `/usr/sbin/bisquite-teleport` | ссылка | CLI → `/usr/lib/bisquite-teleport/bisquite-teleport` |
| `/usr/lib/bisquite-teleport/` | | CLI, схема `knobs`, хук `knobs.apply`, `lib/bisquite-conf`, `pkg-setup` |
| `/usr/lib/systemd/system/` | | `teleport.service`, `bisquite-teleport-{apps,token}.{path,service}`, `bisquite-teleport-labels.{service,timer}`, `bisquite-teleport-join.{service,timer}` |
| `/opt/bisquite/knobs/teleport{,.apply}` | ссылки | схема и хук — там их ищет `bisquite-conf` |
| `/etc/bisquite/teleport/config` | 0600 | ручки (`bisquite-conf show teleport`) |
| `/etc/bisquite/teleport/apps.d/` | 0755 | объявления приложений |
| `/etc/bisquite/teleport/labels-token` | 0600 | `url=…` и `token=…` меток портала |
| `/etc/teleport.yaml` | 0600 | генерируется |
| `/var/lib/teleport/` | 0750 | регистрация агента; у `bound_keypair` — и связанный ключ |
| `/var/lib/bisquite/teleport/` | 0700 | `rollback`, секрет регистрации до регистрации, `join-pending` / `join-refused` (0600), состояние меток, `2.x-units/` |

Юниты: `teleport.service` включает `join` после регистрации; path-юниты
включены всегда (до регистрации ничего не делают); таймер меток включает
`labels-enable`; `bisquite-teleport-join.{service,timer}` включает `join`,
ждущий сети, и выключает первая успешная регистрация или отказ.

## Удаление

`apt remove` останавливает таймеры меток и повтора join и path-юниты, но не агент: удаление
не должно рвать сессию, из которой его запустили. Ни `remove`, ни `purge`
**не стирают регистрацию**: `/var/lib/teleport`, `/etc/bisquite/teleport` и
`/etc/teleport.yaml` остаются. Отключить машину — `sudo bisquite-teleport
leave` **до** удаления пакета.

## Диагностика

```bash
sudo bisquite-teleport status
journalctl -u teleport -f
journalctl -u bisquite-teleport-labels   # тики меток: ни токена, ни тела ответа там нет
journalctl -u bisquite-teleport-join     # тики повтора join: попытка, причина ожидания или отказа
```

## Разработка

```bash
make check     # shellcheck (если установлен) и все tests/test-*.sh
make deb       # dist/bisquite-teleport-agent_<версия>_all.deb и dist/SHA256SUMS
```

Тесты идут на подменённом корне (`BISQUITE_TELEPORT_ROOT`) с поддельными
`systemctl`, `journalctl`, `curl`, `dpkg` и `teleport`: root, systemd и сеть
не нужны. `tests/test-join.sh` — первая загрузка без сети: разбор ошибок,
файлы 0600, стирание секрета, тик повтора, идемпотентность, блокировка.
Сборка `.deb` — `dpkg-deb` без debhelper, воспроизводимая
(`SOURCE_DATE_EPOCH` — время последнего коммита).

`lib/bisquite-conf` — копия библиотеки настроек bisquite-extensions; правки
вносятся там и переносятся сюда той же версией.

Уязвимости — [SECURITY.md](SECURITY.md). Изменения — [CHANGELOG.md](CHANGELOG.md).

### Как выпустить версию

Версию и `CHANGELOG.md` ведёт [release-please](https://github.com/googleapis/release-please)
(`.github/release_please/`), workflow `CI and Release`:

1. Сливаешь в `main` коммиты по [Conventional Commits](https://www.conventionalcommits.org/ru/):
   `fix:` поднимает patch, `feat:` — minor, `!` или `BREAKING CHANGE:` — major;
   `docs:`, `ci:`, `chore:` версию не трогают.
   Тип и область — латиницей, текст заголовка и тела — на русском
   (`feat(rustdesk): пакет для arm64`); подробнее — [CLAUDE.md](CLAUDE.md).
2. После проверок на `main` release-please открывает (и дальше обновляет)
   PR «chore(main): release X.Y.Z» — он поднимает `VERSION` и дописывает
   секцию в `CHANGELOG.md`.
3. Владелец сливает этот PR. release-please создаёт тег `vX.Y.Z` и
   опубликованный релиз с заметками из секции, затем job
   `release-artifacts` собирает из тега `.deb` и `SHA256SUMS` и прикладывает
   их к релизу.
4. После файлов job `dispatch-extensions` запускает в `iamletenkov/cream-extensions`
   workflow `agent-bump.yml`: тот открывает PR с пином агента в расширении
   `teleport-agent` (версия и sha256 из `SHA256SUMS`), слияние выпускает
   расширения, а их релиз сам поднимает их в `bisquite`. Токен — секрет
   `EXTENSIONS_DISPATCH_TOKEN` этого репозитория: fine-grained PAT только на
   cream-extensions с правом Actions: Read and write, действует до 16.10.2027 —
   до этой даты перевыпустить, иначе job релиза покраснеет.

Теги руками не ставят. Если сборка файлов упала, релиз уже виден без них:
«Re-run failed jobs» в том же прогоне или ручной запуск с `release_tag`:

```bash
gh workflow run ci-release.yml --ref main -f release_tag=vX.Y.Z
```

Такой запуск после файлов тоже дёргает `agent-bump.yml`: он идемпотентен — пин уже
на версии, PR на неё открыт или релиз старше пина — нового PR не будет.

## Лицензия

[PolyForm Noncommercial 1.0.0](LICENSE), см. [NOTICE](NOTICE). Коммерческое
использование — по отдельной лицензии правообладателя.
