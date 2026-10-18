# План: stvor-agent на Go и расширение `teleport` каталога cream

> 2026-10-18. Ляжет в репозиторий `stvor-agent` как `docs/plans/2026-10-18-stvor-agent-go.md`.
> Спека: [docs/specs/2026-10-18-stvor-agent-go.md](../specs/2026-10-18-stvor-agent-go.md) (далее «спека», § — её разделы).
> Опись: [docs/research/2026-10-18-agent-split-inventory.md](../research/2026-10-18-agent-split-inventory.md).
> Контракт исполнителя — спека cream `docs/specs/2026-10-18-cream-core.md` (далее «спека cream»), MVP — её §17.
> Затрагивает два репозитория: **SA** — `stvor-agent` (Go-служба), **CE** — `cream-extensions` (расширения
> `teleport` и `stvor-agent`). Портал Stvor **не трогается**: он пинит `bisquite-teleport-agent` 3.1.0 со своего
> зеркала (ADR-0016), релиз 4.0.0 его не задевает (спека §14). `stvorhost` не трогается.
>
> **Исполнителю: после выполнения отметь свою задачу `- [x]` в Progress Tracker в том же коммите.**

## Progress Tracker
Волна A — SA, не зависит от cream:
- [ ] Task 1: Go-каркас SA, общие интерфейсы и типы (go.mod, Makefile, main.go)
- [ ] Task 2: `internal/portal` — клиент `/robot-api/v1/labels` и строгий разбор тела
- [ ] Task 3: `internal/token`, `internal/state`, `internal/atomicfile`
- [ ] Task 4: `internal/teleport` — exec-адаптер `cream run teleport …` на поддельном cream, файлы-образцы контракта
- [ ] Task 5: `internal/reconcile` — один тик
- [ ] Task 6: `internal/daemon` — цикл, джиттер, backoff, сигналы, блокировка
- [ ] Task 7: юнит `packaging/stvor-agent.service` и его тест
- [ ] Task 8: `internal/cli` и сборка команд в `cmd/stvor-agent`
- [ ] Task 9: `docs/test-map.md` — перенос проверок `tests/test-labels.sh`, доводка недостающих Go-тестов

Волна B — после релиза cream `v1.0.0`:
- [ ] Task 10: CE — инструменты проверки для `cream/v1` (`cream validate`, `check.sh`, Makefile)
- [ ] Task 11: CE — расширение `teleport`: перенос join/render/apps/os_logins/leave/scrub-token, хуки, юниты, `test-join.sh`
- [ ] Task 12: CE — `teleport`: управляемые и командные метки, `set-managed`, `managed-status`, `test-managed.sh`
- [ ] Task 13: CE — обёртка `stvor-agent` (пины по архитектурам, drop-in метки `stvor_agent`, `test-stvor-agent.sh`)
- [ ] Task 14: CE — путь `apps.d` у `code-server`/`selkies`, удаление `teleport-agent`, `agent-bump` на `stvor-agent`
- [ ] Task 15: SA — интеграционный тест адаптера с настоящим `cream` и фиктивным расширением

Волна C — упаковка, выпуск, чистка, приёмка:
- [ ] Task 16: SA — упаковка nfpm `.deb` amd64/arm64, сопровождающие скрипты, проверка воспроизводимости
- [ ] Task 17: SA — CI и release-please 4.0.0, dispatch бампа пина в CE
- [ ] Task 18: SA — документация (README, SECURITY, portal-labels-api, AGENTS/CLAUDE, индекс, STATUS)
- [ ] Task 19: SA — удаление bash-кода, юнитов, тестов и инструментов 3.x; сверка файлов-образцов с CE
- [ ] Task 20: SA — сквозной тест в контейнере Debian 12 с systemd (спека §15.3)
- [ ] Task 21: выпуск SA 4.0.0 и пин в обёртке CE (внешнее действие — с «да» владельца)
- [ ] Task 22: приёмка на тестовой ВМ `pve-robot-1` (спека §16)
- [ ] Task 23: Финальная интеграция + `make ci`

## Волны и параллельность
| Волна | Задачи (параллельно, ≤ 3 исполнителя, каждый в своём worktree) | Условие старта |
|---|---|---|
| A0 | 1 (один) | спека принята, ветка `feature/go-agent` от `main` SA |
| A1 | 2, 3, 4 | Task 1 в ветке |
| A2 | 5, 6, 7 | 2, 3, 4 слиты в ветку |
| A3 | 8 (один) | 5, 6, 7 |
| A4 | 9 (один) | 8 |
| B1 | 10, 11, 13 (CE) | релиз cream `v1.0.0` (спека cream §17); Task 1–9 не нужны |
| B2 | 12, 14 (CE); 15 (SA) | 10, 11 (для 12, 14); cream `v1.0.0` и Task 4 (для 15) |
| C1 | 16, 17, 18 (SA) | волна A; 15 |
| C2 | 19 (SA) | 16 (воспроизводимость зелёная), 12 (файлы-образцы в CE) |
| C3 | 20 (SA) | 16, 19; 12, 13 (снимок каталога) |
| C4 | 21, затем 22 | 20; «да» владельца на релиз; cream `v1.0.0` опубликован |
| C5 | 23 | всё |

Волна A и волна B1–B2 (CE) **независимы**: B1 может идти одновременно с A, если cream `v1.0.0` уже вышел, — но
не больше 3 исполнителей на всю машину (тяжёлое — `make ci`, сборка — по одному через `make heavy` или вручную по
очереди).

**Общие файлы — один владелец на волну:**
| Файл | Владелец |
|---|---|
| SA `go.mod`, `Makefile`, `cmd/stvor-agent/main.go`, `.golangci.yml` | A0 — Task 1; A3 — Task 8 (`main.go`); B2 — Task 15 (`Makefile`); C1 — Task 16 (`Makefile`); C2 — Task 19; C3 — Task 20 |
| SA `.github/workflows/ci-release.yml` | A0 — Task 1; C1 — Task 17; C3 — Task 20 |
| SA `docs/README.md`, `docs/STATUS.md` | C1 — Task 18; C5 — Task 23 |
| SA `docs/test-map.md`, добавления в `internal/**/*_test.go` | A4 — Task 9 (единственный исполнитель волны) |
| SA `Makefile` в A4 | Task 9 (подключение `tools/check-test-map.sh`) |
| CE `Makefile`, `tools/check.sh`, `tools/validate-extensions.py`, `.github/workflows/ci-release.yml` (ветка `feature/cream-v1-pilot`) | до конца волны B — только задачи этого плана: B1 — Task 10 (первым, остальные B1 ждут его слияния для регистрации в `check.sh`); B2 — Task 12 (регистрирует `check-contract-fixtures.sh`), затем Task 14 (после слияния 12); другие планы туда не пишут |
| CE `extensions/debian/teleport/files/teleport.sh` | B1 — Task 11; B2 — Task 12 |

