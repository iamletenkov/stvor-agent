# Статус stvor-agent

**Фаза:** выпущен 4.0.1 (Go) — [план](plans/2026-10-18-stvor-agent-go.md) закрыт (23/23). Сопровождение 4.x.

## Сделано
- 4.0.0: Go-служба, `.deb` amd64/arm64 (nfpm, воспроизводимо), CI и release-please, сквозной тест в Debian 12
  с systemd; bash 3.x удалён из `main` (код — в тегах `v3.*`).
- 4.0.1: man-страница `stvor-agent(8)` в `.deb` (`/usr/share/man/man8/`).
- Каталог cream-extensions (`main`, релиз 5.1.0): расширение `teleport` и обёртка `stvor-agent` 4.1.0 с пином
  агента 4.0.0; машины Debian ≥ 12 и Ubuntu ≥ 20.04.
- Портал Stvor ставит роботов на cream 1.1.2, каталог 5.1.0 и stvor-agent 4.0.0 (ADR-0031 портала).
- Документация: README, [первые шаги](getting-started.md), [руководства](guides/), [справочник](reference/cli.md),
  [runbook выпуска](runbooks/01_release.md).

## В работе — телеметрия 4.1.0 (ветка `feature/telemetry`, план Stvor `2026-10-21-robot-telemetry`)
- Task 21, Jetson: бюджет на Nano (systemd 245, cgroup v1) — обычный режим 0,54 % ядра, режим 5 с 0,99 %,
  RSS ≤ 16,7 МБ ([замер](research/2026-10-22-telemetry-budget-jetson.md)); gzip-писатель переиспользуется (−30 % CPU).
  pve-robot-1 и stvorhost (amd64, cgroup v2): 0,03–0,10 % ядра, RSS ≤ 19,5 МБ; образец `pve-robot-debian13`
  и `tools/collect-fixture.sh`. Не сделано: живой systemd 237, выпуск 4.1.0.

## В работе — OpenWrt (план cream `docs/plans/2026-10-22-openwrt-robots.md`, задачи 3–4)
- Task 3 в `main`: `internal/platform`, `internal/initsys` (systemd/procd), пути `/opt/stvor-agent`, свой machine-id.
- Task 4: ассет `stvor-agent_<v>_linux_{amd64,arm64}.tar.gz` (бинарь, procd-скрипты `init.d/`, man, лицензии) в общем
  `SHA256SUMS`, `make e2e-openwrt` на корневой ФС openwrt-builder v0.3.1, `SECURITY.md` (OpenWrt без песочницы).
  Выпуск — вместе с 4.1.0 или следующим минором, по сигналу сессии Stvor.

## Дальше
1. Приёмка на живом портале — задача 7 среза Stvor «Переход на cream и stvor-agent» (бывшая Task 22).
2. `stvorhost` на 3.0.1 и роботы на 3.1.x переходят на 4.x перерегистрацией по runbook портала.

## Сломано
- Ничего известного.
