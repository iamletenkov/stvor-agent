# Карта переноса проверок `tests/test-labels.sh` (3.x) в Go

> 2026-10-18, Task 9 плана [stvor-agent на Go](plans/2026-10-18-stvor-agent-go.md), спека §15.2
> ([specs/2026-10-18-stvor-agent-go.md](specs/2026-10-18-stvor-agent-go.md)).

Строка на каждую проверку `tests/test-labels.sh` — так, как её считает сам скрипт (`проверок: 229`): циклы
развёрнуты (коды 401/429/500/down — по 6 проверок, девять битых тел — по 4, пять файлов `leave`). «Стр.» — номер
строки вызова `check`/`check_not`/`eq` в `tests/test-labels.sh` (у развёрнутых циклов он повторяется).

**Итог:**
- **Go** — проверка перенесена в Go-тест: `пакет.TestИмя` или `пакет.TestИмя/подтест` (подтест — имя из таблицы
  теста, до замены пробелов на `_`); в скобках — что именно проверяет тест, если имя не говорит само.
- **T** — проверка переехала в расширение `teleport` каталога cream-extensions: `tests/test-managed.sh` (Task 12)
  или `tests/test-join.sh` (Task 11); описание — что там проверить. Перечень для CE — [в конце](#для-ce).
- **Go+T** — обе стороны: механизм в T, политика или ручной путь в S.
- **снято** — проверки больше нет, с причиной (обычно — функции нет: ручка `TELEPORT_LABELS_URL`, таймер меток);
  если смысл проверки живёт в другом виде, рядом — тест, который его держит.

Сводка: 229 проверок — Go 151, Go+T 27, T 31, снято 20. Недостававшие Go-тесты дописаны в Task 9:
`cli.TestEnableRefusalsTouchNothing`, `cli.TestEnableTokenOnlyInTokenFile`, `cli.TestApplyRefusesBadInput`
(`internal/cli/ported_test.go`), `reconcile.TestTickLogsAttemptNumber` (`internal/reconcile/ported_test.go`).

**Проверка карты** — `tools/check-test-map.sh`, входит в `make lint`: у каждой строки есть итог и «куда», каждое имя
Go-теста есть в `go test -list . ./...`, подтест — строкой в `.go`-файлах пакета; пока `tests/test-labels.sh` в
дереве, номера строк его проверок и номера строк карты совпадают (после удаления 3.x в Task 19 эта сверка
пропускается, карта остаётся справкой).

Сокращения в описаниях: S — `stvor-agent` (Go), T — расширение `teleport`; render, `set-managed`,
`managed-status`, `leave` — действия T (спека §10.5); маркер `managed-pending` — бывший `labels-restart-pending`.

### `set-managed` (строки 179–240)

| № | Стр. | Проверка (bash) | Итог | Куда |
|---|---|---|---|---|
| 1 | 181 | set-managed: слияние — код 0 | Go+T | T: `tests/test-managed.sh`: `set-managed` пишет управляемые метки, код 0; ручной путь S — `cli.TestApply/applied` |
| 2 | 182 | set-managed: метки оператора сохранены, space заменён | T | T: `tests/test-managed.sh`: render — управляемый `space` перекрывает операторский, остальные метки оператора целы |
| 3 | 183 | set-managed: TELEPORT_ENV | T | T: `tests/test-managed.sh`: render — управляемый `env` перекрывает `TELEPORT_ENV` |
| 4 | 185 | set-managed: space у узла и у каждого приложения | T | T: `tests/test-managed.sh`: render — копия управляемых меток в каждое приложение |
| 5 | 186 | set-managed: старого space в teleport.yaml нет | T | T: `tests/test-managed.sh`: render — прежнего значения в `teleport.yaml` нет |
| 6 | 187 | set-managed: env у узла и приложений | T | T: `tests/test-managed.sh`: render — `env` у узла и приложений |
| 7 | 188 | set-managed: ровно один restart, без reload | Go+T | T: `tests/test-managed.sh`: restart только при изменении итогового конфига, ровно один; S зовёт `set-managed` ровно раз — `reconcile.TestTick200Applies` |
| 8 | 189 | set-managed: хук apply не вызывался | T | T: `tests/test-managed.sh`: `set-managed` пишет ручку без хука `apply` (спека §10.5) |
| 9 | 193 | set-managed: без изменений — код 0 | Go+T | T: `tests/test-managed.sh`: итог `unchanged`; S — `cli.TestApply/unchanged`, `teleport.TestSetManagedResults/unchanged` |
| 10 | 194 | set-managed: без изменений — systemctl не вызывается | T | T: `tests/test-managed.sh`: итоговый конфиг не изменился — без restart |
| 11 | 195 | set-managed: без изменений — config тот же | T | T: `tests/test-managed.sh`: конфиг не переписан |
| 12 | 197 | set-managed: только space | T | T: `tests/test-managed.sh`: `set-managed` с частью ключей; S всегда шлёт ровно `env` и `space` — см. `teleport.TestCleanCall` |
| 13 | 198 | set-managed: только space — env прежний | T | T: `tests/test-managed.sh`: частичная запись не трогает `env` |
| 14 | 199 | set-managed: только space — метки | T | T: `tests/test-managed.sh`: частичная запись `space` |
| 15 | 203 | set-managed: чужой ключ app — отказ | Go+T | `cli.TestApplyRefusesBadInput/foreign key app`, `teleport.TestSetManagedRejectsBadLabels`; T: `tests/test-managed.sh`: код 2 на ввод |
| 16 | 204 | set-managed: чужой ключ вместе с допустимым — отказ | Go+T | `cli.TestApplyRefusesBadInput/foreign key with valid ones`, `cli.TestUsageErrors/apply unknown key`; T: `tests/test-managed.sh`: код 2 на ввод |
| 17 | 205 | set-managed: ключ ручки — отказ | Go+T | `cli.TestApplyRefusesBadInput/knob key`; T: `tests/test-managed.sh`: код 2 на ввод |
| 18 | 206 | set-managed: без аргументов — отказ | Go+T | `cli.TestApplyRefusesBadInput/no args`, `cli.TestUsageErrors/apply without space`; T: `tests/test-managed.sh`: код 2 на ввод |
| 19 | 207 | set-managed: space=A"b — отказ | Go+T | `cli.TestApplyRefusesBadInput/quote in space`; T: `tests/test-managed.sh`: код 2 на ввод |
| 20 | 208 | set-managed: пустой space — отказ | Go+T | `cli.TestApplyRefusesBadInput/empty space`, `cli.TestUsageErrors/apply env empty`; T: `tests/test-managed.sh`: код 2 на ввод |
| 21 | 209 | set-managed: перевод строки в значении — отказ | Go+T | `cli.TestApplyRefusesBadInput/newline in value`; T: `tests/test-managed.sh`: код 2 на ввод |
| 22 | 210 | set-managed: заглавные — отказ | Go+T | `cli.TestApplyRefusesBadInput/upper case`, `cli.TestUsageErrors/apply space out of alphabet`; T: `tests/test-managed.sh`: код 2 на ввод |
| 23 | 211 | set-managed: запятая — отказ | Go+T | `cli.TestApplyRefusesBadInput/comma`; T: `tests/test-managed.sh`: код 2 на ввод |
| 24 | 212 | set-managed: ключ дважды — отказ | Go+T | `cli.TestApplyRefusesBadInput/key twice`, `cli.TestUsageErrors/apply repeated env`; T: `tests/test-managed.sh`: код 2 на ввод |
| 25 | 213 | set-managed: после отказов config не тронут | Go+T | `cli.TestApplyRefusesBadInput` (state.json не тронут, `set-managed` не вызван); T: `tests/test-managed.sh`: конфиг после отказа цел |
| 26 | 214 | set-managed: после отказов systemctl не вызывался | Go+T | `cli.TestApplyRefusesBadInput` (systemctl и портал не вызваны); T: `tests/test-managed.sh`: без restart после отказа |
| 27 | 219 | set-managed: space дописывается, если его не было | T | T: `tests/test-managed.sh`: render — управляемый `space` при отсутствии операторского |
| 28 | 220 | set-managed: space в конце списка | T | T: `tests/test-managed.sh`: render — порядок меток |
| 29 | 223 | set-managed: пустые метки оператора | T | T: `tests/test-managed.sh`: render при пустых `TELEPORT_LABELS` |
| 30 | 224 | set-managed: из пустого списка | T | T: `tests/test-managed.sh`: render при пустых `TELEPORT_LABELS` |
| 31 | 229 | set-managed: агент не поднялся — код 1 | Go+T | T: `tests/test-managed.sh`: restart не подтверждён — `unconfirmed`, код 1; S — `cli.TestApply/unconfirmed`, `teleport.TestSetManagedResults/unconfirmed exit 1` |
| 32 | 230 | set-managed: агент не поднялся — текст | Go+T | `cli.TestApply/unconfirmed` (текст «не подтвердил»); T: `tests/test-managed.sh`: текст отказа |
| 33 | 234 | set-managed: кластер не задан — только запись | Go+T | T: `tests/test-managed.sh`: `deferred` без регистрации; S — `cli.TestApply/deferred`, `reconcile.TestTickDeferred` |
| 34 | 235 | set-managed: кластер не задан — systemctl не вызывается | T | T: `tests/test-managed.sh`: `deferred` — без restart |
| 35 | 236 | set-managed: кластер не задан — записано | T | T: `tests/test-managed.sh`: `deferred` — ручка записана |
| 36 | 239 | set-managed: без systemd — только запись | T | T: `tests/test-managed.sh`: без systemd (образ bisquite) — только запись |
| 37 | 240 | set-managed: без systemd — systemctl не вызывается | T | T: `tests/test-managed.sh`: без systemd — systemctl не вызывается |

### `labels-enable` и `status` (строки 243–279) → `stvor-agent enable`, `status`

| № | Стр. | Проверка (bash) | Итог | Куда |
|---|---|---|---|---|
| 38 | 246 | labels-enable: URL не по шаблону — отказ (http) | Go | `cli.TestEnableRefusalsTouchNothing/http URL`, `cli.TestEnableBadURL` |
| 39 | 248 | labels-enable: URL с чужим путём — отказ | Go | `cli.TestEnableRefusalsTouchNothing/other path`, `cli.TestEnableBadURL` |
| 40 | 250 | labels-enable: URL с кавычкой — отказ | Go | `cli.TestEnableRefusalsTouchNothing/quote in URL`, `portal.TestGetRejectsBeforeNetwork/кавычка` |
| 41 | 252 | labels-enable: без URL — отказ | Go | `cli.TestEnableRefusalsTouchNothing/no URL`, `cli.TestUsageErrors/enable without URL` |
| 42 | 254 | labels-enable: токен не по шаблону — отказ | Go | `cli.TestEnableRefusalsTouchNothing/short token`, `cli.TestEnableStdin/42 chars` |
| 43 | 256 | labels-enable: пустой stdin — отказ | Go | `cli.TestEnableRefusalsTouchNothing/empty stdin`, `cli.TestEnableStdin/empty` |
| 44 | 258 | labels-enable: после отказов файла токена нет | Go | `cli.TestEnableRefusalsTouchNothing`, `cli.TestEnableStdin` |
| 45 | 259 | labels-enable: после отказов systemctl не вызывался | Go | `cli.TestEnableRefusalsTouchNothing`, `cli.TestEnableBadURL` |
| 46 | 260 | labels-enable: после отказов — нет запроса к порталу | Go | `cli.TestEnableRefusalsTouchNothing` |
| 47 | 263 | labels-enable: код 0 | Go | `cli.TestEnableFlow` |
| 48 | 264 | labels-enable: файл токена 0600 | Go | `cli.TestEnableStdin/one line`, `token.TestWriteAndRead` |
| 49 | 265 | labels-enable: в файле адрес и токен (без пробелов вокруг) | Go | `cli.TestEnableStdin/spaces around`, `token.TestTokenFormatting` |
| 50 | 266 | labels-enable: ручка TELEPORT_LABELS_URL | снято | снято: ручки `TELEPORT_LABELS_URL` нет, адрес живёт рядом с токеном (спека §4.3); адрес в файле токена — `cli.TestEnableStdin/one line` |
| 51 | 267 | labels-enable: таймер enable --now | Go | `cli.TestEnableFlow` (таймер заменён службой: `systemctl enable` и `restart stvor-agent.service`, спека §4.2) |
| 52 | 268 | labels-enable: один pull сразу — запрос к порталу | Go | `cli.TestEnableFlow` (один синхронный `sync`, ровно один запрос) |
| 53 | 269 | labels-enable: pull применил метки | Go | `cli.TestEnableFlow` (`set-managed` и `applied_revision`) |
| 54 | 270 | labels-enable: токена нет в config | Go | `cli.TestEnableTokenOnlyInTokenFile` (ни в одном файле, кроме файла токена, — и в state.json) |
| 55 | 271 | labels-enable: токена нет в выводе | Go | `cli.TestEnableFlow`, `cli.TestEnableStdin` (проверка `assertNoToken`) |
| 56 | 272 | labels-enable: токена нет в argv curl | Go | curl нет — HTTP из `net/http`: токен только в заголовке — `portal.TestGetHeaders`; argv и окружение cream без токена — `teleport.TestCleanCall`; argv systemctl — `cli.TestEnableFlow` |
| 57 | 273 | labels-enable: токен в заголовке запроса | Go | `portal.TestGetHeaders` |
| 58 | 275 | status: токена нет | Go | `cli.TestStatusHuman`, `cli.TestStatusJSONGolden` |
| 59 | 276 | status: автоприменение включено | Go | `cli.TestStatusToken/ok`, `cli.TestStatusHuman` |
| 60 | 277 | status: ожидаемые метки | Go | `cli.TestStatusHuman` |
| 61 | 278 | status: временных файлов запроса не осталось | Go | `cli.TestEnableTokenOnlyInTokenFile`, `atomicfile.TestWriteFailureKeepsPrevious`, `atomicfile.TestWriteCreatesFileWithMode` |

### `pull` (строки 282–403) → тик службы и `stvor-agent sync`

| № | Стр. | Проверка (bash) | Итог | Куда |
|---|---|---|---|---|
| 62 | 287 | pull: 200 — код 0 | Go | `cli.TestSyncCodes/applied` |
| 63 | 288 | pull: 200 — метки применены | Go | `reconcile.TestTick200Applies` |
| 64 | 289 | pull: 200 — env применён | Go | `reconcile.TestTick200Applies` (`env` и `space` одним `set-managed`) |
| 65 | 290 | pull: 200 — space у узла и приложений | T | T: `tests/test-managed.sh`: render — копия в каждое приложение |
| 66 | 291 | pull: 200 — один restart | Go+T | `reconcile.TestTick200Applies` (`set-managed` ровно раз); T: `tests/test-managed.sh`: один restart |
| 67 | 292 | pull: labels-expected | Go | `reconcile.TestTick200Applies` (`expected` в state.json), `state.TestSaveWritesSpecFormat` |
| 68 | 293 | pull: labels-applied | Go | `reconcile.TestTick200Applies` (`applied_revision`) |
| 69 | 294 | pull: первый запрос без If-None-Match | Go | `reconcile.TestTick200Applies`, `portal.TestGetHeaders/без ревизии` |
| 70 | 295 | pull: первый запрос без X-Applied-Revision | Go | `reconcile.TestTick200Applies`, `portal.TestGetHeaders/без ревизии` |
| 71 | 296 | pull: curl только https | Go | `portal.TestGetRejectsBeforeNetwork/http://`, `token.TestReadRefusals/http url` |
| 72 | 299 | pull: 304 — код 0 | Go | `cli.TestSyncCodes/304 in effect` |
| 73 | 300 | pull: следующий запрос несёт If-None-Match | Go | `reconcile.TestTickSteadyState/304`, `portal.TestGetHeaders/с ревизией` |
| 74 | 301 | pull: следующий запрос несёт X-Applied-Revision | Go | `reconcile.TestTickSteadyState/304`, `portal.TestGetHeaders/с ревизией и отчётом` |
| 75 | 302 | pull: 304 без расхождения — агент не перезапускается | Go | `reconcile.TestTickSteadyState/304` |
| 76 | 305 | pull: тот же ответ 200 — код 0 | Go | `cli.TestSyncCodes/in effect` |
| 77 | 306 | pull: 200 без изменений — агент не перезапускается | Go | `reconcile.TestTickSteadyState/тот же 200` |
| 78 | 311 | pull: 304, конфиг разошёлся — код 0 | Go | `reconcile.TestTick304HandDamage/space` (итог `applied`, код `sync` 0 — `cli.TestSyncCodes/applied`) |
| 79 | 312 | pull: расхождение — X-Applied-Revision не сообщается | Go | `reconcile.TestTick304HandDamage/space` |
| 80 | 313 | pull: 304, конфиг разошёлся — снова применено | Go | `reconcile.TestTick304HandDamage/space` |
| 81 | 314 | pull: самовосстановление — один restart | Go | `reconcile.TestTick304HandDamage/space` (`set-managed` ровно раз) |
| 82 | 317 | pull: ручной env — код 0 | Go | `reconcile.TestTick304HandDamage/env` |
| 83 | 318 | pull: ручной env исправлен | Go | `reconcile.TestTick304HandDamage/env` |
| 84 | 327 | pull: 401 — код 0 | Go | `cli.TestSyncCodes/401` |
| 85 | 328 | pull: 401 — одна строка в журнале | Go | `reconcile.TestTickPortalFailures/unauthorized` |
| 86 | 329 | pull: 401 — токена нет в журнале | Go | `reconcile.TestTickPortalFailures/unauthorized` (журнал тика без токена), `portal.TestGetClassifiesStatus/401` |
| 87 | 330 | pull: 401 — config не тронут | Go | `reconcile.TestTickPortalFailures/unauthorized` (`set-managed` не вызван) |
| 88 | 331 | pull: 401 — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/unauthorized` (`expected` и `applied_revision` прежние) |
| 89 | 332 | pull: 401 — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/unauthorized` |
| 90 | 327 | pull: 429 — код 0 | Go | `cli.TestSyncCodes/429` |
| 91 | 328 | pull: 429 — одна строка в журнале | Go | `reconcile.TestTickPortalFailures/rate_limited` |
| 92 | 329 | pull: 429 — токена нет в журнале | Go | `reconcile.TestTickPortalFailures/rate_limited` (журнал тика без токена) |
| 93 | 330 | pull: 429 — config не тронут | Go | `reconcile.TestTickPortalFailures/rate_limited` (`set-managed` не вызван) |
| 94 | 331 | pull: 429 — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/rate_limited` (`expected` и `applied_revision` прежние) |
| 95 | 332 | pull: 429 — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/rate_limited` |
| 96 | 327 | pull: 500 — код 0 | Go | `cli.TestSyncCodes/500` |
| 97 | 328 | pull: 500 — одна строка в журнале | Go | `reconcile.TestTickPortalFailures/http_error` |
| 98 | 329 | pull: 500 — токена нет в журнале | Go | `reconcile.TestTickPortalFailures/http_error` (журнал тика без токена), `portal.TestGetClassifiesStatus/500` |
| 99 | 330 | pull: 500 — config не тронут | Go | `reconcile.TestTickPortalFailures/http_error` (`set-managed` не вызван) |
| 100 | 331 | pull: 500 — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/http_error` (`expected` и `applied_revision` прежние) |
| 101 | 332 | pull: 500 — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/http_error` |
| 102 | 327 | pull: down — код 0 | Go | `cli.TestSyncCodes/network` |
| 103 | 328 | pull: down — одна строка в журнале | Go | `reconcile.TestTickPortalFailures/network` |
| 104 | 329 | pull: down — токена нет в журнале | Go | `reconcile.TestTickPortalFailures/network` (журнал тика без токена) |
| 105 | 330 | pull: down — config не тронут | Go | `reconcile.TestTickPortalFailures/network` (`set-managed` не вызван) |
| 106 | 331 | pull: down — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/network` (`expected` и `applied_revision` прежние) |
| 107 | 332 | pull: down — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/network` |
| 108 | 334 | pull: 401 — подсказка про токен | Go | `cli.TestSyncCodes/401` |
| 109 | 341 | pull: битый JSON — отказ (код ≠ 0) | Go | `portal.TestParseBodyRejects/не JSON`, `cli.TestSyncCodes/bad body` |
| 110 | 342 | pull: битый JSON — config не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` (`set-managed` не вызван) |
| 111 | 343 | pull: битый JSON — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 112 | 344 | pull: битый JSON — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 113 | 341 | pull: пустое тело — отказ (код ≠ 0) | Go | `portal.TestParseBodyRejects/пустое тело`, `portal.TestGetClassifiesStatus/200 пустое тело`, `cli.TestSyncCodes/bad body` |
| 114 | 342 | pull: пустое тело — config не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` (`set-managed` не вызван) |
| 115 | 343 | pull: пустое тело — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 116 | 344 | pull: пустое тело — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 117 | 341 | pull: два значения space — отказ (код ≠ 0) | Go | `portal.TestParseBodyRejects/повтор space`, `cli.TestSyncCodes/bad body` |
| 118 | 342 | pull: два значения space — config не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` (`set-managed` не вызван) |
| 119 | 343 | pull: два значения space — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 120 | 344 | pull: два значения space — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 121 | 341 | pull: два объекта — отказ (код ≠ 0) | Go | `portal.TestParseBodyRejects/хвост — второй объект`, `cli.TestSyncCodes/bad body` |
| 122 | 342 | pull: два объекта — config не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` (`set-managed` не вызван) |
| 123 | 343 | pull: два объекта — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 124 | 344 | pull: два объекта — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 125 | 341 | pull: недопустимое значение space — отказ (код ≠ 0) | Go | `portal.TestParseBodyBoundaryValues/заглавные`, `portal.TestParseBodyBoundaryValues/подчёркивание`, `cli.TestSyncCodes/bad body` |
| 126 | 342 | pull: недопустимое значение space — config не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` (`set-managed` не вызван) |
| 127 | 343 | pull: недопустимое значение space — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 128 | 344 | pull: недопустимое значение space — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 129 | 341 | pull: пробел в env — отказ (код ≠ 0) | Go | `portal.TestParseBodyBoundaryValues/пробел`, `cli.TestSyncCodes/bad body` |
| 130 | 342 | pull: пробел в env — config не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` (`set-managed` не вызван) |
| 131 | 343 | pull: пробел в env — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 132 | 344 | pull: пробел в env — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 133 | 341 | pull: ревизия не 16 hex — отказ (код ≠ 0) | Go | `portal.TestParseBodyRejects/revision не hex`, `cli.TestSyncCodes/bad body` |
| 134 | 342 | pull: ревизия не 16 hex — config не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` (`set-managed` не вызван) |
| 135 | 343 | pull: ревизия не 16 hex — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 136 | 344 | pull: ревизия не 16 hex — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 137 | 341 | pull: нет space — отказ (код ≠ 0) | Go | `portal.TestParseBodyRejects/нет space`, `cli.TestSyncCodes/bad body` |
| 138 | 342 | pull: нет space — config не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` (`set-managed` не вызван) |
| 139 | 343 | pull: нет space — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 140 | 344 | pull: нет space — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 141 | 341 | pull: лишний ключ — отказ (код ≠ 0) | Go | `portal.TestParseBodyRejects/лишний ключ в labels`, `cli.TestSyncCodes/bad body` |
| 142 | 342 | pull: лишний ключ — config не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` (`set-managed` не вызван) |
| 143 | 343 | pull: лишний ключ — labels-expected не тронут | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 144 | 344 | pull: лишний ключ — агент не перезапускается | Go | `reconcile.TestTickPortalFailures/bad_body` |
| 145 | 359 | pull: JSON с пробелами между лексемами принимается | Go | `portal.TestParseBodyAccepts/пробелы между токенами` |
| 146 | 360 | pull: JSON с пробелами — применено | Go | `portal.TestParseBodyAccepts/пробелы между токенами`, `reconcile.TestTick200NewRevision` |
| 147 | 366 | pull: агент не поднялся — код 1 | Go | `cli.TestSyncCodes/unconfirmed` |
| 148 | 367 | pull: агент не поднялся — ревизия не отмечена | Go | `reconcile.TestTickUnconfirmedCounts` |
| 149 | 368 | pull: агент не поднялся — ожидаемое записано | Go | `reconcile.TestTickUnconfirmedCounts` |
| 150 | 371 | pull: агент лежит — X-Applied-Revision не сообщается | Go | `reconcile.TestTickUnconfirmedCounts`, `reconcile.TestTickPendingOrInactiveReapplies/агент неактивен` |
| 151 | 374 | pull: следующий тик — код 0 | Go | `cli.TestSyncCodes/applied` |
| 152 | 375 | pull: следующий тик — агент перезапущен | Go | `reconcile.TestTickPendingOrInactiveReapplies/агент неактивен` |
| 153 | 376 | pull: следующий тик — ревизия отмечена | Go | `reconcile.TestTickPendingOrInactiveReapplies/агент неактивен` |
| 154 | 381 | pull: без токена — код 0 | Go | `reconcile.TestTickToken/нет файла` (тик службы «не включён» — не ошибка, `daemon.TestNext/disabled keeps n`); разовый `sync` без токена по спеке §8 — код 1: `cli.TestSyncCodes/not enabled` |
| 155 | 382 | pull: без токена — запроса нет | Go | `reconcile.TestTickToken/нет файла` |
| 156 | 385 | pull: без URL — код 0 | снято | снято: адрес живёт в файле токена (спека §4.3), «токен без адреса» — испорченный файл: `token.TestReadRefusals/no url` |
| 157 | 386 | pull: без URL — запроса нет | снято | снято: то же; испорченный файл токена — запроса нет: `reconcile.TestTickToken/испорчен` |
| 158 | 389 | pull: испорченный файл токена — отказ | Go | `reconcile.TestTickToken/испорчен`, `token.TestReadRefusals/token with spaces`, `cli.TestSyncCodes/token corrupt` |
| 159 | 390 | pull: испорченный файл токена — запроса нет | Go | `reconcile.TestTickToken/испорчен` |
| 160 | 392 | pull: файл токена шире 0600 — отказ | Go | `token.TestReadRefusals/file 0644`, `reconcile.TestTickToken/права`, `cli.TestSyncCodes/token bad mode` |
| 161 | 393 | pull: файл токена шире 0600 — запроса нет | Go | `reconcile.TestTickToken/права` |
| 162 | 396 | pull: адрес ручки не тот, для которого токен — отказ | снято | снято: адрес и токен неразделимы — запрос уходит только на `url` из файла токена (спека §5.6); другой адрес у `enable` сбрасывает состояние — `cli.TestEnableResetsStateForAnotherURL`, смена адреса в тике — `reconcile.TestTickURLChangeResetsState` |
| 163 | 397 | pull: адрес не тот — токен не отправлен | снято | снято: то же (второго адреса нет); `reconcile.TestTickURLChangeResetsState` |
| 164 | 398 | pull: адрес не тот — токена нет в журнале | снято | снято: то же; журнал тика без токена проверяет каждый тест тика — `reconcile.TestTickToken` |
| 165 | 400 | TELEPORT_LABELS_URL через set — отказ (ro) | снято | снято: ручки `TELEPORT_LABELS_URL` нет (спека §4.3); адрес меняет только `enable` — `cli.TestEnableResetsStateForAnotherURL` |
| 166 | 401 | TELEPORT_LABELS_URL через set — текст про labels-enable | снято | снято: ручки нет (спека §4.3) |
| 167 | 402 | TELEPORT_LABELS_URL через set: чужой путь — отказ | снято | снято: ручки нет; форма адреса — `token.TestValidURL` |

### R3: перезапуск после записи подтверждается, пауза 3×/15 мин (строки 406–510)

| № | Стр. | Проверка (bash) | Итог | Куда |
|---|---|---|---|---|
| 168 | 410 | R3: исходно применено | Go | `reconcile.TestTick200Applies` |
| 169 | 414 | R3: restart упал, агент активен — код 1 | Go+T | `cli.TestSyncCodes/unconfirmed`; T: `tests/test-managed.sh`: restart упал при живом агенте — `unconfirmed`, код 1 |
| 170 | 415 | R3: restart упал — маркер остался | T | T: `tests/test-managed.sh`: маркер `managed-pending` ставится до записи и снимается только подтверждённым restart |
| 171 | 416 | R3: restart упал — ревизия не отмечена | Go | `reconcile.TestTickUnconfirmedCounts` |
| 172 | 417 | R3: restart упал — конфиг уже новый | Go+T | T: `tests/test-managed.sh`: запись до restart; S видит новые метки с `pending` — `reconcile.TestTickPendingOrInactiveReapplies/pending` |
| 173 | 420 | R3: снова упал — код 1 | Go | `reconcile.TestTickUnconfirmedCounts` (второй тик — `unconfirmed`), `cli.TestSyncCodes/unconfirmed` |
| 174 | 421 | R3: пока маркер есть — X-Applied-Revision не шлётся | Go | `reconcile.TestTickPendingOrInactiveReapplies/pending`, `reconcile.TestTickUnconfirmedCounts` |
| 175 | 422 | R3: пока маркер есть — ревизия не отмечена | Go | `reconcile.TestTickUnconfirmedCounts` |
| 176 | 424 | R3: следующий тик — код 0 | Go | `reconcile.TestTickPendingOrInactiveReapplies/pending` (итог `applied`) |
| 177 | 425 | R3: следующий тик — перезапуск выполнен, хотя агент был активен | Go+T | `reconcile.TestTickPendingOrInactiveReapplies/pending` (агент активен, `pending` — `set-managed` снова); T: `tests/test-managed.sh`: при маркере restart, даже если конфиг уже записан |
| 178 | 426 | R3: после перезапуска маркера нет | Go+T | T: `tests/test-managed.sh`: подтверждённый restart снимает маркер; S: `pending` — метки не действуют, `reconcile.TestInEffect` |
| 179 | 427 | R3: ревизия отмечена только после перезапуска | Go | `reconcile.TestTickPendingOrInactiveReapplies/pending`, `reconcile.TestTickStatusErrorAfterSet` |
| 180 | 429 | R3: теперь X-Applied-Revision | Go | `reconcile.TestTickSteadyState/304` |
| 181 | 435 | R3: restart без нового InvocationID — код 1 | Go+T | T: `tests/test-managed.sh`: «restart принят, нового запуска нет» — `unconfirmed`; S — `teleport.TestSetManagedResults/unconfirmed exit 1`, `cli.TestApply/unconfirmed` |
| 182 | 436 | R3: restart без нового InvocationID — маркер остался | T | T: `tests/test-managed.sh`: `unconfirmed` — маркер остаётся |
| 183 | 446 | R3: убитый тик — перед перезапуском X-Applied-Revision не шлётся | Go | `reconcile.TestTickPendingOrInactiveReapplies/pending` |
| 184 | 448 | R3: убитый тик — перезапуск на следующем тике | Go+T | `reconcile.TestTickPendingOrInactiveReapplies/pending`; T: `tests/test-managed.sh`: «тик умер между записью и restart» — следующий вызов рестартует |
| 185 | 449 | R3: убитый тик — teleport.yaml с новым space | T | T: `tests/test-managed.sh`: после restart `teleport.yaml` с новыми метками |
| 186 | 450 | R3: убитый тик — ревизия отмечена после перезапуска | Go | `reconcile.TestTickPendingOrInactiveReapplies/pending` |
| 187 | 451 | R3: убитый тик — маркера нет | T | T: `tests/test-managed.sh`: маркер снят после restart |
| 188 | 456 | R3: InvocationID тот же, MainPID новый — подтверждено | T | T: `tests/test-managed.sh`: подтверждение restart по каждому из трёх свойств юнита |
| 189 | 457 | R3: подтверждено по MainPID — маркера нет | T | T: `tests/test-managed.sh`: подтверждение по `MainPID` снимает маркер |
| 190 | 460 | R3: InvocationID пуст, MainPID новый — подтверждено | T | T: `tests/test-managed.sh`: подтверждение при пустом `InvocationID` |
| 191 | 477 | пауза: три попытки подряд | Go | `reconcile.TestTickPause` |
| 192 | 478 | пауза: попытка в журнале | Go | `reconcile.TestTickLogsAttemptNumber` |
| 193 | 479 | пауза: счётчик | Go | `reconcile.TestTickPause` (`restart_attempts` = 3 и время), `state.TestRoundTrip` |
| 194 | 481 | пауза: четвёртый тик — без перезапуска | Go | `reconcile.TestTickPause` |
| 195 | 482 | пауза: пропуск в журнале | Go | `reconcile.TestTickPause` (строка `event=skip reason=paused`, «3 неподтверждённых») |
| 196 | 485 | пауза: за секунду до 15 минут — без перезапуска | Go | `reconcile.TestTickPause` |
| 197 | 486 | пауза: пока пауза — X-Applied-Revision нет | Go | `reconcile.TestTickPause` |
| 198 | 488 | пауза: через 15 минут — попытка | Go | `reconcile.TestTickPause` |
| 199 | 489 | пауза: попытка 4 в журнале | Go | `reconcile.TestTickLogsAttemptNumber` |
| 200 | 491 | пауза: после неудачи снова 15 минут | Go | `reconcile.TestTickPause` |
| 201 | 494 | пауза: восстановление — перезапуск | Go | `reconcile.TestTickPause` |
| 202 | 495 | пауза: подтверждено — счётчик сброшен | Go | `reconcile.TestTickPause`, `reconcile.TestTickUnchangedResetsAttempts` |
| 203 | 496 | пауза: подтверждено — маркера нет | Go+T | `reconcile.TestTickPause` (итог `applied`); T: `tests/test-managed.sh`: маркер снят подтверждённым restart |
| 204 | 497 | пауза: подтверждено — ревизия отмечена | Go | `reconcile.TestTickPause` |
| 205 | 505 | пауза: агент неактивен — перезапуск без паузы | Go | `reconcile.TestTickNoPause/агент неактивен` |
| 206 | 510 | пауза: set-managed руками — перезапуск без паузы | Go | `reconcile.TestTickNoPause/manual`, `cli.TestApply/paused attempts do not stop a person`, `cli.TestSyncCodes/no pause for a person` |

### `leave` снимает метки (строки 513–526) → `stvor-agent disable` + `cream run teleport leave`

| № | Стр. | Проверка (bash) | Итог | Куда |
|---|---|---|---|---|
| 207 | 517 | leave — код 0 | Go+T | `cli.TestDisable/enabled agent`; T: `tests/test-managed.sh`: `leave` стирает управляемые метки |
| 208 | 518 | leave: таймер выключен | Go | `cli.TestDisable` (`systemctl disable --now stvor-agent.service`) |
| 209 | 524 | leave: etc/bisquite/teleport/labels-token удалён | Go | `cli.TestDisable` (файл токена удалён) |
| 210 | 524 | leave: var/lib/bisquite/teleport/labels-expected удалён | Go | `cli.TestDisable` (state.json удалён) |
| 211 | 524 | leave: var/lib/bisquite/teleport/labels-applied удалён | Go | `cli.TestDisable` (state.json удалён) |
| 212 | 524 | leave: var/lib/bisquite/teleport/labels-restart-pending удалён | T | T: `tests/test-managed.sh`: `leave` снимает `managed-pending` (маркер принадлежит T, спека §4.3) |
| 213 | 524 | leave: var/lib/bisquite/teleport/labels-restart-attempts удалён | Go | `cli.TestDisable` (state.json удалён) |
| 214 | 526 | leave: TELEPORT_LABELS_URL очищен | снято | снято: ручки `TELEPORT_LABELS_URL` нет (спека §4.3) |

### `join`: набор ключей не изменился (строки 529–544)

| № | Стр. | Проверка (bash) | Итог | Куда |
|---|---|---|---|---|
| 215 | 534 | join со старым набором ключей | T | T: `tests/test-join.sh`: `join` с `TELEPORT_PROXY`, `TELEPORT_TOKEN`, `TELEPORT_NODENAME`, `TELEPORT_ENV`, `TELEPORT_LABELS` |
| 216 | 537 | join: метки записаны | T | T: `tests/test-join.sh`: метки оператора записаны |
| 217 | 538 | join: teleport.yaml собран | T | T: `tests/test-join.sh`: `teleport.yaml` собран со `space` |
| 218 | 539 | join: таймер меток не трогает | снято | снято: таймера меток нет (спека §4.2), T ничего не знает о службе S |
| 219 | 542 | join с TELEPORT_LABELS_URL — отказ | снято | снято: ручки нет (спека §4.3); неизвестный ключ отвергает cream по `config.keys` |
| 220 | 543 | join с TELEPORT_LABELS_URL — текст про labels-enable | снято | снято: ручки нет (спека §4.3) |
| 221 | 544 | join с TELEPORT_LABELS_URL — адрес не записан | снято | снято: ручки нет (спека §4.3) |

### Юниты таймера меток (строки 547–557) → `packaging/stvor-agent.service`

| № | Стр. | Проверка (bash) | Итог | Куда |
|---|---|---|---|---|
| 222 | 550 | юнит: oneshot | снято | снято: таймер заменён долгоживущей службой (спека §4.2); тип службы — `packaging.TestUnitRequiredKeys` (`Type=exec`) |
| 223 | 551 | юнит: только при токене | Go | `packaging.TestUnitRequiredKeys` (`ConditionPathExists=/etc/stvor-agent/token`) |
| 224 | 552 | юнит: ExecStart — pull | Go | `packaging.TestUnitRequiredKeys` (`ExecStart=/usr/bin/stvor-agent run`) |
| 225 | 553 | юнит: TimeoutStartSec=5min | снято | снято: oneshot нет; длительность тика ограничивают тайм-ауты запроса и cream — `portal.TestGetTimeout`, `teleport.TestTimeoutKillsProcessGroup` |
| 226 | 554 | таймер: OnBootSec=1min | снято | снято: первый тик — после джиттера при старте службы, `daemon.TestRunFirstTickAfterJitterThenInterval` |
| 227 | 555 | таймер: OnUnitActiveSec=1min | снято | снято: интервал 60 с по умолчанию — `daemon.TestLoadConfig/defaults`, `daemon.TestRunFirstTickAfterJitterThenInterval` |
| 228 | 556 | таймер: RandomizedDelaySec=20s | снято | снято: джиттер службы — `daemon.TestJitter` |
| 229 | 557 | таймер: WantedBy=timers.target | снято | снято: таймера нет; служба — `packaging.TestUnitRequiredKeys` (`WantedBy=multi-user.target`) |

## Для CE

Проверки, которые должны появиться в тестах расширения `teleport` (Task 11 — `test-join.sh`, Task 12 —
`test-managed.sh`; спека §10.7). № — номер строки карты выше; «Стр.» — строка `tests/test-labels.sh` 3.x, откуда
брать сценарий и подменённые `systemctl`/`curl`/`teleport`.

| № | Стр. | Проверка (bash) | Тест T | Что проверить |
|---|---|---|---|---|
| №1 | 181 | set-managed: слияние — код 0 | `test-managed.sh` | `set-managed` пишет управляемые метки, код 0; ручной путь S — `cli.TestApply/applied` |
| №2 | 182 | set-managed: метки оператора сохранены, space заменён | `test-managed.sh` | render — управляемый `space` перекрывает операторский, остальные метки оператора целы |
| №3 | 183 | set-managed: TELEPORT_ENV | `test-managed.sh` | render — управляемый `env` перекрывает `TELEPORT_ENV` |
| №4 | 185 | set-managed: space у узла и у каждого приложения | `test-managed.sh` | render — копия управляемых меток в каждое приложение |
| №5 | 186 | set-managed: старого space в teleport.yaml нет | `test-managed.sh` | render — прежнего значения в `teleport.yaml` нет |
| №6 | 187 | set-managed: env у узла и приложений | `test-managed.sh` | render — `env` у узла и приложений |
| №7 | 188 | set-managed: ровно один restart, без reload | `test-managed.sh` | restart только при изменении итогового конфига, ровно один; S зовёт `set-managed` ровно раз — `reconcile.TestTick200Applies` |
| №8 | 189 | set-managed: хук apply не вызывался | `test-managed.sh` | `set-managed` пишет ручку без хука `apply` (спека §10.5) |
| №9 | 193 | set-managed: без изменений — код 0 | `test-managed.sh` | итог `unchanged`; S — `cli.TestApply/unchanged`, `teleport.TestSetManagedResults/unchanged` |
| №10 | 194 | set-managed: без изменений — systemctl не вызывается | `test-managed.sh` | итоговый конфиг не изменился — без restart |
| №11 | 195 | set-managed: без изменений — config тот же | `test-managed.sh` | конфиг не переписан |
| №12 | 197 | set-managed: только space | `test-managed.sh` | `set-managed` с частью ключей; S всегда шлёт ровно `env` и `space` — см. `teleport.TestCleanCall` |
| №13 | 198 | set-managed: только space — env прежний | `test-managed.sh` | частичная запись не трогает `env` |
| №14 | 199 | set-managed: только space — метки | `test-managed.sh` | частичная запись `space` |
| №15 | 203 | set-managed: чужой ключ app — отказ | `test-managed.sh` | код 2 на ввод |
| №16 | 204 | set-managed: чужой ключ вместе с допустимым — отказ | `test-managed.sh` | код 2 на ввод |
| №17 | 205 | set-managed: ключ ручки — отказ | `test-managed.sh` | код 2 на ввод |
| №18 | 206 | set-managed: без аргументов — отказ | `test-managed.sh` | код 2 на ввод |
| №19 | 207 | set-managed: space=A"b — отказ | `test-managed.sh` | код 2 на ввод |
| №20 | 208 | set-managed: пустой space — отказ | `test-managed.sh` | код 2 на ввод |
| №21 | 209 | set-managed: перевод строки в значении — отказ | `test-managed.sh` | код 2 на ввод |
| №22 | 210 | set-managed: заглавные — отказ | `test-managed.sh` | код 2 на ввод |
| №23 | 211 | set-managed: запятая — отказ | `test-managed.sh` | код 2 на ввод |
| №24 | 212 | set-managed: ключ дважды — отказ | `test-managed.sh` | код 2 на ввод |
| №25 | 213 | set-managed: после отказов config не тронут | `test-managed.sh` | конфиг после отказа цел |
| №26 | 214 | set-managed: после отказов systemctl не вызывался | `test-managed.sh` | без restart после отказа |
| №27 | 219 | set-managed: space дописывается, если его не было | `test-managed.sh` | render — управляемый `space` при отсутствии операторского |
| №28 | 220 | set-managed: space в конце списка | `test-managed.sh` | render — порядок меток |
| №29 | 223 | set-managed: пустые метки оператора | `test-managed.sh` | render при пустых `TELEPORT_LABELS` |
| №30 | 224 | set-managed: из пустого списка | `test-managed.sh` | render при пустых `TELEPORT_LABELS` |
| №31 | 229 | set-managed: агент не поднялся — код 1 | `test-managed.sh` | restart не подтверждён — `unconfirmed`, код 1; S — `cli.TestApply/unconfirmed`, `teleport.TestSetManagedResults/unconfirmed exit 1` |
| №32 | 230 | set-managed: агент не поднялся — текст | `test-managed.sh` | текст отказа |
| №33 | 234 | set-managed: кластер не задан — только запись | `test-managed.sh` | `deferred` без регистрации; S — `cli.TestApply/deferred`, `reconcile.TestTickDeferred` |
| №34 | 235 | set-managed: кластер не задан — systemctl не вызывается | `test-managed.sh` | `deferred` — без restart |
| №35 | 236 | set-managed: кластер не задан — записано | `test-managed.sh` | `deferred` — ручка записана |
| №36 | 239 | set-managed: без systemd — только запись | `test-managed.sh` | без systemd (образ bisquite) — только запись |
| №37 | 240 | set-managed: без systemd — systemctl не вызывается | `test-managed.sh` | без systemd — systemctl не вызывается |
| №65 | 290 | pull: 200 — space у узла и приложений | `test-managed.sh` | render — копия в каждое приложение |
| №66 | 291 | pull: 200 — один restart | `test-managed.sh` | один restart |
| №169 | 414 | R3: restart упал, агент активен — код 1 | `test-managed.sh` | restart упал при живом агенте — `unconfirmed`, код 1 |
| №170 | 415 | R3: restart упал — маркер остался | `test-managed.sh` | маркер `managed-pending` ставится до записи и снимается только подтверждённым restart |
| №172 | 417 | R3: restart упал — конфиг уже новый | `test-managed.sh` | запись до restart; S видит новые метки с `pending` — `reconcile.TestTickPendingOrInactiveReapplies/pending` |
| №177 | 425 | R3: следующий тик — перезапуск выполнен, хотя агент был активен | `test-managed.sh` | при маркере restart, даже если конфиг уже записан |
| №178 | 426 | R3: после перезапуска маркера нет | `test-managed.sh` | подтверждённый restart снимает маркер; S: `pending` — метки не действуют, `reconcile.TestInEffect` |
| №181 | 435 | R3: restart без нового InvocationID — код 1 | `test-managed.sh` | «restart принят, нового запуска нет» — `unconfirmed`; S — `teleport.TestSetManagedResults/unconfirmed exit 1`, `cli.TestApply/unconfirmed` |
| №182 | 436 | R3: restart без нового InvocationID — маркер остался | `test-managed.sh` | `unconfirmed` — маркер остаётся |
| №184 | 448 | R3: убитый тик — перезапуск на следующем тике | `test-managed.sh` | «тик умер между записью и restart» — следующий вызов рестартует |
| №185 | 449 | R3: убитый тик — teleport.yaml с новым space | `test-managed.sh` | после restart `teleport.yaml` с новыми метками |
| №187 | 451 | R3: убитый тик — маркера нет | `test-managed.sh` | маркер снят после restart |
| №188 | 456 | R3: InvocationID тот же, MainPID новый — подтверждено | `test-managed.sh` | подтверждение restart по каждому из трёх свойств юнита |
| №189 | 457 | R3: подтверждено по MainPID — маркера нет | `test-managed.sh` | подтверждение по `MainPID` снимает маркер |
| №190 | 460 | R3: InvocationID пуст, MainPID новый — подтверждено | `test-managed.sh` | подтверждение при пустом `InvocationID` |
| №203 | 496 | пауза: подтверждено — маркера нет | `test-managed.sh` | маркер снят подтверждённым restart |
| №207 | 517 | leave — код 0 | `test-managed.sh` | `leave` стирает управляемые метки |
| №212 | 524 | leave: var/lib/bisquite/teleport/labels-restart-pending удалён | `test-managed.sh` | `leave` снимает `managed-pending` (маркер принадлежит T, спека §4.3) |
| №215 | 534 | join со старым набором ключей | `test-join.sh` | `join` с `TELEPORT_PROXY`, `TELEPORT_TOKEN`, `TELEPORT_NODENAME`, `TELEPORT_ENV`, `TELEPORT_LABELS` |
| №216 | 537 | join: метки записаны | `test-join.sh` | метки оператора записаны |
| №217 | 538 | join: teleport.yaml собран | `test-join.sh` | `teleport.yaml` собран со `space` |
