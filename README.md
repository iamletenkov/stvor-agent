# stvor-agent

Агент портала Stvor на машине: держит метки узла Teleport `env` и `space` равными тому, что решил портал.

Портал Stvor решает, в каком пространстве и окружении находится машина (робот, стенд, сервер), а роли
Teleport дают доступ по этим меткам. stvor-agent — служба systemd на машине: раз в минуту она забирает
метки с портала, передаёт их расширению `teleport` через [cream](https://github.com/iamletenkov/cream),
дожидается подтверждённого перезапуска агента Teleport и сообщает порталу, что метки действуют. Входящих
портов агент не открывает, с порталом говорит только исходящим HTTPS.

## Установка

Обычный путь — через cream: обёртка `stvor-agent` каталога
[cream-extensions](https://github.com/iamletenkov/cream-extensions) ставит пакет по пину версии и
sha256, настраивает его и добавляет узлу метку версии агента. Нужны cream и зарегистрированное в Teleport
расширение `teleport` — подробно в [руководстве по установке](docs/guides/install-on-robot.md).

```bash
sudo cream install core/stvor-agent
```

**Пакет `.deb`** — если cream и расширение `teleport` на машине уже есть (пакет зависит от `cream`):

```bash
V=4.0.1; ARCH=$(dpkg --print-architecture)    # amd64 | arm64
curl -fLO "https://github.com/iamletenkov/stvor-agent/releases/download/v$V/stvor-agent_${V}_${ARCH}.deb"
curl -fLO "https://github.com/iamletenkov/stvor-agent/releases/download/v$V/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
sudo apt install "./stvor-agent_${V}_${ARCH}.deb"
stvor-agent version
```

## Быстрый старт

Портал выдаёт токен меток в карточке робота. Включить агент — токен со stdin, чтобы он не попал в
историю оболочки:

```bash
read -rs T && printf '%s' "$T" | sudo stvor-agent enable URL=https://<портал>/robot-api/v1/labels; unset T
sudo stvor-agent status            # включён: да, токен: ok, метки действуют: да
journalctl -u stvor-agent -n 20    # event=poll outcome=ok …
```

`enable` сохраняет токен, запускает службу и сразу делает один опрос портала. Дальше служба работает
сама; `sudo stvor-agent sync` — опрос сейчас.

## Возможности

- **Сверка состояния, а не событий.** На каждом опросе метки в Teleport сравниваются с ответом портала;
  любое расхождение — незавершённый перезапуск, чужая правка, неактивный агент — исправляется.
- **Переезд одним перезапуском.** Новые `env`/`space` применяются одним перезапуском `teleport.service`
  с подтверждением; после трёх неудач подряд — не чаще раза в 15 минут.
- **Бережный опрос.** Условные запросы (`If-None-Match`), джиттер, backoff до 10 минут, лимит портала
  соблюдается.
- **Строгий контракт.** Ответ портала разбирается по токенам; что угодно сверх двух меток из
  фиксированного алфавита — отказ без изменений.
- **Токен под замком.** Файл 0600 root, только stdin, никогда в argv, окружении дочерних процессов и
  журнале.
- **Видно в Teleport.** Динамическая метка узла `stvor_agent=<версия>` показывает, что агент стоит и
  включён.
- **Один статический бинарь** на стандартной библиотеке Go, воспроизводимая сборка `.deb`.

## Документация

- [Первые шаги](docs/getting-started.md)
- Руководства: [установка на робота](docs/guides/install-on-robot.md) ·
  [диагностика](docs/guides/diagnostics.md) ·
  [токен и переезд робота](docs/guides/token-and-move.md)
- Справочник: [CLI и коды выхода](docs/reference/cli.md) ·
  [настройки, файлы, журнал](docs/reference/configuration.md) ·
  [контракт портала `/robot-api/v1/labels`](docs/reference/portal-labels-api.md)
- [Как устроен агент](docs/architecture.md) · [безопасность](SECURITY.md) ·
  [все документы](docs/README.md) · [изменения по версиям](CHANGELOG.md)

На машине: `man stvor-agent`, `stvor-agent help`.

## Совместимость

| | |
|---|---|
| ОС | Debian ≥ 12, Ubuntu ≥ 20.04 (systemd ≥ 245) |
| Архитектуры | `amd64`, `arm64` |
| Зависимости | cream ≥ 1.0.0; расширение `teleport` ≥ 4.0.0 < 5 (обёртка каталога требует ≥ 4.0.2) |
| Портал | контракт `GET /robot-api/v1/labels` v1 (тот же, что у `bisquite-teleport-agent` 3.x) |

Переход с `bisquite-teleport-agent` 3.x — [отличия 3.x и 4.x](docs/history/3x-to-4x.md).

## Лицензия

[PolyForm Noncommercial 1.0.0](LICENSE), см. [NOTICE](NOTICE). Коммерческое использование — по
отдельной лицензии правообладателя.
