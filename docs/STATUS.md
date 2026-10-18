# Статус stvor-agent

**Фаза:** старт переименования и переписывания (2026-10-18); выпущена 3.1.0 (bash).

## Сделано
- Репозиторий переименован из bisquite-teleport-agent; CI и README знают новые имена репозиториев.
- Обвязка Claude Code: AGENTS.md, CLAUDE.md, `.claude/`, lefthook, `make ci`.

## Дальше
1. Спека: агент на Go — служба-представитель портала Stvor; регистрация Teleport уезжает
   в расширение `teleport` (cream-extensions); миграция установленных машин и имён
   (пакет, бинарь, юниты, `/etc/bisquite/teleport`).
2. ADR на трудно откатываемое из спеки; Go-каркас по образцу cream (Makefile в контейнере).
3. Оставшиеся ссылки на старое имя: `debian/control.in` (Homepage), NOTICE — по спеке.

## Сломано
- Ничего известного.
