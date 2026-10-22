# Настройки, файлы, журнал

## Настройки

Настройки службы — домен cream `stvor-agent`, файл `/etc/cream/stvor-agent/config`; юнит читает его как
`EnvironmentFile` (файл необязателен). Менять — через cream: значение проверяется, хук `apply`
перезапускает службу.

```bash
sudo cream set stvor-agent STVOR_AGENT_INTERVAL=120
cream show stvor-agent
```

| Ключ | Умолчание | Что |
|---|---|---|
| `STVOR_AGENT_INTERVAL` | `60` | период опроса портала, секунды, 60–600 (меньше не даёт лимит портала: 6 запросов в минуту на токен) |
| `STVOR_AGENT_LOG_LEVEL` | `info` | `info` или `debug` (тайминги и строка на каждый опрос без изменений) |

Значение вне границ — строка в журнале и умолчание. Параметры установки обёртки
(`STVOR_AGENT_VERSION`, `STVOR_AGENT_SHA256`, `STVOR_AGENT_MIRROR`, `STVOR_AGENT_DEB`) —
[установка на робота](../guides/install-on-robot.md#параметры-обёртки).

### Прокси

По умолчанию у службы нет переменных прокси. Если портал доступен только через прокси — drop-in юнита:

```bash
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
| `/usr/lib/systemd/system/stvor-agent.service` | 0644 | юнит: `Type=exec`, `Restart=always`, `KillMode=mixed`, `TimeoutStopSec=120`, hardening ([SECURITY.md](../../SECURITY.md#hardening-юнита)) |
| `/etc/stvor-agent/token` | 0600 root, каталог 0700 | `url=<адрес>` и `token=<токен>`; пишет только `enable`. Другие права или владелец — отказ |
| `/var/lib/stvor-agent/state.json` | 0600, каталог 0700 | ожидаемые метки и ревизия, применённая ревизия, попытки перезапуска, последний опрос. Испорченный или правленый руками файл не принимается — служба начинает с пустого состояния |
| `/run/stvor-agent.lock` | — | одна операция агента за раз |
| `/etc/cream/stvor-agent/config` | 0644 | настройки (домен cream), необязателен |
| `/usr/share/man/man8/stvor-agent.8.gz` | 0644 | `man stvor-agent` |

Файлы других компонентов, которые агент читает, но не пишет:

- `/etc/cream/teleport/config` — ручку `TELEPORT_MANAGED_LABELS` пишет действие `set-managed`
  расширения `teleport`;
- `/etc/cream/teleport/command-labels.d/stvor_agent` — drop-in метки `stvor_agent`, владелец — обёртка
  каталога `stvor-agent`.

`apt remove` выключает и останавливает службу; `apt purge` удаляет `/etc/stvor-agent` и
`/var/lib/stvor-agent`.

### OpenWrt

Семейство агент определяет сам по `ID`/`ID_LIKE` из `/etc/os-release` (`openwrt`, `lede`); на остальных
системах всё как выше. На OpenWrt `/var` — tmpfs, поэтому:

| Что | Debian/Ubuntu | OpenWrt |
|---|---|---|
| `state.json` | `/var/lib/stvor-agent/state.json` | `/opt/stvor-agent/state.json` |
| состояние телеметрии | `/var/lib/stvor-agent-telemetry/` | `/opt/stvor-agent/telemetry/` |
| machine-id | `/etc/machine-id` | `/opt/stvor-agent/machine-id` — создаёт `run` при первом старте (128 случайных бит, 0640 `root:stvor-telemetry`; без группы — 0600, группа получает его на следующем старте), содержимое не меняется |
| службы | `systemctl … stvor-agent.service` | `/etc/init.d/stvor-agent enable\|restart\|stop\|disable` (procd) |
| расширение cream | `teleport` | `teleport-openwrt` |

Токены (`/etc/stvor-agent/token`, `/etc/stvor-agent-telemetry/token`) и `/run/stvor-agent.lock` — те же: `/run`
на OpenWrt — ссылка на `/var/run` (tmpfs), её делает `/etc/init.d/boot` (`START=10`) на каждой загрузке, до
служб агента (`START=99`).

procd-скрипты (из `tar.gz` выпуска, `/etc/init.d/`) повторяют юниты:

| | `stvor-agent` | `stvor-agent-telemetry` |
|---|---|---|
| старт | только при `/etc/stvor-agent/token` | только при `/etc/stvor-agent-telemetry/token` |
| пользователь | root, `no_new_privs` | `stvor-telemetry` (создаётся скриптом, если нет), `no_new_privs`, `GOMEMLIMIT=40MiB` |
| перезапуск | `respawn 3600 10 0` (через 10 с, без предела) | так же |
| остановка | SIGTERM, через 120 с SIGKILL | через 30 с |
| каталоги | `/opt/stvor-agent` 0750 `root:stvor-telemetry` | `/opt/stvor-agent/telemetry` 0700, `/run/stvor-agent-telemetry` 0755 — владелец `stvor-telemetry` |
| журнал | `logread -e stvor-agent` | `logread -e stvor-agent` |

Настройки — `/etc/cream/stvor-agent-openwrt/config` (`cream set stvor-agent-openwrt …`): скрипты берут из него
только `STVOR_AGENT_INTERVAL` и `STVOR_AGENT_LOG_LEVEL` с простым значением (`[A-Za-z0-9_.:-]`), файл не исполняют.
`stvor-agent disable` выключает и останавливает агента и останавливает телеметрию; её ссылку `rc.d` (её ставит
установщик) не трогает — как юнит на Debian.

## Служба

- Юнит стартует только при наличии токена (`ConditionPathExists=/etc/stvor-agent/token`), после
  `network-online.target`.
- `SIGHUP` — опрос сейчас: `sudo systemctl kill -s HUP stvor-agent`.
- `SIGTERM` — дождаться конца текущего опроса (до `TimeoutStopSec=120`) и выйти.

## Журнал

Одна строка на событие в journald (`SyslogIdentifier=stvor-agent`): сообщение по-русски, ключи
латиницей. Токена, тел ответов и тел ошибок портала в журнале нет — журнал можно пересылать.

```bash
journalctl -u stvor-agent -f
```

| Событие | Ключи и значения |
|---|---|
| `event=poll` | `outcome=ok\|not_modified\|unauthorized\|rate_limited\|network\|redirect\|http_error\|bad_body`, `status=<код HTTP>` |
| `event=apply` | `result=applied\|unchanged\|deferred\|unconfirmed` |
| `event=skip` | `reason=paused\|locked\|disabled\|token\|…` — опрос пропущен |
| `event=start`, `stop`, `signal`, `sleep`, `tick`, `config` | жизненный цикл службы (`sleep`, `tick` — на уровне `debug`) |

Что значат исходы — [диагностика](../guides/diagnostics.md).