Правила для исполнителей: `git add <пути>`, никогда `-A`; `git commit -m "…" -- <пути>`; коммитить целыми файлами
(lefthook прячет непроиндексированное в общий stash); чекбокс своей задачи отметить `- [x]` в этом плане в том же
коммите; нужен чужой файл — остановиться и доложить. Проверки — через агента `test-runner` (сводка, не сырой вывод).
Go-модуль: `github.com/iamletenkov/stvor-agent`. CE-работы — в пилотной ветке `feature/cream-v1-pilot`
(спека cream §16 шаг 2), не в `main` CE (см. «Риски», R2). Push веток и PR в `cream`, `cream-extensions`,
`stvor-agent` разрешены владельцем без отдельного «да»; «да» нужно только на публикацию релизов (Task 21 и релиз
cream 1.0.0).

---

## Волна A — SA, без cream

### Task 1: Go-каркас SA, общие интерфейсы и типы
- **Репозиторий:** SA. **Зависит:** —.
- **Цель:** Go-модуль по образцу `cream` (Makefile docker-first, golangci v2, `go.md`), который собирается рядом с
  bash-кодом 3.x и не ломает его проверок; общие типы, против которых параллельно пишут Task 2–7.
- **Файлы (создать):** `go.mod`; `.golangci.yml` (копия из cream); `Makefile` — цели `help`, `fmt`, `lint`
  (gofmt -l, go vet, golangci-lint в контейнере), `test` (`go test -race -count=1 ./...`), `build`, `build-all`
  (`CGO_ENABLED=0 -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X …/internal/version.Version=$(VERSION)"`),
  `legacy-check` (прежний `tools/check.sh`), `legacy-deb` (прежний `tools/build-deb.sh`), `ci` = lint + test +
  build-all + legacy-check + legacy-deb; `.gitignore` (`.cache/`, `dist/`);
  `cmd/stvor-agent/main.go` (`os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))`);
  `internal/version/version.go`; `internal/cli/cli.go` — заглушка `Run` с подкомандами `version [--short]`,
  `help`, остальное — «не реализовано», код 2;
  `internal/teleport/teleport.go` — интерфейс `Teleport { Status(ctx) (Status, error); SetManaged(ctx, Labels)
  (SetResult, error); CheckActions(ctx) error }`, типы `Status{Managed Labels; Pending, Joined, Active bool}`,
  `SetResult{Status; Result string}`, ошибки-классы `ErrLockBusy` (75), `ErrProtocol` (76), `ErrCream` (64–79);
  `internal/reconcile/result.go` — **зафиксированный** `type Result struct{ Outcome Outcome; HTTPStatus int;
  Apply string; TokenErr error }` (`Apply` — `""|applied|unchanged|deferred|unconfirmed|paused|error`), перечень
  `Outcome` (спека §7: `ok | not_modified | unauthorized | rate_limited | network | redirect | http_error |
  bad_body | skipped_locked` + `disabled`). Правило: backoff и счётчик неудач `n` — только в daemon (Task 6); тик
  (Task 5) сна не подсказывает, кроме паузы 3×/15 мин (выражена через `Apply=paused`, daemon её не учитывает в
  backoff); `last_poll` и `failures` в `state.json` пишет тик (Task 5); `internal/portal/types.go` — `Labels{Env, Space string}`, `Expected{Labels;
  Revision string}`; правка `.github/workflows/ci-release.yml` — в job `check` шаг `make lint test build-all`
  (прежние шаги остаются до Task 17).
- **Тесты первыми:** `internal/cli/cli_test.go` — `version --short` печатает `X.Y.Z` из ldflags, неизвестная
  подкоманда → 2; `internal/version` — умолчание `dev`.
- **Готово:** `make ci` зелёный (Go и legacy-проверки 3.x вместе).

### Task 2: `internal/portal` — клиент и строгий разбор
- **Репозиторий:** SA. **Зависит:** 1. **Волна:** A1.
- **Цель:** спека §5.4: `GET` с `Authorization`, `If-None-Match: "<rev>"`, `X-Applied-Revision` (только вместе с
  `If-None-Match` той же ревизии), `User-Agent`; только `https`, TLS ≥ 1.2, системные корни (пул — параметр
  конструктора для тестов), `ProxyFromEnvironment`, `CheckRedirect` — ошибка; тайм-ауты 10 с / 30 с;
  `LimitReader(4097)`; классификация ответа в `reconcile.Outcome`; разбор тела 200 по токенам
  `json.Decoder.Token()` (порядок ключей, единственность, без лишних ключей и хвоста, UTF-8, NUL — отказ,
  регулярные выражения `env`/`space`/`revision`). Тело 401 и ошибок не логируется и не возвращается.
- **Файлы:** `internal/portal/{client.go,parse.go,client_test.go,parse_test.go}` (+ `types.go` от Task 1 —
  владелец теперь Task 2).
- **Тесты первыми:** `httptest.NewTLSServer` — 200/304/401/429/500/302 (второй сервер не получил запрос),
  тайм-аут, тело 4096 и 4097 байт, `http://` URL отклонён до сети; таблица разбора (лишний ключ, повтор ключа,
  иной порядок, `"ENV"`, вложенный лишний объект, хвост, NUL, не-UTF-8, значения вне регулярок, пустое тело,
  пробелы между токенами допустимы); заголовки (кавычки в `If-None-Match`, `X-Applied-Revision` без
  `If-None-Match` невозможен по API); токен не попадает в текст ни одной возвращённой ошибки.
- **Готово:** `make lint test`.

### Task 3: `internal/token`, `internal/state`, `internal/atomicfile`
- **Репозиторий:** SA. **Зависит:** 1. **Волна:** A1.
- **Цель:** спека §5.5, §5.6, §6. `atomicfile.Write(path, data, mode)`: временный файл в том же каталоге → `fsync`
  → `rename` → `fsync` каталога. `token.Read(fs)` → `{URL, Token}` или класс ошибки
  `absent|bad_mode|bad_owner|corrupt` (0600, владелец 0, каталог 0700; URL по регулярке спеки §6; токен 43 знака
  `[A-Za-z0-9_-]`); `token.Write`, `token.Exists` (только `stat`, для `version --label`); `state.Load` — строгая
  схема 1 (неизвестная схема, лишние поля, значения не по регуляркам → «состояния нет» + причина для журнала);
  `state.Save`; `state.New(url)`. Файловая система и `stat` (uid/mode) — через интерфейс.
- **Файлы:** `internal/atomicfile/*`, `internal/token/*`, `internal/state/*`.
- **Тесты первыми:** права 0644/0640, владелец ≠ 0 (подменный `stat`), каталог 0755, битый формат (нет `url=`,
  лишняя строка, повтор ключа, http-URL, токен 42/44 знака), сбой между записью и `rename` не портит прежний
  файл, `state.json` с лишним полем/схемой 2/кривой ревизией → «нет состояния», круговая запись-чтение; ни одна
  ошибка не содержит значения токена.
- **Готово:** `make lint test`.

