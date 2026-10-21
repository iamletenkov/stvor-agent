# Установка на робота

Агент ставится обёрткой `stvor-agent` каталога
[cream-extensions](https://github.com/iamletenkov/cream-extensions): она берёт `.deb` нужной версии,
сверяет sha256 до установки, ставит пакет, применяет настройки и кладёт drop-in метки `stvor_agent`
для Teleport. Обёртка зависит от расширения `teleport` (≥ 4.0.2 < 5), а пакет — от cream.

## Скрипт портала

Портал Stvor ставит машину одной командой из карточки робота. По шагам она делает то же, что ниже, но
берёт каталог, Teleport и пакет агента со своего зеркала по своим пинам:

```bash
cream source add stvor --archive https://<портал>/dist/teleport/cream-extensions-<v>.tar.gz \
    --sha256 <пин> --replace --sync
cream install teleport TELEPORT_VERSION=18.10.0 TELEPORT_SHA256=<пин arch> \
    TELEPORT_MIRROR=https://<портал>/dist/teleport
printf '%s' "$SECRET" | cream run teleport join TELEPORT_PROXY=<прокси> \
    TELEPORT_JOIN_METHOD=bound_keypair TELEPORT_TOKEN=<имя токена> TELEPORT_REGISTRATION_SECRET=- \
    TELEPORT_ENV=<env> TELEPORT_NODENAME=<имя> TELEPORT_LABELS=space=<slug>
cream install stvor-agent STVOR_AGENT_VERSION=<v> STVOR_AGENT_SHA256=<пин arch> \
    STVOR_AGENT_MIRROR=https://<портал>/dist/teleport
printf '%s' "$TOKEN" | stvor-agent enable URL=https://<портал>/robot-api/v1/labels
```

- У `join` один секрет — `TELEPORT_REGISTRATION_SECRET=-` со stdin: для метода `token` это сам
  join-токен, для `bound_keypair` — секрет регистрации; `TELEPORT_TOKEN` — несекретное имя токена.
- Токен меток (`enable`) и секрет регистрации (`join`) — разные секреты, оба выдаёт портал.

## Параметры обёртки

Задаются в `cream install stvor-agent KEY=…`. Без версии и суммы берётся пин каталога.

| Ключ | Что |
|---|---|
| `STVOR_AGENT_VERSION` | версия пакета; вместе с `STVOR_AGENT_SHA256` |
| `STVOR_AGENT_SHA256` | sha256 `.deb` архитектуры машины |
| `STVOR_AGENT_MIRROR` | база зеркала, файл лежит плоско: `<база>/stvor-agent_<версия>_<arch>.deb`; пусто — релиз GitHub |
| `STVOR_AGENT_DEB` | локальный `.deb` (офлайн), абсолютный путь; взаимоисключим с `STVOR_AGENT_MIRROR` |
| `STVOR_AGENT_INTERVAL`, `STVOR_AGENT_LOG_LEVEL` | настройки службы — [справочник](../reference/configuration.md#настройки) |

Первые четыре — параметры установки: поменять их можно только повтором `cream install stvor-agent`.

## Без сети

```bash
sudo cream source add stvor --archive file:///srv/cream/cream-extensions-<v>.tar.gz --sha256 <сумма> --sync
sudo cream install teleport TELEPORT_TARBALL=/srv/teleport/<тарбол Teleport> TELEPORT_SHA256=<сумма>
sudo cream install stvor-agent STVOR_AGENT_DEB=/srv/stvor-agent_<v>_<arch>.deb STVOR_AGENT_SHA256=<сумма> \
    STVOR_AGENT_VERSION=<v>
```

`enable` при этом требует доступа к порталу: без сети служба просто повторяет опрос с backoff.

## В образе

Сборщик образов [bisquite](https://github.com/iamletenkov/bisquite) ставит расширения `teleport` и
`stvor-agent` в образ (фаза build cream). На первой загрузке машины остаются `cream run teleport join`
и `stvor-agent enable`.

## Голым `.deb`

Если cream и расширение `teleport` на машине уже есть:

```bash
V=4.0.1; ARCH=$(dpkg --print-architecture)
curl -fLO "https://github.com/iamletenkov/stvor-agent/releases/download/v$V/stvor-agent_${V}_${ARCH}.deb"
curl -fLO "https://github.com/iamletenkov/stvor-agent/releases/download/v$V/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
sudo apt install "./stvor-agent_${V}_${ARCH}.deb"
```

Так не появится метка `stvor_agent` (её drop-in кладёт обёртка), а настройки службы — умолчания
(`/etc/cream/stvor-agent/config` необязателен). Без cream пакет не ставится (`Depends: cream (>= 1.0.0)`).

## Что делает пакет

- `postinst` включает юнит только при первой установке (обновление не отменяет `disable` оператора) и
  перезапускает службу при обновлении, если агент включён.
- Служба не стартует без токена: `ConditionPathExists=/etc/stvor-agent/token`.
- `apt remove` выключает и останавливает службу; `apt purge` удаляет `/etc/stvor-agent` и
  `/var/lib/stvor-agent`. Метки Teleport и регистрация узла остаются — их снимает
  `cream run teleport leave`.

## Обновление

```bash
sudo cream source add core --archive "<URL нового каталога>" --sha256 "<сумма>" --replace --sync
sudo cream upgrade stvor-agent           # версия и sha256 — из пина нового каталога
stvor-agent version --short
```

Агент Teleport при обновлении не перезапускается; метка `stvor_agent` получает новую версию в течение
5 минут.
