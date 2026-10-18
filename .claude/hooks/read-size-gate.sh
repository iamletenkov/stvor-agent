#!/usr/bin/env bash
# PreToolUse-хук (Read): блокирует чтение файла > 2 МБ без явного limit —
# защищает контекст от логов, дампов и бинарей. Exit 2 — блокировка.
set -euo pipefail

INPUT="$(cat)"
FILE="$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // empty' 2>/dev/null || true)"
if [ -z "$FILE" ] || [ ! -f "$FILE" ]; then exit 0; fi

LIMIT="$(printf '%s' "$INPUT" | jq -r '.tool_input.limit // empty' 2>/dev/null || true)"
[ -n "$LIMIT" ] && exit 0

SIZE="$(stat -c%s "$FILE" 2>/dev/null || echo 0)"
if [ "$SIZE" -gt 2097152 ]; then
    cat >&2 <<MSG
БЛОКИРОВКА: файл $FILE — $((SIZE / 1048576)) МБ, больше 2 МБ.
Читай участок (Read с offset/limit) или выжимку (grep/head/tail в Bash).
Хук: .claude/hooks/read-size-gate.sh
MSG
    exit 2
fi
exit 0