### Task 4: `internal/teleport` — exec-адаптер и файлы-образцы контракта
- **Репозиторий:** SA. **Зависит:** 1. **Волна:** A1.
- **Цель:** спека §5.7: `CreamTeleport` вызывает `/usr/bin/cream` (путь — параметр) без shell, окружение ровно
  `PATH=/usr/sbin:/usr/bin:/sbin:/bin`, `LANG=C.UTF-8`, stdin `/dev/null`; `managed-status` — тайм-аут 15 с,
  stdout ≤ 64 КиБ; `set-managed` — `run --lock-timeout 20s teleport set-managed env=<v> space=<v>`, тайм-аут 90 с
  (убийство группы процессов по тайм-ауту); строгий JSON схемы 1 (`DisallowUnknownFields` недостаточно — разбор
  по токенам или проверка повторов, как в Task 2); коды: 0/1 + JSON → результат, 2 + JSON (действие печатает
  объект и при коде 2 — контракт расширения) → ошибка ввода, 75 → `ErrLockBusy`, 76 → `ErrProtocol` (ошибка
  ввода/контракта, без счёта попыток), 64–79 → `ErrCream`; `CheckActions` — `cream actions teleport --json`,
  **мягкая**: три исхода — `ok`, `missing` (только при успешном разборе: нет `managed-status` или `set-managed` →
  тики без применения) и `unknown` (код ≠ 0 или ошибка разбора → строка журнала, применение продолжается). Форма
  вывода `actions --json` сверяется с golden-файлом задачи T15 плана cream до закрытия Task 15 этого плана.
  Фейк `teleportfake.Fake` для Task 5/8.
- **Файлы:** `internal/teleport/{cream.go,cream_test.go,parse.go}`, `internal/teleport/teleportfake/fake.go`,
  `internal/teleport/testdata/managed-status-{joined,pending,not-joined,inactive}.json`,
  `internal/teleport/testdata/set-managed-{applied,unchanged,deferred,unconfirmed}.json` — **эталон контракта**,
  CE копирует их побайтно (Task 12, сверка — Task 19).
- **Тесты первыми:** поддельный cream — тестовый бинарь в режиме помощника (`os.Args[0]` + переменная
  `STVOR_TEST_FAKE_CREAM`): пишет argv, окружение, stdin в файл и отвечает по сценарию; проверки: argv ровно как в
  §5.7, флаги cream до имени расширения, **токена нет в argv, окружении и stdin** (тест заводит токен в окружение
  родителя и проверяет, что дочерний его не видит), тайм-аут убивает процесс, не-JSON / чужая схема / лишний ключ /
  два объекта → ошибка, коды 1/2 (с JSON)/75/76/70, stdout > 64 КиБ, все файлы `testdata` разбираются;
  `CheckActions`: `ok`, `missing`, `unknown` на не-JSON и на код ≠ 0.
- **Готово:** `make lint test`.

### Task 5: `internal/reconcile` — один тик
- **Репозиторий:** SA. **Зависит:** 2, 3, 4. **Волна:** A2.
- **Цель:** псевдокод спеки §5.3 дословно; `Desired{Labels}` (§5.9); часы, портал, Teleport, хранилище токена и
  состояния — интерфейсы. Возвращает `reconcile.Result` (Task 1). Журнал — `*slog.Logger`
  параметром, одна строка на событие (`event=poll|apply|skip`), без секретов и тел. Сна не подсказывает (backoff — Task 6, кроме паузы 3×/15 мин); пишет `last_poll` и `failures` в `state.json`.
- **Файлы:** `internal/reconcile/{tick.go,tick_test.go}` (`result.go` — от Task 1, владелец теперь Task 5).
- **Тесты первыми** (фейковые часы и `teleportfake`, портал — фейк-интерфейс): сверка на 200 и на 304; ручная
  порча фактических меток при 304 → `set-managed`; `unconfirmed` → счётчик+время; 3 подряд при активном агенте →
  пропуск ≤ 15 мин со строкой `next_in`; без паузы при неактивном агенте и при `manual`; `deferred` не считается;
  `applied_revision` ставится только при `inEffect`; `X-Applied-Revision` уходит только при `report`; смена URL
  сбрасывает состояние; ошибка `managed-status` → ничего не применяется, `X-Applied-Revision` не уходит; 304 без
  запомненных меток → строка, конец; сеть/401/429/5xx/bad_body → T не вызывается; в `SetManaged` уходят ровно
  `env`, `space`; `ErrLockBusy`/`ErrProtocol` — неудача тика без счёта попыток; нет токена → `disabled`;
  `last_poll` (`at`, `outcome`, `http_status`) и `failures` записаны после каждого исхода; `CheckActions=missing` —
  без применения, `unknown` — применение идёт.
- **Готово:** `make lint test`.

### Task 6: `internal/daemon` — цикл службы
- **Репозиторий:** SA. **Зависит:** 1 (тип `reconcile.Result`), 3 не нужен. **Волна:** A2.
- **Цель:** спека §5.2: первый тик через U[0, 20 с); сон `интервал + U[0, 20 с)`; backoff
  `min(интервал·2^min(n,3), 600 с)` + джиттер, сброс на 200/304, 401 → 600 с; `SIGHUP` — тик сейчас (токен
  перечитывает сам тик), `SIGTERM`/`SIGINT` — дождаться конца тика, выйти 0; `flock` на `/run/stvor-agent.lock`
  (путь — параметр): служба — неблокирующе (`skipped_locked`, одна строка), CLI — ожидание ≤ 120 с; разовая мягкая
  `CheckActions` при старте (`missing` → тики без применения, `unknown` → строка журнала и обычная работа).
  Счётчик неудач `n` и backoff живут только здесь, по `Result.Outcome`. Интервал — из
  `STVOR_AGENT_INTERVAL` (60–600, вне диапазона → 60 со строкой), уровень — `STVOR_AGENT_LOG_LEVEL`.
- **Файлы:** `internal/daemon/{loop.go,backoff.go,lock.go,env.go,*_test.go}`.
- **Тесты первыми:** джиттер в границах (подменный источник случайности), таблица backoff по `n` и исходам,
  401 → 600 с, SIGHUP прерывает сон (канал сигналов — параметр), SIGTERM во время тика ждёт его конца, занятая
  блокировка → тик пропущен, CLI-блокировка ждёт и отдаёт тайм-аут, разбор переменных окружения.
- **Готово:** `make lint test`.

### Task 7: юнит службы
- **Репозиторий:** SA. **Зависит:** 1. **Волна:** A2.
- **Цель:** `packaging/stvor-agent.service` строго по спеке §9.1 и §6 (Type=exec, ExecStart, ConditionPathExists,
  network-online, Restart=always, RestartSec=10, KillMode=mixed, TimeoutStopSec=90, EnvironmentFile=-…,
  StateDirectory/StateDirectoryMode, SyslogIdentifier, hardening-список, без `ProtectSystem=`, UMask=0077).
- **Файлы:** `packaging/stvor-agent.service`, `packaging/unit_test.go` (`package packaging`, читает файл рядом).
- **Тесты первыми:** тест сверяет набор строк hardening, `ConditionPathExists`, `KillMode=mixed`,
  отсутствие `ProtectSystem`/`ReadWritePaths`. `systemd-analyze verify` — в Task 20 (контейнер с systemd).
- **Готово:** `make lint test`.

