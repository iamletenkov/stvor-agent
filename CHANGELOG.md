# Изменения

Версии — [SemVer](https://semver.org/lang/ru/). Секции новее 3.0.1 пишет
release-please из Conventional Commits; 3.0.1 и старше написаны вручную в
формате [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/).

## [3.1.1](https://github.com/iamletenkov/stvor-agent/compare/v3.1.0...v3.1.1) (2026-10-19)


### Безопасность

* Версия кластера из ответа прокси (`server_version` в `/webapi/find`) больше
  не попадает в арифметику bash непроверенной. Раньше значение вида
  `a[$(команда)]` выполняло команду от root при `join` и на тике повтора
  `join-retry` — достаточно ответа враждебного или скомпрометированного
  узла по адресу `TELEPORT_PROXY`. Теперь версия проверяется сразу после извлечения:
  только `X.Y.Z[-pre][+build]` с компонентами до 9 цифр, дальше идёт `X.Y.Z`;
  иначе — «прокси назвал версию не по форме», join ждёт, тик повторяет.
  Сравнение версий — только цифрами и в базе 10.
* Числа из окружения (`BISQUITE_TELEPORT_JOIN_TIMEOUT`,
  `BISQUITE_TELEPORT_RESTART_TIMEOUT`, `BISQUITE_TELEPORT_NOW`) проверяются
  как целые до арифметики, иначе — отказ с именем переменной.
* uid из `/etc/passwd`, права файлов из `stat`, счётчики из файлов состояния —
  цифрами до арифметики и в базе 10.

## [3.1.0](https://github.com/iamletenkov/bisquite-teleport-agent/compare/v3.0.1...v3.1.0) (2026-10-14)


### Добавлено

* join waits for the network at first boot and retries until registered ([e6a4850](https://github.com/iamletenkov/bisquite-teleport-agent/commit/e6a48506e8546cb6ae8f66906853fcff46cec168))
* join waits for the network at first boot and retries until registered ([44cb683](https://github.com/iamletenkov/bisquite-teleport-agent/commit/44cb68355d38b25a3ad815ca9e2fbfc6f8a2102b))

## [3.0.1] — 2026-10-07

### Добавлено
- Статическая метка узла `bisquite_agent_version=<версия пакета>` (из
  `/usr/lib/bisquite-teleport/VERSION`): по инвентарю портал отличает пакет
  3.x от расширения 2.x. Ключ в `TELEPORT_LABELS` зарезервирован.

## [3.0.0] — 2026-10-05

Первый релиз отдельным проектом. Код — расширение `teleport-agent` 2.5.0
репозитория bisquite-extensions, история перенесена.

### Добавлено
- Пакет `bisquite-teleport-agent_3.0.0_all.deb` (`tools/build-deb.sh`,
  `make deb`): `/usr/lib/bisquite-teleport/`, `/usr/sbin/bisquite-teleport`,
  юниты в `/usr/lib/systemd/system/`. Сборка воспроизводимая.
- `postinst` переводит машину с расширения 2.x: копии юнитов из
  `/etc/systemd/system` уходят в `/var/lib/bisquite/teleport/2.x-units/`,
  ссылки включения — на юниты пакета, `/usr/local/sbin/bisquite-teleport` —
  ссылка на `/usr/sbin/bisquite-teleport`. Конфиг, регистрация, токен меток и
  работающий агент не трогаются.
- `prerm`/`postrm`: удаление и `purge` регистрацию не стирают.
- Команда `bisquite-teleport version`.
- `docs/portal-labels-api.md` — контракт `GET /robot-api/v1/labels`.
- LICENSE (PolyForm Noncommercial 1.0.0), NOTICE, SECURITY.md, CI: shellcheck,
  тесты, сборка `.deb`.

### Изменено
- CLI вызывается как `/usr/sbin/bisquite-teleport` (юниты, команда метки
  `os_logins` в `teleport.yaml`).
- Бинарь Teleport пакет не ставит: его ставит обёртка bisquite-extensions
  (`TELEPORT_VERSION/MIRROR/SHA256`), скрипт портала или оператор.

### Удалено
- `install.sh`, `update.sh` и `extension.yaml` расширения — они остались в
  обёртке `teleport-agent` 3.x в bisquite-extensions.

### Не изменилось
- Пути `/etc/bisquite/teleport`, `/var/lib/bisquite/teleport`, имена юнитов,
  метка `managed_by=bisquite-teleport-agent`, контракты `apps.d` и
  `/robot-api/v1/labels`, ключи `join`.
