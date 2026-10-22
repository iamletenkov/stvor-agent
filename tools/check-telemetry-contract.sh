#!/usr/bin/env bash
# Сверка копии контракта телеметрии v1 с пином (план телеметрии Stvor, Task 15, R7). Без сети.
#
# Эталон — backend/app/modules/robot_telemetry/contract/v1.json портала Stvor, его пин —
# contract/v1.sha256 там же. Агент держит побайтную копию internal/telemetry/contract/v1.json
# (зашита в бинарь: диапазоны конфигурации, белый список метрик, лимиты) и пин
# tools/telemetry-contract.sha256 — строку «<sha256>  v1.json», как у портала.
#
# Проверки:
#   1. sha256 копии равен пину — всегда;
#   2. если задан STVOR_CONTRACT (путь к v1.json портала) — копия совпадает с ним побайтно.
#
# Ручки для тестов: CONTRACT_FILE, PIN_FILE.
#
#   tools/check-telemetry-contract.sh            код 0 — копия сходится с пином
#   tools/check-telemetry-contract.sh --print    печатает sha256 копии
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(dirname "$HERE")"
CONTRACT_FILE="${CONTRACT_FILE:-$REPO/internal/telemetry/contract/v1.json}"
PIN_FILE="${PIN_FILE:-$HERE/telemetry-contract.sha256}"

[[ -f "$CONTRACT_FILE" ]] || { echo "ОШИБКА: нет $CONTRACT_FILE" >&2; exit 1; }
local_sum="$(sha256sum < "$CONTRACT_FILE" | cut -c 1-64)"
if [[ "${1:-}" == --print ]]; then
    echo "$local_sum"; exit 0
fi

[[ -f "$PIN_FILE" ]] || { echo "ОШИБКА: нет пина $PIN_FILE" >&2; exit 1; }
pin="$(grep -v '^#' "$PIN_FILE" | awk 'NF { print $1; exit }')"
if [[ ! "$pin" =~ ^[0-9a-f]{64}$ ]]; then
    echo "ОШИБКА: в $PIN_FILE нет sha256" >&2; exit 1
fi
if [[ "$local_sum" != "$pin" ]]; then
    echo "ОШИБКА: контракт телеметрии $CONTRACT_FILE ($local_sum) не совпадает с пином $PIN_FILE ($pin)" >&2
    exit 1
fi
if [[ -n "${STVOR_CONTRACT:-}" ]]; then
    if ! cmp -s "$CONTRACT_FILE" "$STVOR_CONTRACT"; then
        echo "ОШИБКА: копия контракта отличается от $STVOR_CONTRACT" >&2; exit 1
    fi
    echo "контракт телеметрии: совпадает с $STVOR_CONTRACT"
fi
echo "контракт телеметрии: копия совпадает с пином ($pin)"