### Task 8: `internal/cli` и сборка команд
- **Репозиторий:** SA. **Зависит:** 5, 6, 7. **Волна:** A3 (один — владеет `main.go`).
- **Цель:** спека §8: `run`, `enable URL=…` (токен со stdin, одна строка, пробелы по краям срезаются; запись
  атомарная; другой адрес → сброс `state.json`; `systemctl enable` + `restart`; синхронный `sync`; отказ сети —
  0 со строкой), `disable` (`systemctl disable --now`, удалить токен и state; T не вызывается), `sync` (тик
  `manual`), `apply env= space=` (`SetManaged` без паузы, `state.json` не трогается), `status [--json]` (схема §7,
  в сеть не ходит; `service` — `systemctl is-active`), `version [--short|--label]` (`--label`: версия, если файл
  токена существует (`stat`), иначе пустая строка; код 0 всегда; блокировку не берёт). `KEY=VALUE`, повтор ключа —
  2; root для всего, кроме `version`/`help`/`status`. `systemctl` — интерфейс.
- **Файлы:** `internal/cli/*` (владелец после Task 1), `internal/systemd/{systemctl.go,fake.go}`,
  `cmd/stvor-agent/main.go`.
- **Тесты первыми:** коды выхода по таблице §8; `enable` со stdin пустым / двумя строками / неверной формой /
  пробелами; `enable` с другим URL сбрасывает state; `status --json` — схема 1 (золотой файл
  `internal/cli/testdata/status.json`), поле `token` по каждому классу; `version --label` с файлом и без, не
  открывает файл токена (подменная ФС считает `Open`); `apply` с `space` вне регулярки → 2; не-root → 1 с причиной.
- **Готово:** `make ci`.

### Task 9: таблица соответствия проверок
- **Репозиторий:** SA. **Зависит:** 8. **Волна:** A4 (один — может дописывать тесты в любой пакет).
- **Цель:** спека §15.2: `docs/test-map.md` — строка на каждую проверку `tests/test-labels.sh` (~101, 7 разделов:
  `set-managed`, `labels-enable`, `pull`, «R3: перезапуск подтверждается», «leave снимает метки», «join: набор
  ключей не изменился», «юниты таймера меток») → имя Go-теста **или** «переехало в T: `tests/test-managed.sh` /
  `test-join.sh` (CE, Task 11/12), проверка …» **или** «снято: <причина>». Недостающие Go-тесты — дописать здесь.
  Перечень проверок для T передать в Task 12 (раздел «Для CE» в конце таблицы).
- **Файлы:** `docs/test-map.md`, `internal/**/*_test.go` (только добавления).
- **Тесты первыми:** сама таблица; для каждой строки «Go-тест» — тест существует и проходит (скрипт
  `tools/check-test-map.sh`: имена из таблицы есть в `go test -list .`).
- **Готово:** `make ci`; в таблице нет пустых строк; `tools/check-test-map.sh` подключён в `make lint`
  (единственное изменение `Makefile` в волне A4).

---

## Волна B — после cream `v1.0.0`

### Task 10: CE — инструменты для `cream/v1`
- **Репозиторий:** CE, пилотная ветка `feature/cream-v1-pilot` (спека cream §16 шаг 2); выполняется первым в B1. **Зависит:** релиз cream `v1.0.0`. **Волна:** B1.
- **Цель:** каталог переходного вида проверяется целиком: `tools/validate-extensions.py` пропускает манифесты
  `schema: cream/v1`; новый `tools/cream-validate.sh` берёт `cream_<v>_amd64.deb` по пину (`tools/cream-pin.env`:
  версия + sha256) в контейнере `debian:12` и гоняет `cream validate --catalog .` по расширениям `cream/v1`;
  `tools/check.sh` запускает его и регистрирует будущие тесты `extensions/debian/teleport/tests/*.sh` и
  `tools/test-stvor-agent.sh` (если файла ещё нет — пропуск со строкой); CI CE — тот же `make ci`.
  Если пилот cream (`cloud-user`, `docker`) уже завёл эти инструменты — задача сводится к сверке и регистрации тестов.
- **Файлы:** `tools/{validate-extensions.py,cream-validate.sh,cream-pin.env,check.sh}`, `Makefile`,
  `.github/workflows/ci-release.yml`.
- **Тесты первыми:** фикстура `tools/testdata/cream-v1-min/` — минимальный валидный и заведомо невалидный манифест;
  `cream-validate.sh` зелёный на первом, красный на втором; `validate-extensions.py` не падает на `cream/v1`.
- **Готово:** `make ci` в CE.

### Task 11: CE — расширение `teleport`, перенос bash
- **Репозиторий:** CE (пилот). **Зависит:** cream `v1.0.0`; 10 — только для регистрации в `check.sh` (до слияния
  тесты гоняются напрямую). **Волна:** B1.
- **Цель:** спека §10.1–§10.6 без управляемых и командных меток: `extension.yaml` (`cream/v1`, `teleport` 4.0.0,
  `cream: ">=1.0.0"`, provides/conflicts `teleport-node`, `config.keys` §10.3 без `TELEPORT_MANAGED_LABELS`,
  `scope: install` для `TELEPORT_VERSION/SHA256/MIRROR/TARBALL`, действия с `lock`/`args`/`stdin`/`output`/`phases`
  по таблице §10.5, `files` — юниты); `hooks/{install,upgrade,remove,apply}`; тонкие `actions/*`;
  `files/teleport.sh` — перенос из `stvor-agent/src/bisquite-teleport` (join token/bound_keypair, `/webapi/find`,
  откат, 120 с ожидания `host_uuid`, разбор отказов, `join-pending`/`join-refused`, `join-retry`, `scrub-token`,
  `leave`, render узла и приложений, `apps.d` → `/etc/cream/teleport/apps.d/`, `status` текстом); пути
  `BISQUITE_TELEPORT_*` → `CREAM_*`; без `.join.lock` (блокировка cream), без `bisquite-conf` (ручки окружением,
  секрет — `cream config get --secret`, запись — вложенный `cream set --no-apply --allow-readonly teleport KEY=-`
  под унаследованной блокировкой: внутри `join` хук `apply` не запускается, render и (re)start делает сам `join`); render без
  `managed_by` и `bisquite_agent_version`; `files/os-logins`; юниты `teleport.service` (`ExecStartPre`/`ExecReload` =
  `/usr/bin/cream run teleport render`), `teleport-apps.{path,service}` (следит за `apps.d` и `command-labels.d`),
  `teleport-token.{path,service}`, `teleport-join.{service,timer}`; хук `install` — скачивание тарбола (перенос
  `teleport_url`, сверка `TELEPORT_SHA256`, «уже установлен», `TELEPORT_TARBALL`), отказ в `CREAM_PHASE=build` при
  готовой регистрации; `TELEPORT_DEFER=1`.
  Источник bash — read-only рабочее дерево SA (`main`), не правится.
- **Файлы:** `extensions/debian/teleport/**` кроме `files/` частей меток (их нет), `extensions/debian/teleport/tests/test-join.sh`,
  `extensions/debian/teleport/README.md`.
- **Тесты первыми:** `tests/test-join.sh` — перенос ~99 проверок `stvor-agent/tests/test-join.sh` на `CREAM_ROOT`,
  поддельные `systemctl`/`curl`/`teleport`/`journalctl`/`cream` (поддельный `cream set` пишет файл ручек);
  новые: хук `install` (сумма не та — отказ; уже установлен — пропуск; `TELEPORT_TARBALL` и `TELEPORT_MIRROR`
  вместе — отказ; build-фаза с регистрацией — отказ), секрет не в argv (`ps`-снимок поддельного `teleport`).
  shellcheck чистый.
