# Документация stvor-agent

Где что лежит и когда писать документ — [.claude/rules/docs.md](../.claude/rules/docs.md).
Новый документ без строки в этой таблице не считается готовым. Пользовательская документация
(установка, CLI, контракт `apps.d`, диагностика) — в [README.md](../README.md), безопасность — [SECURITY.md](../SECURITY.md).

| Документ | Зачем читать |
|---|---|
| [STATUS.md](STATUS.md) | состояние работ: что сделано, что дальше, что сломано |
| [portal-labels-api.md](portal-labels-api.md) | контракт `GET /robot-api/v1/labels` — метки `space`/`env` с портала |
| [adr/README.md](adr/README.md) | индекс архитектурных решений; шаблон — [adr/0000-template.md](adr/0000-template.md) |
| [specs/2026-10-18-stvor-agent-go.md](specs/2026-10-18-stvor-agent-go.md) | спека stvor-agent на Go и выделения расширения `teleport` в cream-extensions |
| [plans/2026-10-18-stvor-agent-go.md](plans/2026-10-18-stvor-agent-go.md) | план stvor-agent на Go и расширения `teleport`: 23 задачи, волны A (без cream), B (после cream 1.0.0), C |
| [test-map.md](test-map.md) | карта переноса 229 проверок `tests/test-labels.sh` 3.x в Go-тесты и в тесты расширения `teleport`; сверяет `make lint` |
| [research/2026-10-18-agent-split-inventory.md](research/2026-10-18-agent-split-inventory.md) | карта раздела bash-агента: что уходит в `teleport`, что в stvor-agent, что меняется в Stvor |
| [research/2026-10-18-stvor-adr-0030-draft.md](research/2026-10-18-stvor-adr-0030-draft.md) | черновик ADR-0030 портала Stvor и набросок его среза — переносится в Stvor, когда репозиторий освободится |
