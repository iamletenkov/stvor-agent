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

## Дальше
1. Приёмка на живом портале — задача 7 среза Stvor «Переход на cream и stvor-agent» (бывшая Task 22).
2. `stvorhost` на 3.0.1 и роботы на 3.1.x переходят на 4.x перерегистрацией по runbook портала.

## Сломано
- Ничего известного.
