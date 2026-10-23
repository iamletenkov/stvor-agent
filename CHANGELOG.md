# Изменения

Версии — [SemVer](https://semver.org/lang/ru/). Секции новее 3.0.1 пишет
release-please из Conventional Commits; 3.0.1 и старше написаны вручную в
формате [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/).

## [4.1.1](https://github.com/iamletenkov/stvor-agent/compare/v4.1.0...v4.1.1) (2026-10-23)


### Исправлено

* **initsys:** enable на OpenWrt не печатает ошибку ubus и называет службу по-procd ([#25](https://github.com/iamletenkov/stvor-agent/issues/25)) ([52d5c3f](https://github.com/iamletenkov/stvor-agent/commit/52d5c3f1507ae67eac0d36d63521548472bd0188))
* **initsys:** systemctl ищется в /usr/sbin:/usr/bin:/sbin:/bin, а не только в /usr/bin ([#27](https://github.com/iamletenkov/stvor-agent/issues/27)) ([161816b](https://github.com/iamletenkov/stvor-agent/commit/161816b7b2b39a97274d9b0286f587878c64260e))

## [4.1.0](https://github.com/iamletenkov/stvor-agent/compare/v4.0.1...v4.1.0) (2026-10-22)


### Добавлено

* **initsys:** агент работает на OpenWrt — procd, пути /opt и расширение teleport-openwrt ([#22](https://github.com/iamletenkov/stvor-agent/issues/22)) ([0e7a1fa](https://github.com/iamletenkov/stvor-agent/commit/0e7a1fa7f2a404e4428bf784cfa0a0f1fde9e7b4))
* **release:** tar.gz для OpenWrt с procd-скриптами в общем SHA256SUMS и e2e на корневой ФС OpenWrt ([#23](https://github.com/iamletenkov/stvor-agent/issues/23)) ([2237c89](https://github.com/iamletenkov/stvor-agent/commit/2237c898da8fe77f493bdfb76a793f6218a78581))
* **telemetry:** телеметрия робота — сборщики, инвентарь, буфер и служба по контракту v1 ([#21](https://github.com/iamletenkov/stvor-agent/issues/21)) ([39cb83c](https://github.com/iamletenkov/stvor-agent/commit/39cb83c3c3932c51c5ad4fdfb2fdc85ec3a0bd1d))

## [4.0.1](https://github.com/iamletenkov/stvor-agent/compare/v4.0.0...v4.0.1) (2026-10-20)


### docs

* **status:** выпуск 4.0.1 с man-страницей stvor-agent(8) ([#16](https://github.com/iamletenkov/stvor-agent/issues/16)) ([9c2786d](https://github.com/iamletenkov/stvor-agent/commit/9c2786df11123b805266414b9157cae01d58b364))

## [4.0.0](https://github.com/iamletenkov/stvor-agent/compare/v3.1.0...v4.0.0) (2026-10-19)


### ⚠ BREAKING CHANGES

* пакет переименован в stvor-agent, ставится через cream; bash-агент bisquite-teleport-agent 3.x выпускается из ветки fix/3.1.1-version-check.

### Добавлено

* stvor-agent 4.0 — служба портала на Go ([93df186](https://github.com/iamletenkov/stvor-agent/commit/93df1863e0cd8f703a32ab0f8325ddfa26b86eb9))

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
