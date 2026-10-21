# Справочник CLI

```
stvor-agent <команда> [KEY=VALUE …]
```

Аргументы — `KEY=VALUE`, каждый ключ ровно один раз. Все команды, кроме `status`, `version` и `help`,
требуют root. То же кратко — `stvor-agent help` и `man stvor-agent`.

## Команды

| Команда | Что делает |
|---|---|
| `run` | цикл службы — `ExecStart` юнита `stvor-agent.service`; руками не запускают |
| `enable URL=<адрес>` | читает токен меток со stdin, атомарно пишет `/etc/stvor-agent/token` (0600), включает и перезапускает службу, затем делает один синхронный опрос. Другой адрес портала сбрасывает `state.json` |
| `disable` | `systemctl disable --now`, удаляет токен и `state.json`. Teleport не трогает: `env`/`space` остаются, метка `stvor_agent` пустеет за ≤ 5 мин |
| `sync` | один опрос и сверка сейчас, без паузы после неудачных перезапусков |
| `apply env=<v> space=<v>` | ручной запасной путь: те же значения в `teleport` без портала и без паузы. `state.json` не меняет — следующий опрос вернёт решение портала |
| `status [--json]` | состояние: токен (только класс), `state.json`, `managed-status` расширения `teleport`; в сеть не ходит |
| `version [--short\|--label]` | версия, коммит и сборка; `--short` — `X.Y.Z`; `--label` — `X.Y.Z`, если есть файл токена, иначе пустая строка |
| `help` | справка |

Формы аргументов:

- `URL` — ровно `https://<хост>[:порт]/robot-api/v1/labels`, строчными буквами.
- Токен — 43 знака `[A-Za-z0-9_-]`, одна строка на stdin; пробелы по краям отбрасываются.
- `env`, `space` — `[a-z0-9][a-z0-9-]{0,62}`.

Чтобы токен не попал в историю оболочки:

```bash
read -rs T && printf '%s' "$T" | sudo stvor-agent enable URL=https://<портал>/robot-api/v1/labels; unset T
```

## Коды выхода

| Команда | 0 | 1 | 2 |
|---|---|---|---|
| `run` | штатная остановка | ошибка запуска | — |
| `enable` | токен записан, служба запущена; отказ сети при первом опросе — тоже 0 (служба догонит) | неверный адрес или токен, ошибка записи или `systemctl`, не root | неверные аргументы |
| `disable` | выключено | что-то не удалось | неверные аргументы |
| `sync` | метки действуют, узел ещё не зарегистрирован, ошибка сети или HTTP (служба повторит) | ответ не по контракту, перезапуск не подтверждён, токен битый, агент не включён, не root | неверные аргументы |
| `apply` | применено | `set-managed` не выполнен или не подтверждён, не root | значение вне алфавита, неверные аргументы |
| `status` | агент включён | не включён или запуск не от root (`enabled: null`) | неверный флаг |
| `version` | всегда (`--label` — всегда 0) | — | неверный флаг |

Неизвестная команда — код 2 и справка. Команды CLI ждут блокировку агента `/run/stvor-agent.lock` до
120 с; служба берёт её без ожидания и при занятой пропускает опрос.

## `status --json`

Схема 1 — для скриптов; поля только добавляются.

```json
{
  "schema": 1,
  "version": "4.0.1",
  "service": "active",
  "enabled": true,
  "url": "https://portal.example.org/robot-api/v1/labels",
  "token": "ok",
  "expected": {"env": "prod", "space": "sp-1a2b3c4d", "revision": "0123456789abcdef"},
  "applied_revision": "0123456789abcdef",
  "in_effect": true,
  "teleport": {"managed": {"env": "prod", "space": "sp-1a2b3c4d"}, "pending": false, "joined": true, "active": true},
  "restart": {"attempts": 0, "paused_until": null},
  "last_poll": {"at": "2026-10-21T03:00:00Z", "outcome": "not_modified", "http_status": 304}
}
```

| Поле | Значения |
|---|---|
| `service` | состояние юнита по `systemctl is-active` |
| `enabled` | `true`, `false`; `null` — запуск не от root |
| `token` | `ok`, `absent`, `bad_mode`, `bad_owner`, `no_access`, `corrupt` (сам токен не выводится) |
| `expected` | последний ответ портала; `null` — ответа ещё не было |
| `in_effect` | метки портала применены и подтверждены |
| `teleport` | из `cream run teleport managed-status`: управляемые метки, ждёт ли перезапуск (`pending`), зарегистрирован ли узел, активен ли агент Teleport; `null` — расширение не ответило |
| `restart` | неподтверждённые перезапуски подряд и пауза после трёх |
| `last_poll.outcome` | `ok`, `not_modified`, `unauthorized`, `rate_limited`, `network`, `redirect`, `http_error`, `bad_body` |

Что делать с каждым значением — [диагностика](../guides/diagnostics.md).