- **Готово:** `bash extensions/debian/teleport/tests/test-join.sh` и `make lint` в CE; `cream validate` на
  расширении (через `tools/cream-validate.sh`, если Task 10 уже слит).

### Task 12: CE — управляемые и командные метки
- **Репозиторий:** CE (пилот). **Зависит:** 11 (владеет `teleport.sh` после него), 10. **Волна:** B2.
- **Цель:** спека §10.3, §10.5: ключ `TELEPORT_MANAGED_LABELS` (`list:re:…`, `readonly`, ≤ 16, зарезервированные
  ключи); render: `TELEPORT_LABELS` → поверх `TELEPORT_MANAGED_LABELS` → `env`; копия статических меток в каждое
  приложение; командные метки из `/etc/cream/teleport/command-labels.d/` по правилам §10.3 (ключ, коллизии,
  владелец root, права, ≤ 8 аргументов `[A-Za-z0-9._=-]+`, ≤ 8 файлов; нарушение — файл пропущен строкой журнала);
  `status` показывает перекрытые ключи и командные метки с файлом; `set-managed` (полная замена; маркер
  `managed-pending` до записи; `cream set --no-apply --allow-readonly` под унаследованной блокировкой; render в
  кандидат; restart с подтверждением по `InvocationID`/`MainPID`/`ActiveEnterTimestampMonotonic` ≤ 30 с — только
  при изменении итогового конфига / маркере / неактивном агенте; `applied|unchanged|deferred|unconfirmed`;
  коды 0/1/2; JSON в stdout **при любом из этих кодов, включая 2** — контракт расширения; запись ручки — вложенный
  `cream set --no-apply --allow-readonly` под унаследованной блокировкой); `managed-status` (`lock: none`, без
  побочных эффектов); `leave` стирает управляемые метки и маркер (тоже `--no-apply`). Файлы-образцы — побайтная
  копия `stvor-agent/internal/teleport/testdata/*.json` (Task 4) в `tests/fixtures/`. **Пин контракта — sha256
  содержимого набора файлов-образцов** (`tools/contract-fixtures.sha256`: sha256 от отсортированного списка
  «имя␠sha256 файла»), не коммит. `tools/check-contract-fixtures.sh` (CE) проверяет: (1) локальный набор
  совпадает с пином — всегда, без сети; (2) соседняя копия SA (`STVOR_AGENT_DIR`, по умолчанию `../stvor-agent`)
  даёт тот же набор — если копии нет: локально skip с предупреждением, в CI — checkout соседнего репозитория по
  ref (`STVOR_AGENT_REF`, по умолчанию `main`) отдельным шагом workflow. Task 12 сам регистрирует скрипт в
  `tools/check.sh` (владелец `check.sh` в B2 до Task 14).
- **Файлы:** `extensions/debian/teleport/{extension.yaml,files/teleport.sh,actions/set-managed,actions/managed-status,
  tests/test-managed.sh,tests/fixtures/*}`, `tools/check-contract-fixtures.sh`, `tools/contract-fixtures.sha256`,
  `tools/check.sh`, `.github/workflows/ci-release.yml` (шаг checkout SA).
- **Тесты первыми:** `tests/test-managed.sh` по спеке §10.7 + строки «переехало в T» из `docs/test-map.md` SA
  (Task 9): перекрытие ключей, `env`, копия в каждое приложение, зарезервированные ключи; командные метки —
  допустимый файл, коллизия со статической, путь не root / с записью группе, лишние аргументы, 9-й файл;
  `set-managed` — маркер до записи, подтверждение по каждому из трёх свойств юнита, «restart принят, нового
  запуска нет» → `unconfirmed`, «умер между записью и restart» → следующий вызов рестартует, конфиг не изменился →
  без restart (`applied` при первой записи после `join`, иначе `unchanged`), `deferred` без регистрации, код 2
  с JSON в stdout; вложенный `cream set` вызван с `--no-apply` (журнал поддельного cream); пин набора образцов
  совпадает, порча байта образца → `check-contract-fixtures.sh` красный;
  `managed-status` против файлов-образцов (сравнение JSON побайтно после нормализации порядка — порядок задаёт
  эталон); `leave`.
- **Готово:** оба теста расширения и `make ci` в CE.

### Task 13: CE — обёртка `stvor-agent`
- **Репозиторий:** CE (пилот). **Зависит:** cream `v1.0.0`; файлы SA не нужны (до релиза 4.0.0 пин — фиктивный
  тестовый, помечен в README). **Волна:** B1.
- **Цель:** спека §11: `extension.yaml` (`stvor-agent` 4.0.0, arch amd64/arm64, `targets {build: exec, live: "yes"}`,
  `depends: teleport >=4.0.0 <5`, `config.keys`: `scope: install` — `STVOR_AGENT_VERSION`, `STVOR_AGENT_SHA256`,
  `STVOR_AGENT_MIRROR`, `STVOR_AGENT_DEB`; `scope: config` — `STVOR_AGENT_INTERVAL` 60–600, `STVOR_AGENT_LOG_LEVEL`);
  хуки `install`/`upgrade` (источник → sha256 → `dpkg -i`; версия без суммы или наоборот — отказ; пин из
  `files/pin.env`), `remove` (`stvor-agent disable; dpkg -r`), `apply` (`systemctl --no-block try-restart`);
  `files/pin.env`; `files/stvor_agent.command-label` → `/etc/cream/teleport/command-labels.d/stvor_agent`.
- **Файлы:** `extensions/debian/stvor-agent/**`, `tools/test-stvor-agent.sh`.
- **Тесты первыми:** `tools/test-stvor-agent.sh` (порт идей `test-teleport-agent.sh`): выбор источника (зеркало
  плоско `<base>/stvor-agent_<v>_<arch>.deb`, файл, GitHub), сверка суммы, отказ при неполном пине, явные версия и
  сумма побеждают пин, токена нет в argv/окружении `dpkg`/`stvor-agent`, drop-in на месте после install и убран
  `remove` (через `cream` фикстуры или проверку `files` манифеста).
- **Готово:** `bash tools/test-stvor-agent.sh`, `make lint` в CE.

### Task 14: CE — `apps.d` соседей, удаление `teleport-agent`, `agent-bump` на `stvor-agent`
- **Репозиторий:** CE (пилот). **Зависит:** 10, 11, 13, 12 (слит — владение `check.sh` переходит к 14). **Волна:** B2
  (после Task 12; владелец `check.sh`, `Makefile` CE до конца волны B).
- **Цель:** `code-server/teleport-app.sh` и `selkies/teleport-app.sh` пишут в `/etc/cream/teleport/apps.d/`;
  удалить `extensions/debian/teleport-agent/` и `tools/test-teleport-agent.sh`; `tools/agent-bump.sh` и
  `.github/workflows/agent-bump.yml` — на релизы `iamletenkov/stvor-agent`: из `SHA256SUMS` берутся суммы
  `stvor-agent_<v>_amd64.deb` и `_arm64.deb`, правится `extensions/debian/stvor-agent/files/pin.env`, patch-подъём
  версии расширения, `tools/check.sh`, PR; `tools/check.sh` и `Makefile` без удалённого.
