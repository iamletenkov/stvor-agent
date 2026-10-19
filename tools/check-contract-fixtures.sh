#!/usr/bin/env bash
# Сверка файлов-образцов контракта stvor-agent ↔ расширение teleport (спека
# §5.7, §10.7; план, Task 19). Без сети.
#
# Эталон — здесь, internal/teleport/testdata/*.json; cream-extensions держит
# побайтную копию в extensions/debian/teleport/tests/fixtures/. Пин — не коммит,
# а sha256 содержимого набора:
#
#   for f in $(LC_ALL=C sort <имена *.json>); do printf '%s %s\n' "$f" "$(sha256 файла)"; done | sha256
#
# то есть sha256 от отсортированного списка строк «имя␠sha256 файла» (каждая с
# \n). Та же формула — в tools/check-contract-fixtures.sh cream-extensions.
#
# Проверки:
#   1. набор совпадает с пином tools/contract-fixtures.sha256 — всегда;
#   2. соседняя копия cream-extensions (CREAM_EXTENSIONS_DIR, по умолчанию
#      ../cream-extensions рядом с репозиторием) несёт тот же набор. Нет копии
#      или в ней нет образцов (main cream-extensions до слияния пилота) —
#      локально пропуск с предупреждением; в CI (CI=true|1) это ошибка: копию
#      даёт шаг checkout по ref (CE_CONTRACT_REF) в ci-release.yml.
#
# Ручки для тестов: FIXTURES_DIR, PIN_FILE.
#
#   tools/check-contract-fixtures.sh            код 0 — набор сходится
#   tools/check-contract-fixtures.sh --print    печатает sha256 набора
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(dirname "$HERE")"
FIXTURES_DIR="${FIXTURES_DIR:-$REPO/internal/teleport/testdata}"
PIN_FILE="${PIN_FILE:-$HERE/contract-fixtures.sha256}"
CREAM_EXTENSIONS_DIR="${CREAM_EXTENSIONS_DIR:-$(dirname "$REPO")/cream-extensions}"
CE_FIXTURES="$CREAM_EXTENSIONS_DIR/extensions/debian/teleport/tests/fixtures"

# set_list <dir> — "имя sha256" по каждому *.json верхнего уровня, по имени.
set_list(){
    local f
    while IFS= read -r f; do
        printf '%s %s\n' "$f" "$(sha256sum < "$1/$f" | cut -c 1-64)"
    done < <(find "$1" -maxdepth 1 -type f -name '*.json' -printf '%f\n' | LC_ALL=C sort)
}
set_sum(){ set_list "$1" | sha256sum | cut -c 1-64; }

[[ -d "$FIXTURES_DIR" ]] || { echo "ОШИБКА: нет $FIXTURES_DIR" >&2; exit 1; }
if [[ -z "$(set_list "$FIXTURES_DIR")" ]]; then
    echo "ОШИБКА: в $FIXTURES_DIR нет *.json" >&2; exit 1
fi
local_sum="$(set_sum "$FIXTURES_DIR")"
if [[ "${1:-}" == --print ]]; then
    echo "$local_sum"; exit 0
fi

rc=0
pin="$(grep -oE '^[0-9a-f]{64}' "$PIN_FILE" 2>/dev/null | head -1)"
if [[ -z "$pin" ]]; then
    echo "ОШИБКА: в $PIN_FILE нет sha256 набора" >&2; rc=1
elif [[ "$pin" != "$local_sum" ]]; then
    echo "ОШИБКА: файлы-образцы $FIXTURES_DIR не совпадают с пином $PIN_FILE" >&2
    echo "  пин $pin, набор $local_sum — правка образца меняет контракт: пин, копия в cream-extensions и её пин — вместе" >&2
    rc=1
else
    echo "образцы контракта: набор совпадает с пином ($pin)"
fi

if [[ ! -d "$CE_FIXTURES" ]]; then
    if [[ "${CI:-}" == true || "${CI:-}" == 1 ]]; then
        echo "ОШИБКА: нет копии cream-extensions с образцами ($CE_FIXTURES) — шаг checkout cream-extensions в CI?" >&2
        rc=1
    else
        echo "ВНИМАНИЕ: сверка с cream-extensions пропущена — нет $CE_FIXTURES (CREAM_EXTENSIONS_DIR=…)" >&2
    fi
elif ! diff -u --label stvor-agent --label cream-extensions \
        <(set_list "$FIXTURES_DIR") <(set_list "$CE_FIXTURES") >&2; then
    echo "ОШИБКА: образцы расходятся с $CE_FIXTURES — скопируйте их туда побайтно и обновите пин там" >&2
    rc=1
else
    echo "образцы контракта: совпадают с $CE_FIXTURES"
fi
exit "$rc"
