#!/usr/bin/env bash
# Снимок образца для internal/collect/testdata (план Stvor 2026-10-21-robot-telemetry,
# Task 21): копия того, что сборщик телеметрии читает без root, — /proc и /sys
# машины — в каталог-корень образца. Запускается на самой машине от обычного
# пользователя, ничего не меняет и ничего не ставит.
#
#   tools/collect-fixture.sh <каталог-образца>
#   ROOT=<корень> tools/collect-fixture.sh <каталог-образца>   # из другого корня (тест)
#
# Обезличивание (как в testdata/README.md):
#   - MAC-адресов, серийников и имени хоста в снимке нет: эти файлы не копируются;
#   - boot_id заменён выдуманным;
#   - имена пользователей (uid 1000–59999) в mountinfo заменены на «user».
# Симлинки sysfs (device, phy80211, wireless) — пустыми каталогами с .keep, как в
# прежних образцах: сборщику важно их наличие, а не цель.
set -euo pipefail
shopt -s nullglob

out="${1:?usage: tools/collect-fixture.sh <каталог-образца>}"
root="${ROOT:-}"
[[ -e "$out" ]] && { echo "collect-fixture: $out уже есть" >&2; exit 1; }
mkdir -p "$out"

# copy <путь от корня>: обычный файл, если он читается; иначе — молча мимо.
copy() {
    local src="$root/$1"
    [[ -f "$src" && -r "$src" ]] || return 0
    mkdir -p "$out/$(dirname "$1")"
    cat "$src" > "$out/$1" 2>/dev/null || rm -f "$out/$1"
}
# mark <путь>: наличие ссылки или каталога.
mark() {
    [[ -e "$root/$1" ]] || return 0
    mkdir -p "$out/$1"
    : > "$out/$1/.keep"
}

for f in diskstats loadavg meminfo net/dev net/wireless pressure/memory stat uptime; do
    copy "proc/$f"
done
mkdir -p "$out/proc/sys/kernel/random"
echo "00000000-0000-4000-8000-000000000001" > "$out/proc/sys/kernel/random/boot_id"

# mountinfo без имён пользователей.
users="$(awk -F: '$3 >= 1000 && $3 < 60000 { print $1 }' "$root/etc/passwd" 2>/dev/null || true)"
mkdir -p "$out/proc/self"
mi="$(cat "$root/proc/self/mountinfo")"
for u in $users; do
    mi="${mi//\/$u\//\/user\/}"
    mi="${mi//\/$u /\/user }"
done
printf '%s\n' "$mi" > "$out/proc/self/mountinfo"

rel() { printf '%s' "${1#"$root"/}"; }

for d in "$root"/sys/block/*; do
    copy "$(rel "$d")/dev"
done

for d in "$root"/sys/class/net/*; do
    n="$(rel "$d")"
    for a in carrier carrier_changes duplex ifindex mtu operstate speed type; do
        copy "$n/$a"
    done
    for l in device phy80211 wireless; do
        mark "$n/$l"
    done
    [[ -e "$out/$n" ]] || mark "$n"
done

for d in "$root"/sys/class/thermal/thermal_zone*; do
    copy "$(rel "$d")/type"
    copy "$(rel "$d")/temp"
done

for d in "$root"/sys/class/hwmon/hwmon*; do
    for f in "$d"/name "$d"/temp*_input "$d"/temp*_label "$d"/device/name "$d"/device/temp*_input "$d"/device/temp*_label; do
        if [[ -e "$f" ]]; then copy "$(rel "$f")"; fi
    done
done

copy sys/devices/system/cpu/online
for d in "$root"/sys/devices/system/cpu/cpu[0-9]*; do
    n="$(rel "$d")"
    copy "$n/online"
    copy "$n/cpufreq/scaling_cur_freq"
    copy "$n/thermal_throttle/core_throttle_count"
    copy "$n/thermal_throttle/package_throttle_count"
done

copy var/run/reboot-required
echo "collect-fixture: $(find "$out" -type f | wc -l) файлов в $out"