- **Файлы:** `extensions/debian/{code-server,selkies}/teleport-app.sh`, `extensions/debian/teleport-agent/` (удаление),
  `tools/{agent-bump.sh,test-teleport-agent.sh,check.sh,test-agent-bump.sh}`, `.github/workflows/agent-bump.yml`,
  `Makefile`.
- **Тесты первыми:** `tools/test-agent-bump.sh`: фиктивный `SHA256SUMS` с двумя `.deb` → пин и версия обновлены;
  нет одной архитектуры → отказ; релиз старше пина → «нечего делать»; та же версия с другой суммой → отказ;
  проверка путей `apps.d` в существующих тестах `code-server`/`selkies` (если их нет — `grep`-проверка в `check.sh`
  не заводится, достаточно ручного сравнения в ревью).
- **Готово:** `make ci` в CE. Слияние в `main` CE — только вместе с переводом каталога (риск R2).

### Task 15: SA — адаптер против настоящего cream
- **Репозиторий:** SA. **Зависит:** cream `v1.0.0`, Task 4. **Волна:** B2 (владелец `Makefile` SA в B2).
- **Цель:** доказать, что argv, коды и вывод из Task 4 совпадают с настоящим `cream run`: фиктивное расширение
  `test/integration/catalog/teleport-fake/` (`cream/v1`; действия `managed-status`, `set-managed` отдают файлы-образцы
  из `internal/teleport/testdata`, по сценарию — код 1, не-JSON, долгий сон) + контейнер `debian:12` с
  `cream_<v>_amd64.deb` по пину (`test/integration/cream-pin.env`); Go-тест с тегом сборки `integration`
  гоняет `CreamTeleport` против `/usr/bin/cream`; сценарий «блокировка занята» (параллельный `cream run teleport
  hold` под `exclusive`) → код 75 → `ErrLockBusy`; `CheckActions` видит оба действия.
- **Файлы:** `test/integration/**`, `internal/teleport/integration_test.go` (`//go:build integration`),
  `Makefile` (цель `test-integration`, в `ci` не входит до Task 20).
- **Тесты первыми:** сам интеграционный тест.
- **Готово:** `make test-integration` зелёный, `make ci` зелёный.

---

## Волна C — упаковка, выпуск, чистка, приёмка

### Task 16: SA — упаковка
- **Репозиторий:** SA. **Зависит:** волна A, 7. **Волна:** C1 (владелец `Makefile` SA в C1).
- **Цель:** спека §9.1–§9.2: `nfpm.yaml` (пакет `stvor-agent`, `Architecture` по сборке, `Depends: cream (>= 1.0.0)`,
  файлы `/usr/bin/stvor-agent` 0755, юнит в `/usr/lib/systemd/system/`, `/usr/share/doc/stvor-agent/{copyright,NOTICE}`,
  скрипты `packaging/{postinst,prerm,postrm}` — `daemon-reload` только при работающем systemd, `enable`,
  `try-restart` при наличии токена; `prerm remove` — `disable --now`; `postrm purge` — удалить `/etc/stvor-agent`,
  `/var/lib/stvor-agent`; без systemd — только ссылка `enable`); nfpm — бинарь по пину версии и sha256 в образе
  сборки (`packaging/Dockerfile.build` или скачивание в `.cache/` по пину); `SOURCE_DATE_EPOCH` = время коммита;
  цели `make deb` (оба `.deb` + `dist/SHA256SUMS`) и `make repro` (две сборки подряд — равные суммы), `ci` =
  lint + test + build-all + deb + repro + legacy-check. Если nfpm не даёт побайтной воспроизводимости — откат на
  `tools/build-deb.sh` с шаблоном `Architecture` (решение по зелёному `make repro`, записать в README).
- **Файлы:** `nfpm.yaml`, `packaging/{postinst,prerm,postrm,copyright}`, `packaging/package_test.go`, `Makefile`,
  `tools/nfpm.pin` (или аналог).
- **Тесты первыми:** `packaging/package_test.go` (`//go:build package`, запускается `make deb-test`): распаковать
  оба `.deb` (`dpkg-deb` в контейнере) — состав, права, `Depends`, `Architecture`, отсутствие `/etc/bisquite`;
  shellcheck сопровождающих скриптов; сценарии скриптов на подменном `systemctl` (есть/нет systemd, есть/нет
  токена, `remove`/`purge`) — перенос идей `tests/test-package.sh`.
- **Готово:** `make ci` (с `repro`).

### Task 17: SA — CI и release-please 4.0.0
- **Репозиторий:** SA. **Зависит:** 16 (имена целей и артефактов — заранее зафиксированы здесь: `make ci`, `make deb`,
  `dist/stvor-agent_<v>_{amd64,arm64}.deb`, `dist/SHA256SUMS`). **Волна:** C1 (параллельно с 16).
- **Цель:** спека §9.2: `ci-release.yml` — gitleaks + `make ci` на PR и `main`; `release-artifacts` собирает из
  тега с `VERSION=<тег без v>` и проверяет, что `stvor-agent version --short` из собранного бинаря равен тегу;
  прикладывает два `.deb` и `SHA256SUMS`; `dispatch-extensions` — как сейчас, но под переменной репозитория
  `EXTENSIONS_BUMP_ENABLED` (не `true` → предупреждение и пропуск: до слияния Task 14 в `main` CE agent-bump там
  ещё старый и упадёт на имени `.deb`); job `check` делает checkout `iamletenkov/cream-extensions` по ref
  (`CE_CONTRACT_REF`, по умолчанию `feature/cream-v1-pilot`, затем `main`) рядом, чтобы `make ci` сверил
  файлы-образцы (Task 19); release-please: `release-type: go`, `package-name: stvor-agent`, без
  `version-file`, манифест `.` → `3.1.0`; файл `VERSION` удаляется; первый Go-релиз — 4.0.0 (коммит слияния ветки —
  `feat!:` с `BREAKING CHANGE:` или `Release-As: 4.0.0` в теле).
- **Файлы:** `.github/workflows/ci-release.yml`, `.github/release_please/*.json`, `VERSION` (удаление),
  `.claude/skills/release/SKILL.md` (процедура выпуска 4.x).
- **Тесты первыми:** `actionlint` в контейнере по пину (`Makefile` в C1 у Task 16 — поэтому запуск вручную, вывод в
  отчёте; цель `make actionlint` добавляет Task 19); dry-run release-please (`release-please release-pr --dry-run`
  в контейнере) показывает 4.0.0.
- **Готово:** actionlint чистый (вывод в отчёте), `make ci` зелёный; job `check` проходит на черновом PR.

### Task 18: SA — документация
- **Репозиторий:** SA. **Зависит:** волна A (поведение), 16/17 — только имена. **Волна:** C1.
- **Цель:** `README.md` (что такое агент, установка через cream и без, команды §8, журнал, runbook прокси
  drop-in `HTTPS_PROXY`), `SECURITY.md` (токен, root, hardening, цепочка поставки §6), `docs/portal-labels-api.md`
  (клиент — `stvor-agent`; контракт не меняется), `AGENTS.md`/`CLAUDE.md` (карта репозитория Go, курс выполнен,
  DoD `make ci`), `docs/README.md` (строки плана, test-map), `docs/STATUS.md`.
