#!/usr/bin/env bash
# PostToolUse-хук (Edit|Write|MultiEdit): быстрый форматтер/линтер по одному файлу.
#   *.go  — gofmt -w в контейнере golang (тот же образ, что в Makefile);
#   shell — shellcheck (бинарь на хосте или контейнер koalaman/shellcheck).
# Нет docker/тулзы или таймаут — тихий выход 0: хук не роняет сессию.
# Находки shellcheck возвращаются агенту (exit 2 в PostToolUse — подсказка,
# а не блокировка правки). Полная проверка — make lint.
set -euo pipefail

INPUT="$(cat)"
FILE="$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // empty' 2>/dev/null || true)"
[ -z "$FILE" ] && exit 0
case "$FILE" in /*) ;; *) FILE="${CLAUDE_PROJECT_DIR:-$PWD}/$FILE" ;; esac
[ -f "$FILE" ] || exit 0

GO_IMAGE="${GO_IMAGE:-golang:1.25.11}"
SC_IMAGE="${SC_IMAGE:-koalaman/shellcheck:stable}"
DIR="$(dirname "$FILE")"
BASE="$(basename "$FILE")"

is_shell() {
    case "$1" in
        *.sh | *.bash | *.bats) return 0 ;;
        *.md | *.yml | *.yaml | *.json | *.go | *.py | *.txt) return 1 ;;
    esac
    head -n1 "$1" 2>/dev/null | grep -Eq '^#!.*(bash|/sh|dash)'
}

case "$FILE" in
    *.go)
        command -v docker >/dev/null 2>&1 || exit 0
        timeout 30 docker run --rm -u "$(id -u):$(id -g)" -v "$DIR:/w" -w /w \
            "$GO_IMAGE" gofmt -w "$BASE" >/dev/null 2>&1 || true
        ;;
    *)
        is_shell "$FILE" || exit 0
        OUT=""
        if command -v shellcheck >/dev/null 2>&1; then
            OUT="$(cd "$DIR" && timeout 20 shellcheck -x -S warning -f gcc "$BASE" 2>&1)" || true
        elif command -v docker >/dev/null 2>&1; then
            OUT="$(timeout 30 docker run --rm -v "$DIR:/mnt" -w /mnt "$SC_IMAGE" \
                -x -S warning -f gcc "$BASE" 2>&1)" || true
        fi
        if [ -n "$OUT" ]; then
            printf 'shellcheck (%s):\n%s\n' "$FILE" "$OUT" | head -n 40 >&2
            exit 2
        fi
        ;;
esac
exit 0
