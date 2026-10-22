# Документация stvor-agent

Начните с [первых шагов](getting-started.md). Новый документ добавляется строкой в этот индекс.

## Начало

- [Первые шаги](getting-started.md) — cream, узел Teleport, агент, включение токеном, проверка.

## Руководства

| Задача | Документ |
|---|---|
| поставить агент: скриптом портала, без сети, в образ, голым `.deb`; обновить | [guides/install-on-robot.md](guides/install-on-robot.md) |
| понять, почему метки не применяются: `status --json`, журнал, токен, метки в Teleport | [guides/diagnostics.md](guides/diagnostics.md) |
| включить агент, перевыпустить токен, перенести робота, вывести из работы | [guides/token-and-move.md](guides/token-and-move.md) |

## Справочник

| Что | Документ |
|---|---|
| команды, коды выхода, `status --json` | [reference/cli.md](reference/cli.md) |
| настройки, прокси, файлы, служба, события журнала | [reference/configuration.md](reference/configuration.md) |
| контракт `GET /robot-api/v1/labels` v1: запрос, ответы, разбор тела, ревизии, метка `stvor_agent` | [reference/portal-labels-api.md](reference/portal-labels-api.md) |

## Устройство и разработка

| Документ | Зачем читать |
|---|---|
| [architecture.md](architecture.md) | место в системе, один опрос, расписание, пакеты Go |
| [../SECURITY.md](../SECURITY.md) | модель угроз, токен, окружение дочерних процессов, hardening юнита, как сообщить об уязвимости |
| [development.md](development.md) | сборка, тесты, как вносить изменения |
| [runbooks/01_release.md](runbooks/01_release.md) | выпуск версии: релизный PR, `.deb`, `tar.gz` и `SHA256SUMS`, пин в cream-extensions и на портале |
| [test-map.md](test-map.md) | карта переноса проверок bash-агента 3.x в Go-тесты; её сверяет `make lint` |
| [adr/README.md](adr/README.md) | индекс архитектурных решений |
| [../CHANGELOG.md](../CHANGELOG.md) | изменения по версиям |

## История и проектные документы

Для пользования агентом не нужны.

| Документ | Что это |
|---|---|
| [history/3x-to-4x.md](history/3x-to-4x.md) | отличия `bisquite-teleport-agent` 3.x и `stvor-agent` 4.x |
| [specs/2026-10-18-stvor-agent-go.md](specs/2026-10-18-stvor-agent-go.md) | исходная спека агента на Go и выделения расширения `teleport` |
| [plans/2026-10-18-stvor-agent-go.md](plans/2026-10-18-stvor-agent-go.md) | план переписывания (закрыт выпуском 4.0.0) |
| [research/2026-10-18-agent-split-inventory.md](research/2026-10-18-agent-split-inventory.md) | карта раздела bash-агента между `teleport` и stvor-agent |
| [research/2026-10-22-telemetry-budget-jetson.md](research/2026-10-22-telemetry-budget-jetson.md) | замер бюджета службы телеметрии на Jetson Nano (CPU, RSS), профиль, `verify` юнита на systemd 237 и 245 |
| [history/2026-10-18-stvor-adr-0030-draft.md](history/2026-10-18-stvor-adr-0030-draft.md) | черновик ADR портала Stvor; принят там как ADR-0031 |
| [STATUS.md](STATUS.md) | текущее состояние работ (handoff между сессиями) |