- **Файлы:** перечисленные.
- **Тесты первыми:** — (документация); проверка ссылок — ручная, упомянутые `make`-цели существуют (`make help`).
- **Готово:** `make lint` (gitleaks/commitlint на коммите), ревью reviewer.

### Task 19: SA — удаление 3.x и сверка контракта с CE
- **Репозиторий:** SA. **Зависит:** 16 (`make repro` зелёный), 12 (файлы-образцы в CE), 9. **Волна:** C2 (один).
- **Цель:** спека §14: удалить `src/`, `lib/`, `debian/`, `tests/test-{labels,join,conf,package}.sh`,
  `tools/check.sh`, `tools/build-deb.sh` (если nfpm прошёл), `dist/*` 3.x из дерева, если отслеживаются;
  `Makefile` — без `legacy-*`, плюс цель `actionlint` в `lint`; `.claude/rules/shell.md` — оставить, только если остаются shell-скрипты
  (`packaging/*`, `tools/*.sh`); `lefthook.yml` — добавить gofmt-проверку staged `*.go`;
  `tools/check-contract-fixtures.sh` (SA) в `make ci`, **без сети**: (1) набор `internal/teleport/testdata/*.json`
  совпадает с пином `tools/contract-fixtures.sha256` (sha256 содержимого набора, та же формула, что в CE Task 12);
  (2) соседняя копия CE (`CREAM_EXTENSIONS_DIR`, по умолчанию `../cream-extensions`) несёт тот же набор в
  `extensions/debian/teleport/tests/fixtures/`; нет копии — локально skip с предупреждением, в CI копию даёт
  checkout по ref (Task 17).
- **Файлы:** удаляемые, `Makefile`, `lefthook.yml`, `tools/check-contract-fixtures.sh`,
  `tools/contract-fixtures.sha256`.
- **Тесты первыми:** `check-contract-fixtures.sh` красный при изменённом байте образца или расхождении с соседней
  копией, skip без копии, зелёный на пине (вывод в отчёте).
- **Готово:** `make ci`; `git grep -n 'bisquite'` даёт только CHANGELOG/историю и `portal-labels-api.md`.

### Task 20: SA — сквозной тест в контейнере с systemd
- **Репозиторий:** SA. **Зависит:** 16, 19; CE 12, 13 (снимок каталога пилотной ветки). **Волна:** C3 (один,
  тяжёлый — с предупреждением остальным).
- **Цель:** спека §15.3: контейнер Debian 12 с systemd; `cream` по пину + `cream catalog pack` пилотной ветки CE
  (`file://`-архив по пину коммита) + `teleport` (поддельный `teleport`-бинарь, без кластера) + `stvor-agent` из
  `dist/` против `httptest`-портала (маленький Go-бинарь `test/e2e/fakeportal`): `join` → `enable` → первый тик
  `set-managed` без restart → портал меняет `space` → restart подтверждён (`InvocationID` сменился) →
  `X-Applied-Revision` на следующем тике; установка обёртки кладёт drop-in, render добавляет команду `stvor_agent`,
  `teleport.service` получил reload (тот же `InvocationID`); `set-managed` во время restart не ждёт `ExecStartPre`
  (тупик спеки cream §7.3); `systemd-analyze verify` и `systemd-analyze security stvor-agent.service` в лог.
  Цель `make e2e`; в CI — отдельный job (по одному, не на каждый PR — на `main` и вручную).
- **Файлы:** `test/e2e/**`, `Makefile` (цель `e2e`), `.github/workflows/ci-release.yml` (job `e2e`).
- **Тесты первыми:** сам сценарий.
- **Готово:** `make e2e` зелёный (вывод через `test-runner`), `make ci` зелёный.

### Task 21: выпуск SA 4.0.0 и пин в обёртке
- **Репозиторий:** SA, затем CE (пилот). **Зависит:** 20, слияние `feature/go-agent` в `main` SA. **Волна:** C4.
- **Цель:** release-please открывает PR «release 4.0.0»; **слияние релизного PR (публикация релиза) — только с
  прямого «да» владельца**; push веток и PR разрешены без него. После релиза — `tools/agent-bump.sh v4.0.0 SHA256SUMS` в пилотной
  ветке CE вручную (dispatch выключен переменной до слияния CE в `main`, Task 17) → `files/pin.env` обёртки.
- **Файлы:** CE `extensions/debian/stvor-agent/files/pin.env`, `extension.yaml` (версия).
- **Тесты первыми:** `tools/test-stvor-agent.sh` с реальным пином.
- **Готово:** релиз 4.0.0 с двумя `.deb` и `SHA256SUMS`; `sha256sum -c` по скачанным; `make ci` в CE.

### Task 22: приёмка на `pve-robot-1`
- **Репозиторий:** — (отчёт в `docs/research/2026-10-…-stvor-agent-acceptance.md` SA). **Зависит:** 21; cream
  `v1.0.0` опубликован (`.deb` с релиза), снимок каталога CE — `cream catalog pack` пилотной ветки по пину коммита
  (`file://`-архив, как приёмка пилота спеки cream §16 шаг 2). **Приёмка требует cream и расширений с релизов /
  зафиксированных снимков, не из рабочих деревьев.** **Волна:** C4.
- **Цель:** (приёмка на тестовой ВМ разрешена владельцем без отдельного «да»; `stvorhost` — нет) переустановить
  `pve-robot-1` на PVE-ноде (VMID из разрешённого диапазона, после себя — убрать
  временные ВМ); поставить вручную командами спеки §11 (портал не меняется, поэтому вместо «одной команды портала» —
  те же шаги вручную; токен меток — из карточки робота портала, путь `labels-enable` портала даёт его же);
  проверить спеку §16: п. 1–2 (ссылка на Task 23 и 9); п. 3 — узел несёт `space`, `env`, `stvor_agent=4.0.0`,
  перенос робота в другое пространство и обратно сверяется порталом ≤ 2 мин, узел и приложение меняют `space`
  вместе; п. 6 — установка обёртки при открытой `tsh ssh` сессию не рвёт, метка в `cmd_labels` (`tctl get nodes`)
  ≤ 5 мин, обновление на тестовую 4.0.x не меняет `InvocationID`; `systemd-analyze security`; живой `set-managed`
  из-под службы. **Вне этой приёмки** (ждут среза Stvor по ADR-0030): «одна команда портала», п. 4 (`arm64-gate`),
  распознавание `stvor_agent` в карточке. П. 5 — убедиться, что `stvorhost` (3.0.1) не тронут и по-прежнему
  получает метки (только чтение: карточка в портале). `stvorhost` и tbot не трогать.
- **Готово:** отчёт с выводами команд; строка в `docs/README.md` SA.

### Task 23: Финальная интеграция + `make ci`
- **Репозиторий:** SA и CE. **Зависит:** всё.
- **Цель:** сквозной `make ci` в SA (`feature/go-agent` после слияния всех веток исполнителей) и `make ci` в CE
  (пилотная ветка); `make e2e` один раз; `docs/test-map.md` без пустых строк; `docs/STATUS.md` SA; Progress Tracker
  сверен с коммитами; reviewer по финальным веткам обоих репозиториев.
