#!/usr/bin/env bash
# SessionStart-хук: снимок репозитория в контекст — ветка, статус, последние
# коммиты, слаг codebase-memory и первые 40 строк docs/STATUS.md.
# Контракт: JSON в stdout с hookSpecificOutput.additionalContext.
set -euo pipefail

cd "${CLAUDE_PROJECT_DIR:-$PWD}" 2>/dev/null || exit 0
git rev-parse --git-dir >/dev/null 2>&1 || exit 0
command -v jq >/dev/null 2>&1 || exit 0

BRANCH="$(git branch --show-current 2>/dev/null || echo '?')"
STATUS="$(git status --short 2>/dev/null | head -n 30 || true)"
COMMITS="$(git log --oneline -n 5 2>/dev/null || echo '(коммитов нет)')"
AHEAD=""
if [ "$BRANCH" != "main" ] && git rev-parse -q --verify origin/main >/dev/null 2>&1; then
    AHEAD="$(git log --oneline -n 10 origin/main..HEAD 2>/dev/null || true)"
fi
# Слаг codebase-memory: абсолютный путь, «/» → «-», ведущий срезан.
CBM_SLUG="$(pwd | sed 's|^/||; s|/|-|g')"
STATUS_MD=""
if [ -f docs/STATUS.md ]; then STATUS_MD="$(head -n 40 docs/STATUS.md)"; fi

FENCE='```'
CTX="## Снимок репозитория на старте сессии
Слаг codebase-memory: \`$CBM_SLUG\` (передавай в \`project=\`).
Ветка: \`$BRANCH\`

git status --short:
$FENCE
${STATUS:-(чисто)}
$FENCE
Последние коммиты:
$FENCE
$COMMITS
$FENCE"
if [ -n "$AHEAD" ]; then
    CTX="$CTX
Коммиты ветки поверх origin/main:
$FENCE
$AHEAD
$FENCE"
fi
if [ -n "$STATUS_MD" ]; then
    CTX="$CTX

## docs/STATUS.md (первые 40 строк)
$STATUS_MD"
fi

jq -nc --arg ctx "$CTX" '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}'
