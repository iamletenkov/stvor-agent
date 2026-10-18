# Статус stvor-agent

**Фаза:** старт переименования и переписывания (2026-10-18); выпущена 3.1.0 (bash).

## Сделано
- Репозиторий переименован из bisquite-teleport-agent; CI и README знают новые имена репозиториев.
- Обвязка Claude Code: AGENTS.md, CLAUDE.md, `.claude/`, lefthook, `make ci`.

## Дальше
1. План по [спеке](specs/2026-10-18-stvor-agent-go.md) — после MVP cream (зависит от `cream run`/`cream set`);
   переходных пакетов нет. Портал пинит 3.1.0, выпуск 4.0.0 его не задевает до среза Stvor.
2. ADR-0030 портала — [черновик](research/2026-10-18-stvor-adr-0030-draft.md); переносится в Stvor отдельным срезом.
3. Оставшиеся ссылки на старое имя: `debian/control.in` (Homepage), NOTICE — по спеке.

## Сломано
- Ничего известного.