- **Готово:** вывод `make ci` обоих репозиториев и `make e2e` — зелёные.

---

## Рулинги контроллера (закрывают вопросы плана без паузы)
1. **CE-работы — в пилотной ветке `feature/cream-v1-pilot`**, не в `main` CE: каталог в `main` — layout 2, его
   потребитель bisquite ждёт `teleport-agent` до шага 4 спеки cream §16. Слияние в `main` CE — вместе с переводом
   каталога (шаг 3 спеки cream, другой план). До конца волны B `tools/check.sh` и `Makefile` в этой ветке пишут
   только задачи этого плана (Task 10 первым, затем 12, 14); другие планы туда не пишут.
2. **Dispatch бампа** после релиза 4.0.0 выключен переменной `EXTENSIONS_BUMP_ENABLED` до слияния Task 14 в `main` CE;
   пин обёртки в пилоте ставится вручную `tools/agent-bump.sh` (Task 21).
3. **Приёмка без портала** — ручные команды спеки §11; «одна команда портала» и `arm64-gate` — в срезе Stvor.
4. **Файлы-образцы контракта** — эталон в SA (`internal/teleport/testdata`), CE копирует; пин — sha256 содержимого
   набора (не коммит); сверка без сети по соседней локальной копии, в CI — checkout соседнего репозитория по ref.
6. **Push веток и PR** в `cream`, `cream-extensions`, `stvor-agent` разрешены; «да» владельца — только на
   публикацию релизов (Task 21, релиз cream 1.0.0). Приёмка на `pve-robot-1` разрешена; `stvorhost` не трогать.
7. **Проверка действий при старте — мягкая**: `unknown` не останавливает применение, `missing` — только при
   успешном разборе `cream actions --json`.
5. **bash 3.x живёт в ветке до Task 19**: `make ci` гоняет и Go, и legacy-проверки, чтобы ветка была зелёной на
   каждом коммите.

## Риски исполнения
| # | Риск | Что делаем |
|---|---|---|
| R1 | cream `v1.0.0` задерживается или меняет контракт (`run`, коды 75/76, `output: json`, `lock: none`, `--allow-readonly`, `CREAM_LOCK_FD`) — волны B и C стоят | волна A не зависит; Task 4 пишется строго по спеке cream §7.2/§11.3, Task 15 ловит расхождение первым; при изменении контракта — правка спеки SA и Task 4, не обход |
| R2 | CE `main` в layout 2: `validate-extensions.py` и bisquite ломаются на `cream/v1` и на удалении `teleport-agent` | пилотная ветка (рулинг 1), Task 10 учит валидатор пропускать `cream/v1`; удаление `teleport-agent` не попадает в `main` до шага 3 спеки cream |
| R3 | Релиз 4.0.0 вызывает старый `agent-bump` в CE `main` (ищет `bisquite-teleport-agent_*_all.deb`) — красный dispatch или неверный PR | переменная `EXTENSIONS_BUMP_ENABLED` (рулинг 2); публикация релиза — только с «да» владельца |
| R4 | Перенос ~99 проверок `test-join.sh` на новые пути «почти как есть» прячет изменённое поведение (секрет через `cream config get --secret`, блокировка cream вместо `.join.lock`) | Task 11: новые проверки на секрет не в argv и на работу без собственной `flock`; ревьюер сверяет счёт проверок до/после |
| R5 | Тупик `set-managed` → restart → `ExecStartPre=cream run teleport render`, если где-то останется `lock` ≠ `none` | `cream validate` предупреждает (спека cream §7.3); Task 20 воспроизводит сценарий живьём |
| R6 | nfpm не даёт побайтной воспроизводимости | `make repro` в Task 16 решает; откат на `tools/build-deb.sh` с шаблоном `Architecture` |
| R7 | Тесты с `flock`, сигналами и под-процессами флакают под `-race` на нагруженной ВМ | сигналы и часы — каналы/интерфейсы, реальные процессы только в Task 4/15; `-count=1`; тяжёлые прогоны по одному |
| R8 | Параллельные исполнители правят общий `Makefile`/`go.mod` | таблица владельцев; новые зависимости запрещены (только stdlib, спека §5.1) — `go.mod` после Task 1 не меняется |
| R9 | Сквозной тест с systemd в контейнере (privileged, cgroup) нестабилен в GitHub Actions | `make e2e` отдельным job на `main`/вручную; обязательный — локально в Task 20 и 23 |
| R10 | Приёмка на `pve-robot-1` случайно задевает `stvorhost`/tbot или портал | Task 22 — только `pve-robot-1`; портал — только чтение карточки и выдача токена штатным путём |

## Что ждёт от cream
Соответствие задачам плана cream (`cream/docs/plans/2026-10-18-cream-core.md`, номера — его задачи T*). Волна B
стартует после релиза cream `v1.0.0` (T23 плана cream); отдельные задачи этого плана можно начать раньше, когда
слита соответствующая задача cream (пилотная ветка, `cream` из сборки по коммиту).

| Нужно | Раздел спеки cream | Задачи плана cream | Кто здесь ждёт |
|---|---|---|---|
| Формат `cream/v1` (манифест, `config.keys`, `depends` с диапазоном, `provides`/`conflicts`, действия), `cream validate`, `cream catalog pack` | §4, §7.1, §8.2 | T4, T10, T17 (+T7) | 10, 11, 12, 13, 15, 20 |
| `cream run`, `cream actions --json`, коды 0–63 насквозь, 64–79 (75, 76), флаги до имени расширения | §7.2, §11.3 | T15 (+T1, T8); golden `actions --json` — T15 | 4 (сверка формы до закрытия 15), 15, 20 |
| Блокировка `lock/<ext>`, `CREAM_LOCK_FD`, вложенный `cream set` под унаследованной блокировкой | §6.3 | T6, T8, T12, T15, T19 | 11, 12, 15, 20 |
| `cream set --allow-readonly`, `--no-apply`, `KEY=-`, `cream config get --secret`, хук `apply` | §8.3, §8.4 | T5, T12 | 11, 12, 13 |
| `install`/`upgrade`/`remove` с хуками, повтор install (`scope: install`), отказ `remove` при зависимых, `CREAM_PHASE` | §6.1–§6.5, §8.5 | T13, T14, T11, T16 | 11, 13, 20, 22 |
| Источник `archive` (`file://`), `--sha256`, `--replace --sync` | §9.1, §9.2 | T7, T17 | 20, 22 |
| `cream_<v>_<arch>.deb`, релиз `v1.0.0` | §15, §17 | T18, T20, T23 | 10, 15, 16 (`Depends`), 20, 22 |
| Интеграционная фикстура `lock-restart` (тупик через systemd) | §14, §7.3 | T10, T19 | 20 (дублирует живьём) |
| `files` расширения в каталог другого расширения (drop-in `command-labels.d/stvor_agent`, удаление при `remove`) | §4.2, §6.5 | T9, T13 + фикстура в T19 (запрошена у плана cream) | 13, 20, 22 |
