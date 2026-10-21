# Отличия 3.x и 4.x

4.x — новая реализация на Go, а не обновление `bisquite-teleport-agent` 3.x (bash): переходного пакета и
миграции нет. Машину переводят перерегистрацией по runbook портала Stvor.

| `bisquite-teleport-agent` 3.x (bash) | `stvor-agent` 4.x (Go) |
|---|---|
| пакет `bisquite-teleport-agent_<v>_all.deb`, CLI `bisquite-teleport` | `stvor-agent_<v>_<arch>.deb`, `/usr/bin/stvor-agent` |
| регистрация, `teleport.yaml`, `apps.d`, `os_logins`, `leave` | расширение `teleport` каталога cream (`cream run teleport …`) |
| `labels-enable`, таймер `bisquite-teleport-labels.timer`, `pull` | `stvor-agent enable`, служба `stvor-agent.service`, `sync` |
| метки портала — правка `TELEPORT_LABELS`/`TELEPORT_ENV` | управляемые метки `TELEPORT_MANAGED_LABELS` через `set-managed` |
| `/etc/bisquite/teleport/labels-token` | `/etc/stvor-agent/token` (тот же формат) |
| метки `managed_by=bisquite-teleport-agent`, `bisquite_agent_version` | динамическая метка `stvor_agent` |

Контракт `/robot-api/v1/labels` не изменился. `bisquite-teleport-agent` 3.1.x поддерживается, пока
портал Stvor пинит 3.x; 3.1.1 — исправление безопасности
([SECURITY.md](../../SECURITY.md#311-версия-кластера-в-арифметике-bash)). Код 3.x удалён из `main` и
живёт в тегах `v3.*`.
