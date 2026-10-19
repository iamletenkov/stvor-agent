#!/usr/bin/env bash
# Сверка docs/test-map.md (итоговая карта переноса проверок tests/test-labels.sh
# 3.x; сам скрипт удалён в Task 19, номера его строк — справка) с Go-тестами:
#   - у каждой строки таблицы есть итог (Go, T, Go+T, снято) и непустое «куда»;
#   - каждое имя `pkg.TestName` есть в выводе `go test -list . ./...`,
#     подтест `pkg.TestName/имя` — строкой "имя" в .go-файлах пакета;
#   - итог Go/Go+T несёт хотя бы одно имя Go-теста, T/Go+T — тест расширения
#     (test-managed.sh или test-join.sh), «снято» — причину («снято: …»).
#
#   tools/check-test-map.sh <вывод go test -list . ./...>
#
# Go на хосте не нужен: список тестов строит make lint в контейнере.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(dirname "$HERE")"
MAP="${TEST_MAP:-$REPO/docs/test-map.md}"
LIST="${1:?usage: tools/check-test-map.sh <go test -list output>}"

rc=0
fail(){ echo "test-map: $*" >&2; rc=1; }

[[ -f "$MAP" ]] || { echo "test-map: нет $MAP" >&2; exit 1; }
[[ -f "$LIST" ]] || { echo "test-map: нет списка тестов $LIST" >&2; exit 1; }

# "pkg.TestName" for every listed test: the names come before the
# "ok <import path>" line of their package.
known="$(awk '
    /^(ok|FAIL|\?)[ \t]/ { n = split($2, p, "/"); for (i = 0; i < c; i++) print p[n] "." names[i]; c = 0; next }
    /^(Test|Example|Benchmark|Fuzz)/ { names[c++] = $1 }
' "$LIST")"
[[ -n "$known" ]] || fail "в $LIST нет ни одного теста"

# The directory of a Go package by its last path element.
pkg_dir(){
    local d
    for d in "$REPO"/internal/*/ "$REPO"/internal/*/*/ "$REPO"/packaging/ "$REPO"/cmd/*/; do
        d="${d%/}"
        [[ "$(basename "$d")" == "$1" ]] && { printf '%s\n' "$d"; return 0; }
    done
    return 1
}

rows=0; n_go=0; n_t=0; n_both=0; n_gone=0; want_no=1
while IFS= read -r row; do
    rows=$((rows + 1))
    IFS='|' read -r _ no line what verdict where _ <<< "$row"
    no="${no// /}"; line="${line// /}"
    verdict="$(sed -E 's/^ +| +$//g' <<< "$verdict")"
    where="$(sed -E 's/^ +| +$//g' <<< "$where")"
    what="$(sed -E 's/^ +| +$//g' <<< "$what")"
    [[ "$no" == "$want_no" ]] || fail "строка №$no: ждали №$want_no (номера по порядку)"
    want_no=$((no + 1))
    [[ "$line" =~ ^[0-9]+$ ]] || fail "№$no: «Стр.» не число: [$line]"
    [[ -n "$what" ]] || fail "№$no: пустая «Проверка»"
    [[ -n "$where" ]] || fail "№$no: пустое «Куда»"

    # shellcheck disable=SC2016  # backticks are the markdown code span, not a substitution
    refs="$(grep -oE '`[a-z0-9]+\.(Test|Example|Fuzz)[A-Za-z0-9_]*(/[^`]+)?`' <<< "$where" | tr -d '`')"
    has_t=0; grep -qE 'test-(managed|join)\.sh' <<< "$where" && has_t=1
    case "$verdict" in
        Go)    n_go=$((n_go + 1)); [[ -n "$refs" ]] || fail "№$no: итог Go без имени Go-теста" ;;
        T)     n_t=$((n_t + 1)); (( has_t )) || fail "№$no: итог T без test-managed.sh/test-join.sh" ;;
        Go+T)  n_both=$((n_both + 1))
               [[ -n "$refs" ]] || fail "№$no: итог Go+T без имени Go-теста"
               (( has_t )) || fail "№$no: итог Go+T без test-managed.sh/test-join.sh" ;;
        снято) n_gone=$((n_gone + 1)); grep -q 'снято:' <<< "$where" || fail "№$no: итог «снято» без «снято: <причина>»" ;;
        *)     fail "№$no: итог [$verdict] — не Go, T, Go+T или снято" ;;
    esac

    while IFS= read -r ref; do
        [[ -n "$ref" ]] || continue
        top="${ref%%/*}"
        grep -qxF "$top" <<< "$known" || { fail "№$no: нет теста $top (go test -list)"; continue; }
        [[ "$ref" == */* ]] || continue
        sub="${ref#*/}"; pkg="${top%%.*}"
        if ! dir="$(pkg_dir "$pkg")"; then fail "№$no: не найден каталог пакета $pkg"; continue; fi
        grep -qF "\"$sub\"" "$dir"/*.go || fail "№$no: нет подтеста \"$sub\" в $pkg ($ref)"
    done <<< "$refs"
done < <(grep -E '^\| *[0-9]+ *\|' "$MAP")

(( rows > 0 )) || fail "в $MAP нет строк таблицы"

echo "test-map: строк $rows — Go $n_go, Go+T $n_both, T $n_t, снято $n_gone"
exit "$rc"
