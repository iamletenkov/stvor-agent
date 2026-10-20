# Документация stvor-agent

Где что лежит и когда писать документ — [.claude/rules/docs.md](../.claude/rules/docs.md).
Новый документ без строки в этой таблице не считается готовым. Пользовательская документация
(установка через cream и без, CLI и коды выхода, файлы, журнал, прокси, диагностика, отличия от 3.x) — в [README.md](../README.md), операционные процедуры — в `runbooks/`, безопасность — [SECURITY.md](../SECURITY.md).

| Документ | Зачем читать |
|---|---|
| [STATUS.md](STATUS.md) | состояние работ: что сделано, что дальше, что сломано |
| [runbooks/01_release.md](runbooks/01_release.md) | выпуск версии: релизный PR, `.deb` и `SHA256SUMS`, пин в cream-extensions и на портале |
| [runbooks/02_robot-diagnostics.md](runbooks/02_robot-diagnostics.md) | диагностика на роботе: `status --json`, журнал службы, файл токена, метка `stvor_agent` в Teleport |
| [runbooks/03_token-reissue-and-move.md](runbooks/03_token-reissue-and-move.md) | перевыпуск токена меток, смена домена портала, замена машины, вывод робота из работы |
| [portal-labels-api.md](portal-labels-api.md) | контракт `GET /robot-api/v1/labels` v1 — запрос, ответы и коды, строгий разбор тела, ETag и `X-Applied-Revision`, метка `stvor_agent` |
| [adr/README.md](adr/README.md) | индекс архитектурных решений; шаблон — [adr/0000-template.md](adr/0000-template.md) |
| [specs/2026-10-18-stvor-agent-go.md](specs/2026-10-18-stvor-agent-go.md) | спека stvor-agent на Go и выделения расширения `teleport` в cream-extensions |
| [plans/2026-10-18-stvor-agent-go.md](plans/2026-10-18-stvor-agent-go.md) | план stvor-agent на Go и расширения `teleport`: 23 задачи, волны A (без cream), B (после cream 1.0.0), C; закрыт выпуском 4.0.0 |
| [test-map.md](test-map.md) | карта переноса 229 проверок `tests/test-labels.sh` 3.x в Go-тесты и в тесты расширения `teleport`; сверяет `make lint` |
| [research/2026-10-18-agent-split-inventory.md](research/2026-10-18-agent-split-inventory.md) | карта раздела bash-агента: что уходит в `teleport`, что в stvor-agent, что меняется в Stvor |
| [research/2026-10-18-stvor-adr-0030-draft.md](research/2026-10-18-stvor-adr-0030-draft.md) | черновик ADR-0030 портала Stvor и набросок его среза — переносится в Stvor, когда репозиторий освободится |
| [../README.md](../README.md) | что такое stvor-agent 4.x, установка, команды, файлы, журнал, диагностика, разработка и выпуск |
| [../SECURITY.md](../SECURITY.md) | модель угроз агента (root, токен, окружение дочерних процессов, hardening юнита), как сообщить об уязвимости, исправление 3.1.1 |
| [../AGENTS.md](../AGENTS.md) | регламент для агентов и людей: карта репозитория, как работать, DoD, выпуск |
