# Статус stvor-agent

**Фаза:** stvor-agent 4.x на Go — волна C [плана](plans/2026-10-18-stvor-agent-go.md) (упаковка, CI, документация,
удаление 3.x). Ветка `feature/go-agent`; выпущенный релиз — 3.1.1 (bash, исправление безопасности).

## Сделано
- Волна A (Task 1–9): Go-служба — клиент портала, токен и состояние, адаптер `cream run teleport`, тик, цикл
  службы, юнит, CLI; карта переноса проверок 3.x — [test-map.md](test-map.md).
- Волна B (Task 10–13, 15): расширения `teleport` и `stvor-agent` в ветке `feature/cream-v1-pilot`
  cream-extensions; интеграционный тест адаптера с настоящим cream.
- Task 16: `.deb` amd64/arm64 собирает nfpm, `make repro-check` — побайтно воспроизводимо.
- Task 18: README, SECURITY, контракт портала, AGENTS/CLAUDE и индекс — под 4.x.

## В работе / дальше
1. Task 14 (CE: `apps.d` у `code-server`/`selkies`, удаление `teleport-agent`, `agent-bump`), Task 17 (CI и
   release-please 4.0.0).
2. Task 19 — удаление bash 3.x и сверка файлов-образцов с CE; Task 20 — сквозной тест в Debian 12 с systemd.
3. Task 21 — релиз 4.0.0 (только с «да» владельца), Task 22 — приёмка на `pve-robot-1`, Task 23 — `make ci`.
4. Портал пинит 3.1.x и переходит на 4.x своим срезом по ADR-0030
   ([черновик](research/2026-10-18-stvor-adr-0030-draft.md)); `stvorhost` (3.0.1) не трогаем.

## Сломано
- Ничего известного. Ссылки README на расширения cream-extensions ведут в `main` — заработают после слияния
  пилота.
