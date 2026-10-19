# stvor-agent на Go и расширение `teleport` каталога cream

> 2026-10-18. Ляжет в репозиторий `stvor-agent` как `docs/specs/2026-10-18-stvor-agent-go.md`. Архитектурная задача:
> новая реализация агента (bash → Go, таймер → служба), новая граница между агентом и Teleport (расширение `teleport`
> каталога `cream-extensions`), новая упаковка (`.deb` по архитектурам). Затрагивает репозитории `stvor-agent`
> (переписывается целиком), `cream-extensions` (новые расширения `teleport` и `stvor-agent`, удаление `teleport-agent`,
> путь `apps.d` у `code-server` и `selkies`), `cream` (требования к контракту — §12) и Stvor (ADR-0030 и срез — отдельные
> документы портала). Входы: опись `inventory-agent-split.md` и `inventory-extension-system.md` (2026-10-18), код
> `stvor-agent` 3.1.0 (`0aeb84e`), обёртка `cream-extensions/extensions/debian/teleport-agent` 3.1.0, Stvor `4ffcfc16`
> (`backend/app/modules/teleport/*`, ADR-0013, 0015, 0016, 0022, 0025, runbook 10).
> Контракт исполнителя — спека [cream: формат расширения и исполнитель](https://github.com/iamletenkov/cream/blob/main/docs/specs/2026-10-18-cream-core.md) (далее «спека cream»);
> допущения §12 сверены с ней 2026-10-18 и заменены ссылками на её разделы. Решение портала — ADR-0030 Stvor.

## 1. Проблема
`bisquite-teleport-agent` 3.1.0 — 1623 строки bash в одном CLI, который делает две несвязанные вещи:
- **регистрирует узел в Teleport и держит его конфиг** (join `token`/`bound_keypair` с откатом, повтор join без сети,
  render `teleport.yaml`, `apps.d`, динамическая метка `os_logins`, `leave`, стирание токена) — это общая задача
  любого робота, портал для неё не нужен;
- **представляет портал Stvor на машине** (раз в минуту забирает ожидаемые метки `env`/`space` с
  `/robot-api/v1/labels`, применяет их одним перезапуском агента Teleport, отчитывается `X-Applied-Revision`).

Из-за смешения: портал зашит в ручки Teleport (`TELEPORT_LABELS_URL`), тик меток делает read-modify-write строки
оператора `TELEPORT_LABELS` (гонка с `bisquite-conf set` и откат правок оператора), `status` смешивает две зоны,
библиотека ручек вендорится копией (третий потребитель `bisquite-conf`), а единственный `_all.deb` держит bash-код,
который трудно тестировать (~410 проверок на подменённых `curl`/`systemctl`). Экосистема переходит на cream
(исполнитель расширений на Go) — у Teleport-логики появляется естественный дом, у агента портала — шанс стать
маленькой проверяемой службой.

## 2. Цель и не-цели
**Цель.**
1. `stvor-agent` — статический Go-бинарь и долгоживущая служба systemd, единственная задача которой — привести
   машину к желаемому состоянию, которое объявляет портал Stvor. В этой версии желаемое состояние — только метки
   `env` и `space` по неизменному контракту `/robot-api/v1/labels` v1. Признак и версию агента публикует
   динамическая метка Teleport `stvor_agent=<версия>` (§4.4), а не управляемая метка.
2. Вся Teleport-логика — в расширении каталога `teleport` (cream), которое ничего не знает о портале и годится
   любому «внешнему управляющему» через понятие **управляемых меток**.
3. Поведение, на которое опирается Stvor (ADR-0013: сверка состояния, а не ревизии; один restart с подтверждением;
   узел и все приложения вместе; ошибки сети — одна строка журнала; 3×/15 мин; токен не в argv/журнале), сохраняется
   и доказывается Go-тестами, перенесёнными из `tests/test-labels.sh` по таблице соответствия.

**Не-цели.**
- Канал обновлений ПО (Mender и т. п.), новые эндпоинты портала, новые виды желаемого состояния. Служба
  проектируется так, чтобы их можно было добавить (§5.9), но в этой версии их нет.
- Изменение контракта `/robot-api/v1/labels` (запрос, ответы, заголовки, лимиты Traefik) — **не меняется**.
- Переходная совместимость: переходных `.deb`, переноса путей `/etc/bisquite/*`, миграции с 3.x нет — реальных машин
  с агентом нет; тестовая ВМ `pve-robot-1` переустанавливается; `stvorhost` (агент 3.0.1) не трогаем, он продолжает
  работать по неизменному HTTP-контракту.
- Сам cream: у него своя спека (ссылка в шапке); здесь — только сверка того, на что опирается этот документ (§12).
- Подпись релизов: как в ADR-0016 — только SHA-256 по пину (подпись — отдельная задача экосистемы).

## 3. Решения владельца и рулинги (вход, не пересматриваются)
| # | Решение | Источник |
|---|---|---|
| В1 | Экосистема: cream (исполнитель), cream-extensions (каталог), stvor-agent, bisquite (сборка образов), Stvor (портал) | владелец 2026-10-18 |
| В2 | stvor-agent — Go, статический бинарь, `.deb` по архитектуре (amd64, arm64), долгоживущая служба вместо таймера; лицензия PolyForm NC; репозиторий уже `stvor-agent` | владелец |
| В3 | Teleport-логика — расширение каталога `teleport`; метки — вариант 1 описи: ручку `TELEPORT_MANAGED_LABELS` пишет только расширение, действия `set-managed` (restart с подтверждением, pending-маркер) и `managed-status` (JSON) | владелец |
| В4 | В каталоге — тонкая обёртка `stvor-agent` с пином версии и sha256 `.deb` по архитектурам | владелец |
| В5 | Без переходной совместимости и переходных `.deb`; `pve-robot-1` переустанавливается; `stvorhost` не трогать | владелец |
| В6 | Контракт `/robot-api/v1/labels` со стороны Stvor не меняется | владелец |
| В7 | Канал обновлений — вне рамок; агент расширяем, новых каналов нет | владелец |
| Р1 | Stvor считает «агент есть» по метке `stvor_agent` **или** по старой `managed_by=bisquite-teleport-agent`; старое распознавание удаляется отдельной задачей после переустановки `stvorhost` | контроллер |
| Р3 | `stvor_agent` — **динамическая** метка Teleport (`ssh_service.commands`, команда агента, период как у `os_logins`), а не управляемая; включается расширением `teleport` один раз при установке `stvor-agent`; обновление агента Teleport не перезапускает. Stvor распознаёт её в `cmd_labels` узла | контроллер 2026-10-18 |
| Р2 | Бутстрап Stvor: зеркало раздаёт `cream_<v>_<arch>.deb`, `cream-extensions-<v>.tar.gz`, `stvor-agent_<v>_<arch>.deb`, тарбол Teleport; скрипт: cream → источник-архив → `cream install teleport` (секрет — stdin) → `cream install stvor-agent` (токен меток — stdin); офлайн — тот же набор файлов | контроллер |

## 4. Функциональная карта: что было → куда ушло
Легенда: **T** — расширение `teleport` (bash, каталог cream-extensions), **S** — `stvor-agent` (Go),
**C** — cream, **✗** — удаляется.

### 4.1 Команды
| Было (`bisquite-teleport …`) | Стало | Комментарий |
|---|---|---|
| `join KEY=…` (token/bound_keypair, `/webapi/find`, откат, 120 с ожидания `host_uuid`, разбор отказов, `join-pending`, `.join.lock`) | **T** `cream run teleport join KEY=…` (регистрация — только действием; `cream install teleport` ставит бинарь и юниты, §10.4) | переносится как есть, меняются пути и имена |
| `join-retry` | **T** `cream run teleport join-retry` (тик `teleport-join.timer`; он же — ручной «зарегистрировать сейчас») | как есть |
| `scrub-token` | **T** (внутреннее, `teleport-token.service`) | как есть, `RemainAfterExit=yes` сохраняется |
| `render`, `apply` (хук ручек) | **T** действие `render` (`lock: none`; зовут `ExecStartPre`/`ExecReload` `teleport.service`) и хук `apply` | render учится управляемым и командным меткам (§10.3) |
| `set KEY=…` | **C** `cream set teleport KEY=…` | обёртка не нужна; правило запретных ключей — в схеме/хуке T |
| `status` | **T** `cream run teleport status` (текст: регистрация, приложения, ручки, управляемые и командные метки; машинное — `managed-status`) + **S** `stvor-agent status [--json]` (портал, токен, ожидаемые/применённые метки) | одна зона — одна команда |
| `leave` | **T** `cream run teleport leave` (регистрация, конфиг, управляемые метки, pending) + **S** `stvor-agent disable` (токен, состояние) | T больше не знает файлов S |
| `os-logins` | **T** файл `files/os-logins`, его зовёт сам Teleport (`ssh_service.commands`) | метка — справка о машине, а не решение портала |
| `set-managed env=… space=…` | механизм — **T** `cream run teleport set-managed k=v…` (вывод — всегда JSON); политика (когда звать, пауза 3×/15 мин) и ручной путь — **S** `stvor-agent apply env=… space=…` | В3 |
| `pull` | **S** — тик службы `stvor-agent run`; разово — `stvor-agent sync` | таймер ✗ |
| `labels-enable URL=…` | **S** `stvor-agent enable URL=…` (токен со stdin) | токен и адрес — секрет S, не ручки T |
| `version`, `help` | у каждого свои | |

### 4.2 Юниты systemd
| Было | Стало |
|---|---|
| `teleport.service` (`ExecStartPre=render`, `ExecReload=render`+HUP) | **T** `teleport.service` |
| `bisquite-teleport-apps.{path,service}` | **T** `teleport-apps.{path,service}` |
| `bisquite-teleport-token.{path,service}` | **T** `teleport-token.{path,service}` |
| `bisquite-teleport-join.{service,timer}` | **T** `teleport-join.{service,timer}` |
| `bisquite-teleport-labels.{service,timer}` | **S** `stvor-agent.service` (долгоживущая, `Restart=always`) |

### 4.3 Файлы
| Было | Стало |
|---|---|
| `/usr/sbin/bisquite-teleport`, `/usr/lib/bisquite-teleport/*` | **T** `/opt/cream/ext/teleport/`: `hooks/*`, `actions/*` (тонкие входы) и общая библиотека `files/teleport.sh` (§10.2) |
| `lib/bisquite-conf` (копия), `/opt/bisquite/knobs/teleport{,.apply}` | **C**: ручки приходят хукам и действиям окружением, секреты — `cream config get --secret`, запись — `cream set --allow-readonly` (спека cream §6.3, §8.3); схема — `config.keys` манифеста |
| `/etc/bisquite/teleport/config` | **T/C** `/etc/cream/teleport/config` |
| `/etc/bisquite/teleport/apps.d/` (контракт для `code-server`, `selkies`) | **T** `/etc/cream/teleport/apps.d/`; `teleport-app.sh` в `code-server` и `selkies` правится тем же релизом каталога |
| `/etc/teleport.yaml`, `/var/lib/teleport/`, `/usr/local/bin/teleport` | **T**, пути Teleport без изменений |
| `/var/lib/bisquite/teleport/{rollback,registration-secret,join-pending,join-refused,.join.lock}` | **T** `/var/lib/cream/ext/teleport/…` (`CREAM_STATE_DIR`); `.join.lock` ✗ — блокировка домена cream (§10.6) |
| `/var/lib/bisquite/teleport/labels-restart-pending` | **T** `/var/lib/cream/ext/teleport/managed-pending` (маркер живёт у того, кто пишет и рестартует) |
| `/etc/bisquite/teleport/labels-token` (`url=`, `token=`) | **S** `/etc/stvor-agent/token` (тот же формат, 0600 root, каталог 0700) |
| ручка `TELEPORT_LABELS_URL` | ✗ (адрес живёт рядом с токеном) |
| `labels-expected`, `labels-applied`, `labels-restart-attempts`, `.labels.lock`, `.pull.*` | **S** `/var/lib/stvor-agent/state.json` + `/run/stvor-agent.lock` |
| `2.x-units/`, ссылка `/usr/local/sbin/bisquite-teleport`, перенос 2.x в `pkg-setup` | ✗ |

### 4.4 Статические метки узла
| Было | Стало |
|---|---|
| `managed_by=bisquite-teleport-agent` (ставил render) | ✗ в T (расширение не говорит от имени Stvor). Признак агента — динамическая метка `stvor_agent=<версия>` (Р1, Р3; ниже) |
| `bisquite_agent_version=<X.Y.Z>` | ✗, заменяет `stvor_agent` |
| — | **`stvor_agent`** (динамическая, только узел): Teleport раз в 5 мин исполняет `/usr/bin/stvor-agent version --label` и публикует вывод. Включает её обёртка `stvor-agent` drop-in-файлом командной метки расширения `teleport` (§10.3, §11) |
| `env` (`TELEPORT_ENV`), `space` (элемент `TELEPORT_LABELS`) | оператор задаёт их ручками T как раньше; S задаёт управляемые `env`, `space`, которые **перекрывают** операторские (§10.3) |
| `os_logins` (динамическая) | **T**, без изменений |

**Почему `stvor_agent` динамическая (Р3).** Управляемая метка с версией меняла бы набор `TELEPORT_MANAGED_LABELS` при
каждом обновлении агента, а смена управляемых меток — это restart агента Teleport с разрывом SSH-сессий (§10.5).
Динамическая метка читает версию в момент исполнения команды, поэтому:
- обновление `stvor-agent` (новый `.deb`) не трогает `teleport.yaml` и Teleport не перезапускает: метка сменится сама
  в течение периода (5 мин, как у `os_logins`);
- первый тик после регистрации ничего не перезапускает: S задаёт управляемые `env`, `space`, которые `join` уже
  записал ручками оператора (`TELEPORT_ENV`, `space` в `TELEPORT_LABELS`); итоговый `teleport.yaml` не меняется, и
  `set-managed` записывает ручку без restart (правило «restart только при изменении итогового конфига», §10.5);
- включение метки — разовое появление drop-in-файла `/etc/cream/teleport/command-labels.d/stvor_agent` при установке
  обёртки `stvor-agent`. Path-юнит `teleport-apps.path` (он уже следит за `apps.d`) видит файл и делает
  `systemctl reload teleport.service` — `ExecReload` = render + HUP. HUP у Teleport — плавный перезапуск: новый процесс
  читает конфиг, старый дорабатывает открытые сессии; restart и разрыва нет. В образе bisquite и при установке до
  старта Teleport файл просто попадает в первый render.

Неустранимого перезапуска не остаётся. `version --label` печатает версию, только если агент включён (есть файл
токена, §8), иначе пустую строку: `stvor-agent disable` снимает признак без вызова T (как прежде снимал метку).

## 5. Архитектура службы stvor-agent
### 5.1 Состав
Один бинарь `/usr/bin/stvor-agent`, только стандартная библиотека Go (`net/http`, `crypto/tls`, `encoding/json`,
`log/slog`, `os/exec`, `os/signal`, `syscall` для `flock`) — внешних зависимостей нет. `CGO_ENABLED=0`, linux/amd64 и
linux/arm64. Пакеты:

```
cmd/stvor-agent/      — точка входа: run(args, stdin, stdout, stderr) int
internal/cli/         — разбор подкоманд и KEY=VALUE, коды выхода
internal/portal/      — клиент /robot-api/v1/labels: запрос, строгий разбор тела, классификация ответа
internal/token/       — файл токена: чтение с проверкой прав и владельца, атомарная запись
internal/state/       — state.json: схема, строгая загрузка, атомарная запись
internal/teleport/    — интерфейс Teleport {Status, SetManaged} и реализация через `cream run teleport …`
internal/reconcile/   — один тик: чистая логика решения (часы и Teleport — интерфейсы)
internal/daemon/      — цикл, джиттер, backoff, сигналы, блокировка
internal/version/     — версия (-ldflags -X)
```

### 5.2 Цикл
- `stvor-agent run` (ExecStart службы) делает первый тик через случайные 0–20 с после старта, дальше — бесконечный
  цикл «тик → сон». Сон = `интервал + U[0, 20 с)` (эквивалент `RandomizedDelaySec=20s` прежнего таймера).
- **Интервал** — ручка `STVOR_AGENT_INTERVAL` (секунды, 60 по умолчанию, допустимо 60–600; меньше 60 запрещено
  схемой: Traefik ограничивает 6 запросов в минуту на значение `Authorization`, §6 описи).
- **Backoff.** Подряд идущие неудачи `n` (сеть, TLS, тайм-аут, 3xx, 429, 5xx, прочие коды, тело не по контракту):
  сон = `min(интервал · 2^min(n,3), 600 с) + джиттер`; успех (200/304) сбрасывает `n`. 401 — сон 600 с (портал
  отозвал токен; ускоряет только `stvor-agent enable`, которое перезапускает службу). Раньше тика служба не
  ретраит никогда — это и есть требование ADR-0013 «на 429 — ждать тик».
- **Сигналы:** `SIGHUP` — перечитать токен и тик сейчас; `SIGTERM`/`SIGINT` — дождаться конца текущего тика (дочерний
  `cream run` не убивается посреди restart агента Teleport, §5.8) и выйти 0.
- **Одна операция за раз:** тик службы, `sync`, `apply`, `enable`, `disable` берут `flock` на `/run/stvor-agent.lock`.
  Служба — неблокирующе (занято — тик пропущен одной строкой журнала, как `labels_lock nowait`); CLI — ждёт до 120 с.

### 5.3 Один тик (`internal/reconcile`)
```
tick(manual):
  tok  := token.Read()                       // нет файла → "не включён", конец
                                              // права/владелец/формат не те → ошибка, конец (код 1 у CLI)
  st   := state.Load()                        // битый файл → пустое состояние + строка журнала
  if st.URL != tok.URL { st = state.New(tok.URL) }   // другой портал: прежний ответ ничего не значит
  ts, tsErr := teleport.Status()             // cream run teleport managed-status
  want(exp) := {env: exp.env, space: exp.space}
  report := st.Expected != nil && st.Applied == st.Expected.Rev && tsErr == nil && inEffect(ts, want(st.Expected))
  resp := portal.Get(tok, ifNoneMatch = st.Expected?.Rev, xApplied = report ? st.Expected.Rev : "")
  switch resp.Kind:
    network|tls|redirect|429|5xx|other  → строка журнала, backoff, конец (ничего не тронуто)
    401                                  → строка журнала, сон 600 с, конец
    bad_body                             → строка журнала (без тела), backoff, конец
    304: if st.Expected == nil → строка «304 без запомненных меток», конец
    200: st.Expected = resp.Labels; state.Save(st)
  w := want(st.Expected)
  if tsErr != nil → строка журнала, конец                // не знаем состояние — не трогаем
  if !inEffect(ts, w):
     if !manual && st.Attempts.Count >= 3 && ts.Active && now < st.Attempts.Last + 15 мин:
        строка «перезапуск пропущен: N неподтверждённых подряд, следующая попытка через …», конец
     r := teleport.SetManaged(w)              // cream run --lock-timeout 20s teleport set-managed env=… space=…
     switch r.Result:
       applied|unchanged → st.Attempts = 0
       deferred          → (узел не зарегистрирован) попытки не считаются
       unconfirmed       → st.Attempts = {Count+1, now}
     ts = teleport.Status()
  if inEffect(ts, w) && st.Applied != st.Expected.Rev:
     st.Applied = st.Expected.Rev            // портал узнает на следующем тике (X-Applied-Revision)
  state.Save(st)

inEffect(ts, w) := ts.Managed == w && !ts.Pending && ts.Joined && ts.Active
```
Свойства, которые тик обязан сохранить (ADR-0013, R3 прежнего агента):
- сверяется **состояние**, а не ревизия: на каждом тике, и на 200, и на 304, — ручная правка `TELEPORT_MANAGED_LABELS`
  невозможна (ro), но pending, неактивный агент или чужой restart ловятся каждым тиком;
- `X-Applied-Revision` уходит **только** вместе с `If-None-Match` той же ревизии и только если метки действуют
  (`inEffect`) и ревизия уже отмечена применённой;
- ошибка сети/HTTP — ровно одна строка журнала, состояние T не трогается;
- человек (`stvor-agent apply`, `sync`) паузой 3×/15 мин не ограничивается; неактивный агент Teleport тоже не
  ставится на паузу (сессий нет — рвать нечего).
- первая запись после регистрации restart не вызывает: `join` от Stvor ставит `env` и `space` ручками T, первый тик
  S переносит те же значения в управляемые метки, итоговый конфиг не меняется (§4.4, §10.5);
- обновление S restart не вызывает: версию агента публикует динамическая метка `stvor_agent` (Р3, §4.4), в
  управляемые метки S её не пишет. Restart агента Teleport остаётся только там, где портал **меняет** `env`/`space`
  (перенос робота, ADR-0013) — ради этого он и нужен.

### 5.4 HTTP-клиент (`internal/portal`)
- `GET <url>`; заголовки: `Authorization: Bearer <токен>`, при запомненной ревизии `If-None-Match: "<rev>"`, при
  `report` — `X-Applied-Revision: <rev>`, `User-Agent: stvor-agent/<версия>` (портал его не читает; контракт не
  меняется). `HEAD` агент не использует.
- Транспорт: только `https`; TLS ≥ 1.2, системные корни (`/etc/ssl/certs`); прокси — `http.ProxyFromEnvironment`
  (по умолчанию в службе окружения прокси нет; задаётся drop-in `Environment=HTTPS_PROXY=` по runbook);
  `CheckRedirect` возвращает ошибку — редиректам не следует, 3xx = неудача; соединение 10 с, весь запрос 30 с.
- Тело читается через `io.LimitReader(4097)`: больше 4096 байт — `bad_body`.
- **Строгий разбор тела 200.** `encoding/json` с `Unmarshal` в структуру **запрещён**: он принимает повторы ключей
  (последний побеждает) и сопоставляет ключи без учёта регистра (`"ENV"` попал бы в `env`). Разбор — по токенам
  `json.Decoder.Token()`: ровно объект `{"labels":{"env":S,"space":S},"revision":S}`, ключи в этом порядке, каждый
  один раз, без лишних ключей и без хвоста после объекта; валидный UTF-8; NUL в теле — отказ целиком (прежний урок:
  NUL склеивал две половины в валидное значение); пробелы между токенами допустимы. Значения: `env`, `space` —
  `^[a-z0-9][a-z0-9-]{0,62}$`, `revision` — `^[0-9a-f]{16}$`. Любое отклонение — `bad_body`, метки не тронуты.
- Ответы 304 и 401 сверяются только по коду; тело 401 и любых ошибок в журнал не попадает.

### 5.5 Состояние
`/var/lib/stvor-agent/state.json` (каталог 0700 — `StateDirectory=`, файл 0600), запись атомарная: временный файл в
том же каталоге → `fsync` → `rename` → `fsync` каталога. Схема 1:
```json
{"schema":1,
 "url":"https://stvor.example/robot-api/v1/labels",
 "expected":{"env":"prod","space":"sp-1","revision":"0123456789abcdef"},
 "applied_revision":"0123456789abcdef",
 "restart_attempts":{"count":0,"last":"2026-10-18T10:00:00Z"},
 "last_poll":{"at":"2026-10-18T10:00:00Z","outcome":"not_modified","http_status":304},
 "failures":0}
```
Загрузка строгая (неизвестная схема, лишние поля, значения не по регулярным выражениям — «состояния нет» + строка
журнала): руками правленый файл — не состояние. ETag отдельно не хранится: он равен `"<expected.revision>"`.

### 5.6 Конфигурация
- **Несекретное** — домен ручек cream `stvor-agent` (схема — в обёртке каталога, §11), файл
  `/etc/cream/stvor-agent/config`: `STVOR_AGENT_INTERVAL` (`int`, 60–600, умолчание 60), `STVOR_AGENT_LOG_LEVEL`
  (`enum:info,debug`, `info`). Служба читает его как `EnvironmentFile=-/etc/cream/stvor-agent/config` — своего
  парсера формата нет, нет и рассинхрона с разбором cream. Хук `apply` обёртки (§11) — `systemctl --no-block try-restart
  stvor-agent.service` (неблокирующий, как требует конвенция ручек).
- **Секретное** — `/etc/stvor-agent/token` (`url=<адрес>\ntoken=<токен>\n`), **не ручка cream**: адрес и токен
  неразделимы (токен уходит только на тот адрес, для которого выпущен), а `cream set` позволил бы поменять один без
  другого. Пишет только `stvor-agent enable`.
- Без cream (бинарь поставлен голым `.deb`) служба работает на умолчаниях — `EnvironmentFile` необязателен.

### 5.7 Взаимодействие с cream и расширением `teleport` — точные вызовы
S вызывает только два действия T и только через cream, абсолютным путём, без shell, с чистым окружением
(`PATH=/usr/sbin:/usr/bin:/sbin:/bin`, `LANG=C.UTF-8`; токена в окружении и argv нет), stdin — `/dev/null`. Оба
действия объявлены `output: json`: stdout `cream run` — ровно объект действия без конверта, флаги cream — до имени
расширения, всё после имени действия уходит действию (спека cream §7.2):

| Когда | Вызов | Тайм-аут | Разбор |
|---|---|---|---|
| каждый тик, после `set-managed`, `status` | `/usr/bin/cream run teleport managed-status` (`lock: none` — не ждёт долгий `join`) | 15 с | stdout ≤ 64 КиБ, строгий JSON схемы 1 (ниже); код ≠ 0 или не JSON → `tsErr` |
| метки не действуют | `/usr/bin/cream run --lock-timeout 20s teleport set-managed env=<v> space=<v>` | 90 с (ожидание блокировки ≤ 20 с + restart с подтверждением ≤ 30 с + запас) | код и JSON (ниже) |

По тайм-ауту S шлёт SIGTERM группе процессов `cream` и SIGKILL той же группе через 15 с: действие cream держит в
своей отдельной группе и убивает её сам по SIGTERM (у него 10 с между своими SIGTERM и SIGKILL), а SIGKILL сразу
оставил бы действие сиротой (найдено интеграционным тестом с настоящим cream, план Task 15). Собственный тайм-аут
действия из манифеста cream отдаёт кодом 79.

`stvor-agent disable` T не вызывает: управляемые `env`/`space` остаются (узел не переезжает молча), а метка
`stvor_agent` пустеет сама, потому что `version --label` без файла токена печатает пустую строку (§4.4).

`managed-status` (контракт T, схема 1):
```json
{"schema":1,"managed":{"env":"prod","space":"sp-1"},
 "pending":false,"joined":true,"active":true}
```
`set-managed`: тот же объект плюс `"result":"applied|unchanged|deferred|unconfirmed"`. Коды выхода действия: 0 —
`applied`/`unchanged`/`deferred`; 1 — `unconfirmed` (маркер pending остаётся); 2 — неверный ввод; прочие 3–63 —
ошибка. Коды cream: 75 — блокировка домена `teleport` занята дольше `--lock-timeout` (например, идёт `join`), 76 —
действие нарушило протокол, 64–79 — прочие ошибки до запуска (спека cream §11.3); S считает их неудачей тика без
счёта попыток restart. `set-managed` задаёт **полный** набор управляемых меток (замена, а не слияние): единственный
управляющий детерминированно знает, что должно стоять, и не оставляет мусорных ключей.
S проверяет ответ строго (`schema == 1`, ключи и значения по регулярным выражениям T); неизвестная схема — `tsErr`.
При старте служба один раз сверяет `cream actions teleport --json`: нет `managed-status` или `set-managed` — строка
журнала и тики без применения (контракт `depends: teleport >=4.0.0 <5` дублируется проверкой во время работы).

### 5.8 Обработка остановки посреди применения
`KillMode=mixed`: при остановке службы SIGTERM получает только `stvor-agent`, он дожидается дочернего `cream run`
(`TimeoutStopSec=120`: `set-managed` идёт до 90 с, и ещё до 15 с между SIGTERM и SIGKILL его группе — §5.7). Если дочерний всё же убит (SIGKILL по тайм-ауту, отключение питания) — маркер
`managed-pending` T записан **до** записи ручек, и следующий тик после старта увидит `pending=true` → `set-managed`
повторит restart.

### 5.9 Расширяемость без новых каналов
Желаемое состояние в коде — тип `Desired` с одним полем `Labels`; тик — «получить желаемое → сравнить с фактическим →
применить → отчитаться». Новый вид желаемого состояния (когда он появится, отдельной спекой и ADR портала) — новый
источник в `internal/portal` и новый применитель рядом с `internal/teleport`; цикл, состояние, блокировка,
наблюдаемость и упаковка не меняются. Общих интерфейсов «на будущее» сверх этого не вводится.

## 6. Безопасность
- **Root.** Служба работает от root: `set-managed` пишет `/etc/cream/teleport/config` и перезапускает
  `teleport.service`. Поверхность недоверенного ввода — ответ ≤ 4096 байт, разбираемый строгим токенным парсером на
  Go (память безопасна); значения, уходящие в T, прошли регулярные выражения и передаются argv без shell.
  Непривилегированные варианты — в §13.
- **Hardening юнита** (наследуется дочерними `cream`/T, поэтому только то, что им не мешает):
  `NoNewPrivileges=yes`, `PrivateTmp=yes`, `ProtectHome=yes`, `ProtectKernelTunables=yes`, `ProtectKernelModules=yes`,
  `ProtectKernelLogs=yes`, `ProtectControlGroups=yes`, `ProtectClock=yes`, `ProtectHostname=yes`,
  `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`, `RestrictNamespaces=yes`, `RestrictRealtime=yes`,
  `RestrictSUIDSGID=yes`, `LockPersonality=yes`, `MemoryDenyWriteExecute=yes`, `SystemCallArchitectures=native`,
  `UMask=0077`. **Не** ставится `ProtectSystem=` (T пишет `/etc/cream/teleport`, `/etc/teleport.yaml`,
  `/var/lib/cream/ext/teleport`; перечислять чужие пути в `ReadWritePaths=` — связать юнит S с раскладкой T). Сам агент
  Teleport запускает systemd в своём юните — ограничения S на него не распространяются. Приёмка: живой `set-managed`
  из-под службы на `pve-robot-1` и `systemd-analyze security stvor-agent.service` в отчёте.
- **Токен.** 43 знака `[A-Za-z0-9_-]`; со stdin только в `enable` (одна строка, пробелы по краям срезаются); файл 0600,
  владелец root, каталог 0700 — иначе отказ с причиной (содержимое не печатается); в памяти — только для заголовка;
  никогда — в argv, окружении дочерних процессов, журнале, `status`, `state.json`. Отправляется только на `url=` из
  того же файла; URL — строго `^https://[a-z0-9.-]+(:[0-9]{1,5})?/robot-api/v1/labels$`.
- **Секрет регистрации Teleport** S не видит никогда: он идёт только в T (§10.4).
- **Портал не может** через S ничего, кроме двух значений DNS-алфавита: S отправляет в T ровно ключи `env`, `space`; не меняет кластер, токен, приложения, прочие метки.
- **Цепочка поставки.** Воспроизводимая сборка (§9.2), `SHA256SUMS` в релизе, пин версии и сумм по архитектурам в
  обёртке каталога и в `.env` Stvor; сверка `sha256sum` до `dpkg -i` на машине.
- **Портов не открывает**: ни метрик, ни API; всё общение с людьми — CLI и журнал.

## 7. Наблюдаемость
- **Журнал** — `log/slog` в stderr (journald, `SyslogIdentifier=stvor-agent`): одна строка на событие, сообщение по-русски,
  ключи латиницей (`event=poll outcome=not_modified status=304 rev=…`, `event=apply result=unconfirmed attempt=2`,
  `event=skip reason=paused next_in=540s`). Уровень `debug` добавляет тайминги и строку на каждый тик без изменений.
  Секретов и тел ответов в журнале нет.
- **`stvor-agent status [--json]`** читает токен (только «задан/нет/битый/права»), `state.json` и
  `managed-status` T, в сеть не ходит. Схема 1:
```json
{"schema":1,"version":"4.0.0","service":"active","enabled":true,
 "url":"https://stvor.example/robot-api/v1/labels","token":"ok",
 "expected":{"env":"prod","space":"sp-1","revision":"0123456789abcdef"},
 "applied_revision":"0123456789abcdef","in_effect":true,
 "teleport":{"managed":{"env":"prod","space":"sp-1"},"pending":false,"joined":true,"active":true},
 "restart":{"attempts":0,"paused_until":null},
 "last_poll":{"at":"2026-10-18T10:00:00Z","outcome":"not_modified","http_status":304}}
```
  `token`: `ok | absent | bad_mode | bad_owner | no_access | corrupt` (`no_access` — запуск не от root: файл закрыт,
  `enabled` — `null`, код 1, причина «нужен root для полного статуса» в stderr); `outcome`: `ok | not_modified | unauthorized | rate_limited |
  network | redirect | http_error | bad_body | skipped_locked`. Без `--json` — те же поля таблицей по-русски.
- Портал видит агента через существующие поля (`pulled_at`, `applied_revision`, метка `stvor_agent` в инвентаре) —
  новых каналов телеметрии нет.

## 8. CLI агента
| Команда | Что делает | Коды |
|---|---|---|
| `stvor-agent run` | цикл службы (ExecStart) | 0 при штатной остановке |
| `stvor-agent enable URL=<адрес>` | токен со stdin → `/etc/stvor-agent/token` (атомарно, 0600); другой адрес — сброс `state.json`; `systemctl enable stvor-agent.service` и `restart`; затем **синхронный** `sync` (как `labels-enable` → `pull`) | 0; 1 — ввод/запись; 2 — использование. Отказ сети при первом `sync` — 0 со строкой (служба догонит) |
| `stvor-agent disable` | `systemctl disable --now`, удалить токен и `state.json`; T не вызывается — управляемые env/space остаются (узел не переезжает молча), метка `stvor_agent` пустеет на следующем исполнении команды (≤ 5 мин) | 0/1 |
| `stvor-agent sync` | один тик сейчас, без паузы 3×/15 мин | 0 — метки действуют или сеть/HTTP-ошибка (строка); 1 — ответ не по контракту, restart не подтверждён, токен битый |
| `stvor-agent apply env=<v> space=<v>` | ручной запасной путь карточки робота: те же значения в T без портала (`set-managed`), без паузы; ожидаемое в `state.json` не меняет — следующий тик вернёт решение портала | 0/1/2 |
| `stvor-agent status [--json]` | §7 | 0; 1 — не включён (для скриптов) |
| `stvor-agent version [--short\|--label]`, `help` | без флага — версия, коммит, сборка; `--short` — `X.Y.Z`; `--label` — команда динамической метки `stvor_agent` (её зовёт Teleport): `X.Y.Z`, если файл токена существует (агент включён), иначе пустая строка; в сеть не ходит, блокировку не берёт, файл токена не читает (только `stat`) | 0 всегда (иначе Teleport положил бы в метку текст ошибки) |

Все команды, кроме `version`/`help`/`status`, требуют root. Аргументы — `KEY=VALUE` (как у cream), повтор ключа —
отказ, значение со stdin — только токен у `enable`.

## 9. Упаковка и выпуск
### 9.1 Пакет
- `stvor-agent_<версия>_<amd64|arm64>.deb`, `Architecture` по сборке, `Depends: cream (>= 1.0.0)` —
  S вызывает `cream` на каждом тике; 1.0.0 — MVP cream, в котором есть всё, на что опирается §12 (спека cream §17);
  мажор cream 2 сменит контракт `cream/v1`, поэтому верхней границы нет, а несовместимость поймает `depends` обёртки; `systemd` не в `Depends` (сборка образа без systemd допустима).
- Состав: `/usr/bin/stvor-agent` (0755), `/usr/lib/systemd/system/stvor-agent.service`,
  `/usr/share/doc/stvor-agent/{copyright,NOTICE}`. Каталоги `/etc/stvor-agent` (0700) и `/var/lib/stvor-agent`
  создаются `enable`/`StateDirectory=`.
- Юнит: `Type=exec`, `ExecStart=/usr/bin/stvor-agent run`, `ConditionPathExists=/etc/stvor-agent/token` (без токена
  служба не стартует — «ждёт `enable`»), `After=`/`Wants=network-online.target`, `Restart=always`, `RestartSec=10`,
  `KillMode=mixed`, `TimeoutStopSec=120` (§5.8), `EnvironmentFile=-/etc/cream/stvor-agent/config`, `StateDirectory=stvor-agent`,
  `StateDirectoryMode=0700`, hardening из §6, `WantedBy=multi-user.target`.
- Сопровождающие скрипты: `postinst` — `daemon-reload` (если systemd запущен), `enable` юнита при первой установке
  (обновление не отменяет `disable` оператора); при наличии токена —
  `try-restart` (обновление пакета). `prerm remove` — `disable --now`. `postrm purge` — удалить `/etc/stvor-agent` и
  `/var/lib/stvor-agent`. Сборка образа (нет systemd) — только ссылка `enable`, ничего не стартует.

### 9.2 Сборка и выпуск
- `make` в контейнере `golang:1.26.9` (как в cream): `make lint` (gofmt, `go vet`, golangci-lint), `make test`
  (`go test -race ./...`), `make build-all` (`CGO_ENABLED=0 go build -trimpath -buildvcs=false
  -ldflags "-s -w -buildid= -X …/version.Version=<v>"`), `make deb` (nfpm), `make ci` (всё + проверка
  воспроизводимости: две сборки подряд дают равные суммы `.deb`).
- **nfpm** (отдельный бинарь в контейнере сборки, по пину версии) собирает оба `.deb` из одного `nfpm.yaml`; `mtime`
  файлов и архива — из `SOURCE_DATE_EPOCH` (время коммита тега). Если пин nfpm не даёт побайтной воспроизводимости,
  задача упаковки возвращается к проверенному `tools/build-deb.sh` (`dpkg-deb`, `gzip -n`) с шаблоном
  `Architecture` — критерий выбора — зелёная проверка воспроизводимости в `make ci`.
- **release-please**: `release-type: go`, тег `vX.Y.Z`; первый Go-релиз — **4.0.0** (коммит `feat!:`; теги
  `v3.0.0`…`v3.1.0` в репозитории уже есть, история версий монотонна, `stvor_agent=4.0.0` не путается с
  `bisquite_agent_version=3.0.1` на `stvorhost`). `package-name: stvor-agent`; файл `VERSION` удаляется (версия — из
  тега через `-ldflags`, CI сверяет).
- **CI** (`ci-release.yml`): PR и `main` — `make ci`; релиз — сборка из тега, `stvor-agent_<v>_amd64.deb`,
  `stvor-agent_<v>_arm64.deb`, `SHA256SUMS` в релиз GitHub; затем job `dispatch-extensions`:
  `gh workflow run agent-bump.yml -R iamletenkov/cream-extensions -f tag=<тег>` токеном
  `EXTENSIONS_DISPATCH_TOKEN` (PAT продлевается на новый целевой репозиторий; `HOME_REPO: iamletenkov/stvor-agent` —
  в форке отсутствие токена — предупреждение).
- **cream-extensions** `agent-bump.yml` (нынешний, перенацеленный на `stvor-agent` — спека cream §3, §16 шаг 3):
  берёт `SHA256SUMS` релиза, `tools/agent-bump.sh` ставит версию и обе суммы в
  `extensions/debian/stvor-agent/files/pin.env`, поднимает patch-версию расширения, гоняет
  `tools/check.sh`, открывает PR. Пины Stvor (`STVOR_AGENT_*` в `.env`) обновляются вручную по runbook портала —
  автоматики в портал нет.

## 10. Расширение `teleport` в cream-extensions
### 10.1 Что переписать, а что перенести
| Часть | Решение | Почему |
|---|---|---|
| join (token, bound_keypair, `/webapi/find`, версия кластера, откат, разбор журнала, `join-pending`/`join-refused`), `join-retry`, `scrub-token`, `leave`, `os-logins`, render узла и приложений, `apps.d` | **перенести bash как есть** в `files/teleport.sh`, заменив пути, имена юнитов и переменные (`BISQUITE_TELEPORT_*` → `CREAM_*` из окружения cream, спека cream §6.3) | ~100 проверок `test-join.sh` уже держат это поведение; переписывание — риск без выигрыша, расширения каталога — bash |
| render меток | **дописать**: `TELEPORT_LABELS` ∪ `TELEPORT_MANAGED_LABELS` и командные метки из `command-labels.d` (§10.3); убрать `managed_by`, `bisquite_agent_version` | новая граница |
| `set-managed` | **переписать** (bash): полный набор управляемых меток, маркер `managed-pending`, restart только при изменении итогового конфига, подтверждение (`InvocationID`/`MainPID`/`ActiveEnterTimestampMonotonic` — перенос `restart_agent`/`new_run`), вывод JSON | В3; политика пауз уходит в S |
| `managed-status` | **новое** | контракт для S |
| `pull`, `labels-enable`, `labels_with_space`, `parse_labels`, `read_token_file`, `TELEPORT_LABELS_URL`, юниты меток | **удалить** из T | это S |
| `lib/bisquite-conf` | **удалить**; ручки — окружением и командами cream (спека cream §6.3, §8.3) | один владелец формата |
| `.join.lock` и собственная `flock` | **удалить**; блокировка домена cream (§10.6) | одна блокировка на домен |
| `pkg-setup`, переход 2.x, ссылка `/usr/local/sbin` | **удалить** | В5 |
| обёртка `teleport-agent` (`install.sh`, `agent.sh`, `update.sh`), `test-teleport-agent.sh` | **удалить**; функции скачивания тарбола (`teleport_url`, сверка по `TELEPORT_SHA256`, «уже установлен») — переносятся в хук `install` нового `teleport`; `agent-bump` перенацеливается на `stvor-agent` (§9.2) | одна реализация установки |

### 10.2 Манифест и раскладка
Манифест — формат `cream/v1`; канонический пример — спека cream §4.3 (поля, ключи `scope: install`, действия с
`lock`, `files`). Здесь — только то, что спека cream сокращает:
```
extensions/debian/teleport/
  extension.yaml          schema cream/v1; name teleport; version 4.0.0; cream ">=1.0.0"; provides [teleport-node];
                          conflicts.capabilities [teleport-node]; hooks install/upgrade/remove/apply; config.keys §10.3;
                          actions §10.5; files — юниты ниже
  hooks/{install,upgrade,remove,apply}
  actions/{join,join-retry,leave,status,set-managed,managed-status,render,reload-apps,scrub-token}
                          тонкие входы: exec "$CREAM_EXT_DIR/files/teleport.sh" <имя> "$@"
  files/teleport.sh       общая библиотека хуков и действий (перенос bash, §10.1)
  files/os-logins         команда динамической метки os_logins (её зовёт Teleport)
  files/{teleport.service,teleport-apps.path,teleport-apps.service,teleport-token.path,teleport-token.service,
         teleport-join.service,teleport-join.timer}
  README.md, tests/ (§10.7)
```
Версия интерфейса управляемых меток — **мажор расширения** `teleport` (4.x): несовместимое изменение ручки
`TELEPORT_MANAGED_LABELS`, действий `set-managed`/`managed-status`, схемы их JSON или контракта `command-labels.d`
— это `teleport` 5.0.0. Потребитель объявляет `depends: [{name: teleport, version: ">=4.0.0 <5"}]` (спека cream §4.2,
§9.4). Отдельная способность (`teleport-managed-labels-v1`) не нужна: поставщик один, и `depends` по имени с
диапазоном выражает то же без второго словаря версий. Юниты названы по расширению (`teleport-*`), префикс `cream-`
зарезервирован за cream (спека cream §4.1). Внутренний CLI `cream-teleport` прежней редакции не нужен: входы — файлы
`actions/*`, в `PATH` ничего не ставится (`bin` в MVP cream отложен и пилоту не нужен).

### 10.3 Ручки домена `teleport` (`/etc/cream/teleport/config`)
Из прежней схемы остаются `TELEPORT_PROXY` (readonly, пишет `join`), `TELEPORT_JOIN_METHOD` (readonly),
`TELEPORT_TOKEN` (readonly, имя токена — не секрет; РT1), `TELEPORT_CA_PIN`, `TELEPORT_ENV`, `TELEPORT_NODENAME`, `TELEPORT_LABELS`,
`TELEPORT_APPS`, `TELEPORT_APPS_DISABLE`, `TELEPORT_APPS_ICONS`, `TELEPORT_APPS_DISCOVERY`, `TELEPORT_OS_LOGINS` — те
же типы и регулярные выражения (`ro:` → `readonly`, `?` → `empty`, спека cream §8.2). Удаляется `TELEPORT_LABELS_URL`.
Добавляются:

| Ключ | Тип | Умолчание | Описание |
|---|---|---|---|
| `TELEPORT_VERSION`, `TELEPORT_SHA256`, `TELEPORT_MIRROR`, `TELEPORT_TARBALL` | как в спеке cream §4.3, `scope: install` | 18.10.0 / пусто | бинарь Teleport; смена — повтор `cream install teleport KEY=…` (§10.4) |
| `TELEPORT_MANAGED_LABELS` | `list:re:^[a-z][a-z0-9_]{0,62}=[a-z0-9][a-z0-9._-]{0,62}$`, `readonly` (≤ 16 элементов) | пусто | метки внешнего управляющего; пишет только `set-managed` (`cream set --no-apply --allow-readonly` под унаследованной блокировкой), `cream set` оператора отказывает |

Значение допускает `.` — ради будущих управляющих; S сам ограничивает `env`/`space` DNS-алфавитом портала.
Зарезервированы в управляемых метках: `app`, `os_logins`, `teleport.icon` (как в `TELEPORT_LABELS`).
**РT1 (2026-10-19).** У действия cream может быть только один секретный параметр (спека cream §4.1), поэтому у
`join` единственный секрет — `TELEPORT_REGISTRATION_SECRET=-` со stdin: для метода `token` это сам join-токен, для
`bound_keypair` — секрет регистрации. `TELEPORT_TOKEN` — несекретное имя токена, передаётся парой. Секрет первого
входа лежит в `$CREAM_STATE_DIR` (0600) до подтверждения регистрации; render берёт его оттуда, `scrub-token` стирает.
Ни один ключ домена `teleport` не секретный; секрет в argv cream отвергает (64, спека cream §8.3).

**Командные метки (`/etc/cream/teleport/command-labels.d/`).** Обобщение `apps.d`: другое расширение кладёт файл
`<ключ>` (через `files` своего манифеста — cream владеет файлом и удалит его при `remove`) с одной строкой
`command=<абсолютный путь> [аргумент…]`. render добавляет в `ssh_service.commands` элемент `{name: <ключ>,
command: [путь, аргументы…], period: 5m0s}` (как у `os_logins`). Правила render, нарушение — файл пропускается со
строкой журнала, конфиг остального не страдает:
- ключ `^[a-z][a-z0-9_]{0,62}$`, не `env`, `space`, `app`, `os_logins`, `teleport.icon` и не ключ из
  `TELEPORT_LABELS`/`TELEPORT_MANAGED_LABELS` (Teleport и Stvor сливают командные метки поверх статических, так
  командная метка могла бы подменить `space`);
- путь — существующий исполняемый файл, владелец root, без записи группе и прочим; аргументы — `[A-Za-z0-9._=-]+`,
  не больше 8; shell не участвует;
- каталог `command-labels.d` — 0755 root; не больше 8 файлов.

Командные метки ставятся только узлу (Teleport не даёт `commands` приложениям). Изменение каталога видит
`teleport-apps.path` (он следит за `apps.d` и `command-labels.d`) и делает reload (HUP, плавный, без разрыва сессий).

**Правило render:** метки узла = `TELEPORT_LABELS` (без `env`, как прежде) → поверх — `TELEPORT_MANAGED_LABELS`
(управляемая побеждает по ключу) → `env` = управляемый `env`, если задан, иначе `TELEPORT_ENV`; командные метки —
`os_logins` (при `TELEPORT_OS_LOGINS=1`) и `command-labels.d`. Статические метки копируются в узел и **в каждое
приложение** одним render (атомарность «узел + приложения», ADR-0013). `cream run teleport status` показывает
перекрытые операторские ключи («перекрыто управляемой меткой») и командные метки с источником-файлом.

Хук `apply` (ручки оператора) — как прежде: render + `systemctl --no-block reload-or-restart teleport.service`
(HUP для оператора допустим); cream держит на время хука блокировку домена (§10.6).

### 10.4 Хуки жизненного цикла
- **`install`** — ставит бинарь и юниты, **не регистрирует**:
  - бинарь по ключам `scope: install` (§10.3): `TELEPORT_SHA256` — сумма тарбола **архитектуры машины**,
    `TELEPORT_MIRROR` — база `<base>/binaries/teleport/<ver>/<тарбол>` (пусто — `cdn.teleport.dev`), `TELEPORT_TARBALL`
    — локальный файл (офлайн), взаимоисключим с `TELEPORT_MIRROR`; «уже установлен той же версии» — пропуск;
  - юниты раскладывает cream (`files`), хук делает `daemon-reload` (если systemd работает) и `enable` path-юнитов;
    агент не стартует;
  - повтор с новым ключом `scope: install` (портал поднял версию Teleport) — cream перезапускает хук (спека cream
    §6.5): новый бинарь, `try-restart teleport.service`, только если версия бинаря сменилась; повтор после
    `status: failed` (не скачался тарбол) — тоже автоматически, той же командой;
  - в сборке образа (`CREAM_PHASE=build`) с готовой регистрацией в `/var/lib/teleport` — отказ (прежняя защита «одна
    нода на все копии образа»).
- **Регистрация — только действием `join`** (§10.5): секрет приходит на stdin действия (`stdin: secret`, спека cream
  §7.2) и никуда не пишется ручкой; повтор регистрации — тот же `join` с `TELEPORT_FORCE=1`, а не повтор `install`.
  `TELEPORT_DEFER=1` — записать конфиг и секрет, поставить `join-pending`, **не** включать ни агента, ни таймер повтора
  (режим `--no-start` рабочей станции Stvor); зарегистрировать позже — `cream run teleport join-retry`.
- **`upgrade`** — новые скрипты и/или бинарь по тем же ключам; `cream run teleport render`; `try-restart
  teleport.service` только если версия бинаря сменилась (иначе `reload`).
- **`remove`** — `leave`, `disable --now` юнитов, удалить бинарь и `/var/lib/cream/ext/teleport`; юниты, дерево и
  (с `--purge`) домен ручек удаляет cream.

### 10.5 Действия
| Действие | `lock` | Вход | Выход / коды |
|---|---|---|---|
| `join KEY=…` | `exclusive` | `args: pairs`: `TELEPORT_PROXY`, `TELEPORT_JOIN_METHOD`, `TELEPORT_TOKEN`, `TELEPORT_REGISTRATION_SECRET=-` (секрет со stdin), `TELEPORT_CA_PIN`, `TELEPORT_ENV`, `TELEPORT_NODENAME`, `TELEPORT_LABELS`, `TELEPORT_OS_LOGINS`, `TELEPORT_FORCE`, `TELEPORT_DEFER` | 0 — зарегистрирован или ждёт сети (`join-pending`); 1 — отказ кластера (причина из журнала Teleport); JSON-итог в stdout |
| `join-retry` | `exclusive` | — | тик `teleport-join.timer` и ручной запуск отложенной регистрации |
| `leave` | `exclusive` | — | агент выключен, `/var/lib/teleport`, yaml, секрет, `TELEPORT_MANAGED_LABELS`, `managed-pending` стёрты |
| `status` | `none` | — | текст: регистрация, служба, приложения, ручки (секреты маскированы), управляемые и командные метки |
| `set-managed k=v…` | `exclusive` | `args: free`: полный набор управляемых меток (пустой набор = снять все), проверка по типу `TELEPORT_MANAGED_LABELS` | §5.7. Маркер `managed-pending` пишется **до** записи ручки; запись; render в кандидат. Restart (не HUP) с подтверждением нового запуска ≤ 30 с — только если узел зарегистрирован **и** (кандидат ≠ текущий `/etc/teleport.yaml`, или есть маркер от прошлого неподтверждённого вызова, или агент неактивен); подтвердилось — маркер снят, `applied`. Итоговый конфиг не меняется, маркера не было и агент активен — `teleport.yaml` не трогается, restart нет, маркер снят: `applied`, если ручка изменилась (первый тик после `join`), `unchanged`, если нет. Не зарегистрирован — запись без restart, `deferred` |
| `managed-status` | `none` | — | §5.7, без побочных эффектов |
| `render` | `none`, `phases: [build, live]` | — | `ExecStartPre`/`ExecReload` `teleport.service`: пишет `/etc/teleport.yaml` из ручек, `apps.d`, `command-labels.d`; домен только читает |
| `reload-apps` | `none` | — | `ExecStart` `teleport-apps.service`: `systemctl reload teleport.service` |
| `scrub-token` | `exclusive` | — | `teleport-token.service` (`RemainAfterExit=yes`): стирает одноразовый токен после регистрации |

### 10.6 Блокировка
Отдельной `flock` у T больше нет: блокировку держит cream — `lock/teleport` (спека cream §6.3). `join`, `join-retry`,
`leave`, `set-managed`, `scrub-token` и хук `apply` идут под ней эксклюзивно; вложенный `cream set --allow-readonly`
из действия наследует её через `CREAM_LOCK_FD`. Оператор `cream set` (HUP) и S (`set-managed` → restart) больше не
встречаются на одном юните без порядка (риск В7 описи). S ждёт блокировку ≤ 20 с (`--lock-timeout 20s`), затем код
cream 75.

`render`, `reload-apps`, `managed-status` и `status` — `lock: none` (правило спеки cream §7.3): `set-managed`
держит `lock/teleport` и синхронно делает `systemctl restart teleport.service`; `ExecStartPre=/usr/bin/cream run
teleport render` — процесс юнита, а не потомок `set-managed`, и с `lock: exclusive` ждал бы ту же блокировку до
таймаута. Действия с `lock: none` только читают домен; файлы cream пишет атомарно (`rename`), поэтому render видит
целую версию ручек. `scrub-token` под `exclusive` безопасен: `join` не ждёт синхронно `teleport-token.service`.

### 10.7 Тесты расширения
- `tests/test-join.sh` — перенос `stvor-agent/tests/test-join.sh` (~99 проверок) на новые пути и имена.
- `tests/test-managed.sh` — новые: render (перекрытие ключей, `env`, копия в каждое приложение, зарезервированные
  ключи, командные метки: допустимый файл, ключ-коллизия со статической меткой, путь не root или с записью группе,
  лишние аргументы — файл пропущен, остальной конфиг цел), `set-managed` (маркер до записи; подтверждение по каждому
  из трёх свойств юнита; «restart принят, нового запуска нет» — `unconfirmed`; «тик умер между записью и restart» —
  следующий вызов рестартует; итоговый конфиг не изменился — без restart, `applied`/`unchanged`; `deferred` без
  регистрации; код 2 на ввод), `managed-status` (схема; файлы-образцы `tests/fixtures/managed-status-*.json` —
  **те же**, что лежат в `stvor-agent/internal/teleport/testdata/`, сверка побайтно — скрипт
  `tools/check-contract-fixtures.sh` в обоих репозиториях по пину коммита каталога), `leave` стирает управляемые метки.
- Подменённый корень `CREAM_ROOT`, поддельные `systemctl`/`curl`/`teleport`/`journalctl` — как сейчас; запуск из
  `tools/check.sh` (`cream validate --catalog .` + shellcheck + эти тесты, спека cream §14).
- Тупик «`set-managed` → restart → `ExecStartPre=cream run teleport render`» закрывает интеграционный тест cream
  (фикстура `lock-restart`, спека cream §14); здесь — живой прогон в контейнере с systemd (§15.3).

## 11. Обёртка `stvor-agent` в cream-extensions
```
extensions/debian/stvor-agent/
  extension.yaml   schema cream/v1; name stvor-agent; version 4.0.0; arch [amd64, arm64]; targets {build: exec,
                   live: "yes"}; provides [stvor-agent]; depends [{name: teleport, version: ">=4.0.0 <5"}];
                   hooks install/upgrade/remove/apply; config.keys (ниже и §5.6); files — drop-in метки
  hooks/install    .deb → sha256 → dpkg -i
  hooks/upgrade    то же (postinst пакета перезапускает службу)
  hooks/remove     stvor-agent disable; dpkg -r stvor-agent
  hooks/apply      systemctl --no-block try-restart stvor-agent.service
  files/pin.env    STVOR_AGENT_PIN_VERSION, STVOR_AGENT_PIN_SHA256_AMD64, STVOR_AGENT_PIN_SHA256_ARM64 (пишет agent-bump)
  files/stvor_agent.command-label
                   command=/usr/bin/stvor-agent version --label
                   → files: {dst: /etc/cream/teleport/command-labels.d/stvor_agent, mode: "0644"}
  README.md
```
Ключи `config.keys` обёртки:
- `scope: install` — `STVOR_AGENT_VERSION` + `STVOR_AGENT_SHA256` (сумма `.deb` архитектуры машины; заданы версия без
  суммы или сумма без версии — отказ; не заданы — пин из `files/pin.env`), источник — одно из `STVOR_AGENT_MIRROR`
  (база, файл плоско `<base>/stvor-agent_<v>_<arch>.deb` — так раздаёт зеркало Stvor), `STVOR_AGENT_DEB`
  (абсолютный путь — офлайн) или по умолчанию релиз GitHub. Повтор `cream install stvor-agent` с новой версией —
  перезапуск хука (спека cream §6.5), это и есть обновление агента порталом;
- `scope: config` — `STVOR_AGENT_INTERVAL`, `STVOR_AGENT_LOG_LEVEL` (§5.6).

Явные версия и сумма из параметров побеждают пин каталога: у портала свой пин (`.env`), каталог — умолчание для
образов bisquite. Хук `install` токен **не** принимает и службу не включает: включение — `stvor-agent enable`
(токен со stdin, §8). Так токен меток не становится ручкой cream (§13) и не требует отложенного в MVP хука `secret`.
Коды хука: 0 — пакет стоит; иначе — сбой (сумма, источник, `dpkg`), cream вернёт 79 и `status: failed`, повтор той же
командой перезапустит хук. Drop-in командной метки раскладывает cream до хука (спека cream §6.5); `teleport-apps.path`
видит его и делает reload агента Teleport (HUP, без разрыва сессий, §4.4); в сборке образа файл попадает в первый
render. Тест — `tools/test-stvor-agent.sh` (порт `test-teleport-agent.sh`: выбор источника, сверка суммы, отказ при
неполном пине, токен не в argv/окружении `dpkg`/`stvor-agent`, drop-in на месте и удаляется `remove`).

Вызовы на машине (как их соберёт скрипт Stvor; формы команд — спека cream §7.2, §8.5, §9.1):
```sh
cream source add stvor --archive https://<домен>/dist/teleport/cream-extensions-<v>.tar.gz --sha256 <пин> --replace --sync
cream install teleport TELEPORT_VERSION=18.10.0 TELEPORT_SHA256=<пин arch> TELEPORT_MIRROR=https://<домен>/dist/teleport
printf '%s' "$SECRET" | cream run teleport join TELEPORT_PROXY=<proxy> TELEPORT_JOIN_METHOD=bound_keypair \
  TELEPORT_TOKEN=<имя> TELEPORT_REGISTRATION_SECRET=- TELEPORT_ENV=<env> TELEPORT_NODENAME=<имя> TELEPORT_LABELS=space=<slug>
cream install stvor-agent STVOR_AGENT_VERSION=<v> STVOR_AGENT_SHA256=<пин arch> STVOR_AGENT_MIRROR=https://<домен>/dist/teleport
printf '%s' "$LABEL_TOKEN" | stvor-agent enable 'URL=https://<домен>/robot-api/v1/labels'
# офлайн: --archive file:///<каталог>/cream-extensions-<v>.tar.gz, TELEPORT_TARBALL=…, STVOR_AGENT_DEB=…
# образ bisquite (расширения уже в образе), firstboot:
printf '%s' '<секрет>' | cream run teleport join TELEPORT_PROXY=… TELEPORT_REGISTRATION_SECRET=- …
printf '%s' '<токен>' | stvor-agent enable 'URL=https://<домен>/robot-api/v1/labels' || true
```

## 12. Что требуется от cream (сверено со спекой cream)
Допущения C1–C14 прежней редакции сверены 2026-10-18 со [спекой cream](https://github.com/iamletenkov/cream/blob/main/docs/specs/2026-10-18-cream-core.md).
Каждое закрыто ссылкой на её раздел (номера § в правом столбце — разделы спеки cream, кроме помеченных «здесь»);
где спеки расходились, решение записано в обеих. Всё ниже входит в MVP cream
1.0.0 (её §17) — отложенное этой спеке не нужно.

| # | Было допущение | Решение / раздел спеки cream |
|---|---|---|
| C1 | повтор `cream install` той же версии перезапускает `install.sh` с новыми параметрами | **Изменено здесь.** Повтор пуст при `status: installed`, если не изменился ключ `scope: install`; при `failed`/`failed-upgrade` хук перезапускается сам (§6.5, §8.2, §8.5). Перерегистрация — `cream run teleport join TELEPORT_FORCE=1`, а не повтор install (здесь §10.4); обновление бинаря Teleport и агента — повтор install с новым ключом `scope: install` |
| C2 | `KEY=-` — секрет со stdin скрипту, не в реестр/журнал | §7.2 (секрет только `KEY=-`, на stdin **действия**), §8.3, §10 (`params_digest` с литералом `<secret>`), §13. Секреты `install` не нужны: регистрация — действие `join` (`stdin: secret`), токен меток — `stvor-agent enable`; хук `secret` отложен (§17) |
| C3 | `cream run` насквозь: argv без shell, stdin/stdout/stderr и код; сообщения cream — stderr; `--json` передаётся действию | §7.2: так, кроме `--json` — это флаг cream (до имени расширения) и конверт; S вызывает без него и получает объект действия (`output: json`) как есть. Коды действия 0–63 насквозь, 64–79 — коды cream (§11.3) |
| C4 | `actions` в манифесте, поле `layout` | §4.2, §7.1: `schema: cream/v1`, действия — файлы `actions/<имя>` с `lock`, `args`, `stdin`, `output`, `phases` (здесь §10.2) |
| C5 | `cream source add <имя> --tar <URL\|путь> --sha256`; повтор — замена; путь офлайн | §9.1, §9.2: `--archive <https://…\|file:///…> --sha256`, замена — `--replace`, кеш — `--sync` или `cream source sync`; голый путь — отказ, офлайн — `file://` |
| C6 | ручки: типы `bisquite-conf`, `ro`, `secret`, `list:`, `?`; `ro` пишет своё расширение; неблокирующий `apply`; `cream show --json`; `EnvironmentFile` | §8.1–§8.4: `readonly` + `--allow-readonly` из действия под унаследованной блокировкой (§6.3), `empty`, `secret: file`, `scope`; хук `apply` обязан не ждать заданий systemd |
| C7 | библиотека ручек для bash по стабильному пути | **Не нужна.** Несекретные ключи — в окружении хука/действия, секреты — `cream config get --secret`, запись — `cream set` (§6.3, §8.3). Библиотеки-расширения (`kind: library`, §4.5) — для общего кода, ручкам не нужны |
| C8 | раскладка `/opt/cream/<ext>`, `/var/lib/cream/<ext>`; `CREAM_EXT_DIR`, `CREAM_STATE_DIR`, `CREAM_ROOT` | §5, §6.3: `/opt/cream/ext/<ext>`, `/var/lib/cream/ext/<ext>`, `/etc/cream/<ext>/{config,secrets}`; переменные — те же (здесь §4.3 поправлен) |
| C9 | `requires` по способностям, `teleport-managed-labels-v1` | **Изменено здесь.** `depends: teleport >=4.0.0 <5` по имени с диапазоном (§4.2, §9.4); способность не нужна (здесь §10.2, закрывает О2) |
| C10 | `cream list --json` | §8.3, §11.4 |
| C11 | `cream upgrade`/`remove` зовут хуки | §6.5; `remove` отказывает, пока есть зависимые (`stvor-agent` → `teleport`) |
| C12 | фаза build без systemd, скрипты различают | §6.3 (`CREAM_PHASE`), §6.4, §12.2; `render` объявлен `phases: [build, live]` |
| C13 | `cream_<v>_<arch>.deb`, `/usr/bin/cream`, сам в сеть не ходит | §0, §1, §15: cream ходит в сеть только в `source sync`; хуку `teleport` сеть нужна для тарбола (или `TELEPORT_TARBALL`/зеркало) |
| C14 | `cream run` не держит общую блокировку дольше действия, сериализует расширение | §6.3: действия берут только `lock/<ext>` (`exclusive`/`shared`/`none`), `lock/global` — только install/upgrade/remove; ожидание — `--lock-timeout`, затем 75. `managed-status` — `lock: none`, тик S не ждёт долгий `join` (спека cream поправлена: было `shared`) |

Правки, внесённые в спеку cream по этой сверке: `managed-status` — `lock: none`; пример §4.3 без
`stvor_agent` в управляемых метках, с действиями `render`/`reload-apps` (`lock: none`) и `set-managed` (`args: free`);
форма `cream source add --archive`; правило §7.3 о тупике через systemd; флаги cream в `cream run` — только до имени
расширения.

Требование к **cream-extensions** (не к cream): релиз каталога публикует ассет `cream-extensions-<v>.tar.gz`,
собранный воспроизводимо (`git archive --prefix=cream-extensions-<v>/ v<v> | gzip -n`), и `SHA256SUMS`. Автоархив
GitHub не годится: его байты не обещаны, а пин Stvor запрещает «та же версия — другая сумма» (ADR-0016 п. 6).

## 13. Отвергнутые варианты
| Вариант | Почему нет |
|---|---|
| Оставить агента на bash, только разрезать | вопрос В2 решён владельцем; плюс ~100 проверок на подменённом `curl` вместо `httptest` и строгий JSON на регулярных выражениях |
| oneshot + таймер вместо службы | таймер не умеет backoff и мгновенный тик по SIGHUP; состояние перечитывается каждую минуту; владелец выбрал службу |
| Вариант 2 описи: S сам пишет `cream set --no-apply teleport …` и сам рестартует `teleport.service` | S знал бы имя юнита и правило «restart, не HUP» чужого расширения; два процесса рестартуют один юнит без общей блокировки; слияние в `TELEPORT_LABELS` — гонка и откат правок оператора |
| drop-in `labels.d/stvor.conf` + path-юнит у T | асинхронно: S не узнаёт момент и успех restart, `X-Applied-Revision` пришлось бы выводить косвенно; path-юнит по конвенции дал бы reload |
| `set-managed` со слиянием (ключи вне вызова остаются) | у управляющего нет способа убрать ключ; ручной вызов оставлял бы мусор; S и так знает полный набор |
| S от непривилегированного пользователя + sudoers на `cream run teleport set-managed` | `sudo` не обязателен на минимальных образах; правило sudoers с аргументами — хрупкое; выигрыш мал при строгом разборе 4 КиБ |
| S непривилегированный + polkit-правило на запуск oneshot-юнита T | `polkitd` нет на минимальных образах Debian; контракт превращается в «файл + юнит», теряются JSON и коды |
| Свой парсер конфига в Go вместо `EnvironmentFile=` | второй разбор формата cream — второй источник расхождений |
| Токен меток ручкой cream (`secret`) | адрес и токен разделялись бы `cream set`; токен попал бы в общий файл ручек |
| Метка `stvor_agent` управляемой (S пишет `stvor_agent=<версия>` через `set-managed`) | каждое обновление агента меняло бы набор управляемых меток — restart агента Teleport с разрывом SSH-сессий; первый тик после регистрации тоже рестартовал бы; динамическая метка (Р3) не требует ни того, ни другого |
| Метка `stvor_agent` при join (передать её скриптом Stvor в `join`) | Stvor писал бы метку агента в обход S; при динамической метке и не нужно |
| Ручка T `TELEPORT_COMMAND_LABELS`, которую обёртка `stvor-agent` дописывает `cream set teleport …` | чтение-изменение-запись чужого домена — та же гонка с оператором, что В7 описи; drop-in `command-labels.d` с владельцем-расширением (`files`) снимается при `remove` сам и повторяет проверенный контракт `apps.d` |
| Включать метку из `stvor-agent` при первом запуске (`set-managed` один раз) | служба писала бы конфиг Teleport вне своего контракта; первый запуск после каждой переустановки — снова restart; drop-in ставится установкой и дальше не меняется |
| Способность `teleport-managed-labels-v1` для версии интерфейса | поставщик один; `depends: teleport >=4.0.0 <5` выражает то же без второго словаря версий (спека cream §4.2) |
| Регистрация внутри хука `install` (`cream install teleport … TELEPORT_REGISTRATION_SECRET=-`) | секрет `install` потребовал бы хук `secret` (отложен в MVP cream) или хранения секрета ручкой; повтор `install` пуст при `installed` и не годится для перерегистрации; `join` — действие со `stdin: secret` |
| Включение службы хуком обёртки (`STVOR_AGENT_TOKEN=-` в `cream install stvor-agent`) | токен меток стал бы параметром cream; `stvor-agent enable` со stdin — уже готовый путь, и он же нужен образам bisquite |
| Собственная `flock` расширения `teleport` (`.lock`) рядом с блокировкой cream | две блокировки на один домен — порядок захвата и тупики; cream уже сериализует действия домена (спека cream §6.3) |
| goreleaser | release-please уже создаёт тег, релиз и CHANGELOG; goreleaser дублирует их ради упаковки, которую nfpm делает один |
| Версия 1.0.0 для первого Go-релиза | в репозитории уже есть `v3.x`; немонотонная история и путаница `stvor_agent=1.0.0` против `bisquite_agent_version=3.0.1` на `stvorhost` |
| `encoding/json` `Unmarshal` для тела ответа | повторы ключей и регистронезависимое сопоставление ключей — нарушение строгого контракта v1 |
| Перенести `managed_by=bisquite-teleport-agent` в T «для совместимости» | T не говорит от имени Stvor; совместимость `stvorhost` держит Stvor (Р1) |
| Обёртка `stvor-agent` ставит и Teleport (как прежний `teleport-agent`) | две реализации установки Teleport; граница T/S размывается |

## 14. Миграция репозитория `stvor-agent`
**Удаляется:** `src/bisquite-teleport`, `lib/bisquite-conf`, `src/knobs`, `src/knobs.apply`, `src/pkg-setup`,
`src/systemd/*`, `debian/*`, `tools/build-deb.sh` (если nfpm прошёл проверку воспроизводимости, §9.2), `tools/check.sh`,
`tests/test-conf.sh` (поведение библиотеки ручек — забота cream), `tests/test-package.sh` (сценарии 2.x; идеи
проверок пакета — в Go-тест упаковки), `VERSION`.
**Переезжает в cream-extensions** (коммитом туда, после переноса здесь удаляется): bash join/render/os-logins/leave/
scrub-token → `extensions/debian/teleport/files/teleport.sh` (входы — `hooks/*`, `actions/*`, §10.2); юниты Teleport;
`tests/test-join.sh`.
**Остаётся и обновляется:** `docs/portal-labels-api.md` (копия контракта v1; клиент — `stvor-agent`), `LICENSE`,
`NOTICE`, `SECURITY.md`, `CHANGELOG.md` (продолжается с 4.0.0), `README.md`, `CLAUDE.md`, `.github/` (новый CI §9.2,
release-please `release-type: go`, `package-name: stvor-agent`).
**Новое:** `go.mod`, `cmd/`, `internal/`, `nfpm.yaml`, `packaging/stvor-agent.service`, `Makefile` (docker-first),
`docs/test-map.md` — таблица соответствия проверок (§15).

Порядок работ по репозиториям (зависимости):
1. cream: `v1.0.0` — MVP спеки cream (её §17; всё, на что опирается §12) — блокер всего остального.
2. cream-extensions: расширение `teleport` (перенос bash, §10), путь `apps.d` у `code-server` и `selkies`, удаление
   `teleport-agent`, перенацеливание `agent-bump` на `stvor-agent`, библиотека `cloud-user` и `docker` (пилот спеки
   cream §16 шаг 2), ассет-снимок каталога.
3. stvor-agent: Go-служба, упаковка, релиз 4.0.0 → dispatch → обёртка `stvor-agent` с пином → релиз каталога.
4. Stvor: срез по ADR-0030 (пины, зеркало, скрипт установки, распознавание `stvor_agent` в `cmd_labels`, фронт, гейты).
5. bisquite: примеры VMFILE и манифестов (`EXTENSION teleport` + `EXTENSION stvor-agent`, строки firstboot) — отдельно.
6. Переустановка `pve-robot-1`; живая приёмка (§16). `stvorhost` не трогается.

**Совместимость на переходный период: портал пинит 3.1.0, выпуск 4.0.0 портал не задевает до среза Stvor.** Пока
срез Stvor не сделан (портал сейчас занят другой работой, трогать его нельзя), скрипт установки портала ставит
`bisquite-teleport-agent` 3.1.0 со своего зеркала: релиз 3.1.0 уже опубликован, а зеркало раздаёт файл строго по
пину `.env` (`BISQUITE_AGENT_VERSION`/`BISQUITE_AGENT_SHA256`, ADR-0016) — новые теги 4.x в репозитории `stvor-agent`
его не меняют. Поэтому репозиторию не нужна ветка выпуска 3.x: релиз 3.1.0 и его ассеты не удаляются и не
перевыпускаются, а исправления 3.x не планируются (машин с 3.1.0 вне стенда нет, `stvorhost` — 3.0.1). Переход
портала на 4.x — только срезом по ADR-0030, одновременно с пинами cream и каталога.

## 15. Тестирование
### 15.1 Go (S)
- `internal/portal`: `httptest.NewTLSServer` с корнем в пуле теста — 200/304/401/429/5xx/3xx (редирект не исполнен,
  второй сервер не получил запрос), тайм-ауты, тело 4096/4097 байт, таблица строгого разбора (лишний ключ, повтор
  ключа, иной порядок, `"ENV"`, вложенный лишний объект, хвост после объекта, NUL, не-UTF-8, значения вне
  регулярных выражений, пустое тело), заголовки (`If-None-Match` в кавычках, `X-Applied-Revision` только вместе с ним
  и той же ревизией).
- `internal/reconcile`: фейковые часы и фейковый `Teleport` — сверка на 200 и на 304; ручная порча → применение;
  `unconfirmed` → счётчик; 3×/15 мин при активном агенте; без паузы при неактивном и для `manual`; `deferred` не
  считается; `applied_revision` только при `inEffect`; смена URL сбрасывает состояние; ошибка `managed-status` —
  ничего не применяется и `X-Applied-Revision` не уходит.
- `internal/teleport` (exec-адаптер): поддельный `cream` — тестовый бинарь в режиме помощника (`os.Args[0]` +
  переменная окружения теста) пишет argv, окружение и stdin в файл и отвечает по сценарию: argv ровно как в §5.7,
  **токена нет ни в argv, ни в окружении, ни в stdin**, тайм-ауты убивают процесс, не-JSON и чужая схема — ошибка,
  файлы-образцы `testdata/managed-status-*.json` (те же, что у T, §10.7).
- `internal/token`, `internal/state`: права ≠ 0600, владелец ≠ 0 (через подменяемый `stat` в тестах), битый формат,
  атомарность записи (сбой между записью и `rename` не портит прежний файл).
- `internal/daemon`: джиттер в границах, backoff по таблице, 401 → 600 с, SIGHUP → тик, SIGTERM ждёт тик,
  неблокирующая блокировка пропускает тик.
- `cmd`/`internal/cli`: коды выхода, `enable` со stdin (пустой, неверная форма, пробелы), `status --json` — схема;
  `version --label`: версия при наличии файла токена, пустая строка без него, код 0 в обоих случаях, файл токена
  не открывается (только `stat`).
- `internal/reconcile` и адаптер: в `set-managed` уходят ровно `env`, `space` (без `stvor_agent`); коды cream 75/76 —
  неудача тика без счёта попыток restart.
- Юнит: тест сверяет строки hardening и `ConditionPathExists`; в CI — `systemd-analyze verify`.

### 15.2 Таблица соответствия
`docs/test-map.md`: строка на каждую проверку `tests/test-labels.sh` (~101, 7 разделов: `set-managed`,
`labels-enable`, `pull`, «R3: перезапуск подтверждается», «leave снимает метки», «join: набор ключей не изменился»,
«юниты таймера меток») → имя Go-теста **или** «переехало в T: `tests/test-managed.sh` / `test-join.sh`, проверка …»
**или** «снято: <причина>» (например, юниты таймера). Задача переноса закрывается, когда пустых строк нет; ревьюер
сверяет таблицу с кодом.

### 15.3 Сквозные
- Контейнер Debian 12 с systemd (в CI stvor-agent, по одному): `cream` + снимок каталога + `teleport` (поддельный
  `teleport`-бинарь, без кластера) + `stvor-agent` против `httptest`-портала: `join` → `enable` → первый тик —
  `set-managed` без restart (итоговый конфиг тот же) → портал меняет `space` → restart подтверждён →
  `X-Applied-Revision` на следующем тике; установка обёртки кладёт drop-in, render добавляет команду `stvor_agent` в
  `teleport.yaml`, `teleport.service` получил reload, а не restart; `set-managed` во время restart не ждёт
  `ExecStartPre=cream run teleport render` (тупик спеки cream §7.3).
- Живые — гейты Stvor (`robots-gate` фазы 12–14, `arm64-gate`, `workstation-gate`) и приёмка §16.

## 16. Приёмка
1. `make ci` зелёный в stvor-agent и `tools/check.sh` в cream-extensions; воспроизводимость `.deb` подтверждена.
2. `docs/test-map.md` без пустых строк.
3. На переустановленной `pve-robot-1` (amd64) одна команда портала ставит cream, `teleport`, `stvor-agent`; узел
   несёт `space`, `env`, `stvor_agent=4.0.0`; перенос робота в другое пространство и обратно завершается сверкой
   портала за ≤ 2 мин; узел **и** приложение (`TELEPORT_APPS`) меняют `space` вместе (повтор приёмки ADR-0013).
4. arm64 — `arm64-gate` портала с тем же набором.
5. `stvorhost` продолжает получать метки агентом 3.0.1, карточка — «агент есть».
6. На `pve-robot-1` установка обёртки `stvor-agent` при открытой сессии `tsh ssh` сессию не рвёт (HUP), метка
   `stvor_agent` видна в `cmd_labels` узла (`tctl get nodes`) не позже 5 мин; обновление `stvor-agent` на тестовую
   4.0.x не меняет `InvocationID` `teleport.service`, метка сменяется в течение периода.

## 17. Открытые вопросы
Открытых вопросов нет. Закрытые при сверке со спекой cream 2026-10-18:

| # | Вопрос | Решение |
|---|---|---|
| О1 | Подтвердить C1–C14 в спеке cream | §12: каждое допущение — раздел спеки cream или правка здесь; C1, C7, C9 изменены здесь, `managed-status` (`lock: none`) и правило тупика через systemd — правки спеки cream |
| О2 | Имена `cream-teleport`, `teleport-managed-labels-v1`, юниты `teleport-*` | внутренний CLI `cream-teleport` упразднён: входы — `hooks/*` и `actions/*`, библиотека — `files/teleport.sh` (префикс `cream-` зарезервирован за cream, спека cream §4.1); способности нет — `depends: teleport >=4.0.0 <5` (§10.2); юниты `teleport-apps.*`, `teleport-token.*`, `teleport-join.*` соответствуют конвенции «юнит по имени расширения» |
| О3 | Минимальная версия cream в `Depends:` | `cream (>= 1.0.0)` — MVP cream содержит всё из §12 (§9.1); поднимается, только если S начнёт опираться на поле или команду из отложенного списка спеки cream §17 |
