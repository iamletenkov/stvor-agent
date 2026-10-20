# 02 — Диагностика на роботе

Когда: карточка робота в портале показывает «Метки расходятся», «без stvor-agent» или нет версии агента;
перенос робота в другое пространство не завершается. Команды — на роботе от root (кроме `version`).
Таблица признаков — раздел «Диагностика» [README](../../README.md#диагностика).

## 1. Состояние агента

```sh
stvor-agent version                 # версия, коммит, сборка
sudo stvor-agent status --json      # схема 1; в сеть не ходит
systemctl status stvor-agent.service
```

Что смотреть в `status --json`:

| Поле | Норма | Если не так |
|---|---|---|
| `enabled` | `true` | `false` — агент не включён: [03](03_token-reissue-and-move.md) §1; `null` — запуск не от root |
| `token` | `ok` | `absent` — не включён; `bad_mode`, `bad_owner`, `no_access`, `corrupt` — файл токена испорчен или права не 0600 root: `enable` заново ([03](03_token-reissue-and-move.md)) |
| `url` | адрес портала `https://<домен>/robot-api/v1/labels` | другой портал — [03](03_token-reissue-and-move.md) §3 |
| `last_poll.outcome` | `ok` или `not_modified` | `unauthorized` — токен отозван или перевыпущен ([03](03_token-reissue-and-move.md) §2); `network`, `http_error`, `rate_limited` — сеть или портал, служба повторит; `bad_body` — ответ не по [контракту](../portal-labels-api.md) |
| `in_effect` | `true` | метки портала ещё не применены — см. `teleport` и `restart` |
| `teleport.joined` / `teleport.active` | `true` / `true` | узел не зарегистрирован (`sudo cream run teleport status`) или агент Teleport не работает |
| `teleport.pending` | `false` | `true` — перезапуск Teleport с новыми метками ещё не подтверждён |
| `restart.paused_until` | `null` | три неподтверждённых перезапуска подряд — пауза 15 мин; `sudo stvor-agent sync` её обходит |

## 2. Журнал службы

```sh
journalctl -u stvor-agent -n 50 --no-pager
journalctl -u stvor-agent --since '-1h' --no-pager | grep -E 'event=(poll|apply|skip)'
sudo stvor-agent sync; echo "код $?"   # тик сейчас: итог одной строкой; 0 — норма или временная ошибка
```

Ключи строк — `event=poll outcome=…`, `event=apply result=applied|deferred|unconfirmed`,
`event=skip reason=paused`. Подробности тиков — `sudo cream set stvor-agent STVOR_AGENT_LOG_LEVEL=debug`
(вернуть `info` после разбора). Токена и тел ответов портала в журнале нет — журнал можно пересылать.

## 3. Токен

```sh
sudo ls -ld /etc/stvor-agent /etc/stvor-agent/token   # drwx------ root и -rw------- root
sudo stvor-agent status --json | grep -E '"(token|url)"'
```

Содержимое файла (`url=`, `token=`) не выводить и не пересылать. Права или владелец не те — не чинить
`chmod`-ом, а включить заново с токеном ([03](03_token-reissue-and-move.md) §1): `enable` пишет файл
атомарно с правильными правами.

## 4. Метки в Teleport

На роботе — что видит расширение `teleport`:

```sh
sudo cream run teleport status           # регистрация, управляемые env/space, командные метки
sudo cream run teleport managed-status   # JSON, который читает агент
stvor-agent version --label              # значение метки stvor_agent: версия или пусто без токена
```

В кластере (локальный admin `tctl`, или `tsh` с правом читать узлы):

```sh
tctl get node/<id узла> --format=json | jq '.spec.cmd_labels.stvor_agent.result, .metadata.labels'
tsh ls stvor_agent=<X.Y.Z>               # узлы с этой версией агента
```

| Метка `stvor_agent` | Что значит |
|---|---|
| версия `X.Y.Z` | агент установлен и включён |
| пусто | пакет и drop-in обёртки стоят, токена нет — агент не включён (или команда ещё не исполнялась: период 5 мин) |
| ключа нет | обёртка каталога `stvor-agent` не ставилась (голый `.deb`) или `teleport` старше 4.0.2: `cream install stvor-agent` |

`env`/`space` узла не совпадают с порталом при `last_poll.outcome=ok` — смотреть `event=apply` в журнале
и `teleport.pending`; ручной запасной путь — `sudo stvor-agent apply env=<v> space=<v>` (следующий тик
вернёт решение портала).
