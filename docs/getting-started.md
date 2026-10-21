# Первые шаги

Поставить агент на машину Debian или Ubuntu, включить его токеном портала и убедиться, что метки
применились. Обычно всё это делает скрипт установки из карточки робота на портале Stvor; ниже — те же
шаги руками. Нужны root, доступ к GitHub и к кластеру Teleport.

## 1. cream и каталог расширений

```bash
V=1.1.4; ARCH=$(dpkg --print-architecture)
curl -fLO "https://github.com/iamletenkov/cream/releases/download/v$V/cream_${V}_${ARCH}.deb"
curl -fLO "https://github.com/iamletenkov/cream/releases/download/v$V/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
sudo apt install "./cream_${V}_${ARCH}.deb"

CV=5.2.0
URL="https://github.com/iamletenkov/cream-extensions/releases/download/v$CV"
SUM=$(curl -fsSL "$URL/SHA256SUMS" | awk -v f="cream-extensions-$CV.tar.gz" '$2 == f {print $1}')
sudo cream source add core --archive "$URL/cream-extensions-$CV.tar.gz" --sha256 "$SUM" --sync
```

## 2. Узел Teleport

Агент меняет метки уже зарегистрированного узла; регистрация — дело расширения `teleport`. Секрет
регистрации (для метода `token` — сам join-токен) передаётся только через stdin:

```bash
sudo cream install core/teleport
printf '%s' "$SECRET" | sudo cream run teleport join \
  TELEPORT_PROXY=teleport.example.org:443 TELEPORT_REGISTRATION_SECRET=- \
  TELEPORT_ENV=dev TELEPORT_NODENAME=robot-01
sudo cream run teleport status
```

## 3. Агент

```bash
sudo cream install core/stvor-agent      # .deb по пину каталога, sha256 сверяется до установки
stvor-agent version
```

Служба установлена, но не запущена: ей нужен токен меток (`ConditionPathExists=/etc/stvor-agent/token`).

## 4. Включить

Токен меток выдаёт портал: карточка робота → «Метки» → «Выпустить токен меток».

```bash
read -rs T && printf '%s' "$T" | sudo stvor-agent enable URL=https://<портал>/robot-api/v1/labels; unset T
```

`enable` пишет `/etc/stvor-agent/token` (0600 root), включает и запускает службу и сразу делает один
опрос портала.

## 5. Проверить

```bash
sudo stvor-agent status
```

```
версия:               4.0.1
служба:               active
включён:              да
токен:                ok
метки действуют:      да
…
```

```bash
journalctl -u stvor-agent -n 20          # event=poll outcome=ok, event=apply result=applied
sudo cream run teleport managed-status   # env и space, которые видит Teleport
```

Через 5 минут у узла в Teleport появится метка `stvor_agent=4.0.1`.

## Что дальше

- [Установка на робота](guides/install-on-robot.md) — офлайн, своё зеркало, образ, голый `.deb`.
- [Диагностика](guides/diagnostics.md) — если «метки действуют: нет».
- [Токен и переезд робота](guides/token-and-move.md) — перевыпуск токена, смена портала, вывод из работы.
