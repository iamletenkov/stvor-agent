#!/usr/bin/env bash
# shellcheck disable=SC2016  # literal $… in fakes is the test data
# Тесты join без сети при первой загрузке: разбор ошибок (сеть — ждать,
# отказ кластера — не повторять), файлы состояния 0600, стирание секрета,
# тик повтора (join-retry), идемпотентность, блокировка и status — на
# подменённом корне (BISQUITE_TELEPORT_ROOT) с поддельными systemctl,
# journalctl, curl и teleport. root, systemd и сеть не нужны.
#
#   tests/test-join.sh        код 0 — все проверки прошли

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(dirname "$HERE")"
SRC="$REPO/src"

TMP="$(mktemp -d)"
trap 'rm -r -f -- "$TMP"' EXIT

pass=0; fail=0
ok(){ pass=$((pass + 1)); }
# fd 3: the real stderr — a check called with 2>/dev/null must still report.
exec 3>&2
bad(){ fail=$((fail + 1)); echo "FAIL: $*" >&3; }
check(){ local what="$1"; shift; if "$@"; then ok; else bad "$what"; fi; }
check_not(){ local what="$1"; shift; if "$@"; then bad "$what"; else ok; fi; }
eq(){ local what="$1" want="$2" got="$3"; if [[ "$want" == "$got" ]]; then ok; else bad "$what: ждали [$want], получили [$got]"; fi; }

SECRET='Zk3_q9-RobotSecret-0123456789abcdef'
PROXY=teleport.example.org:443
# The arguments of the firstboot line of Stvor (render_firstboot_line), as is.
STVOR_ARGS=(TELEPORT_PROXY="$PROXY" TELEPORT_JOIN_METHOD=bound_keypair TELEPORT_TOKEN=robot-lab-1
            TELEPORT_REGISTRATION_SECRET=- TELEPORT_ENV=dev TELEPORT_NODENAME=lab-1
            TELEPORT_LABELS=space=sp-0123abcd)

# ---------------------------------------------------------------------------
# Scratch root and fakes
# ---------------------------------------------------------------------------
# FAKE: switches and logs of the fakes.
#   net          curl: a code (6 DNS, 7 connect, 28 timeout, 60 TLS), "up" or
#                "html" (200 without server_version: a captive portal),
#                "body" (200 with the answer from $FAKE/body)
#   agent        what `systemctl start teleport.service` does:
#                  join   — the agent registers (host_uuid appears)
#                  refuse — the agent logs a refusal (journal.refuse)
#                  hang   — the agent logs a network error and keeps trying
#                  fail   — the start fails
#   active       exists — teleport.service is active
#   journal      what journalctl prints
#   systemctl.log  every systemctl call
new_root(){
    ROOT="$(mktemp -d "$TMP/root.XXXXXX")"
    FAKE="$ROOT/fake"
    mkdir -p "$ROOT/bin" "$FAKE"
    export BISQUITE_TELEPORT_ROOT="$ROOT" BISQUITE_CONF_ROOT="$ROOT" FAKE
    export BISQUITE_TELEPORT_SYSTEMD=1 BISQUITE_TELEPORT_JOIN_TIMEOUT=2
    export PATH="$ROOT/bin:$BASE_PATH"
    mkdir -p "$ROOT/usr/lib"
    cp -aL "$SRC" "$ROOT/usr/lib/bisquite-teleport"
    STAGED="$ROOT/usr/lib/bisquite-teleport"
    export TELEPORT_BIN="$ROOT/bin/teleport"
    bash -c 'set -euo pipefail; source "$1/lib/bisquite-conf"; conf_init teleport "$1/knobs"' _ "$STAGED"
    printf '#!/bin/sh\necho "Teleport v18.10.0 git: go1.25"\n' > "$ROOT/bin/teleport"
    chmod +x "$ROOT/bin/teleport"
    echo 7 > "$FAKE/net"
    echo join > "$FAKE/agent"
    cat > "$ROOT/bin/curl" <<'EOF'
#!/usr/bin/env bash
net="$(cat "$FAKE/net")"
if [[ "$net" == up ]]; then echo '{"server_version":"18.10.0"}'; exit 0; fi
if [[ "$net" == html ]]; then echo '<html>captive portal</html>'; exit 0; fi
if [[ "$net" == body ]]; then cat "$FAKE/body"; exit 0; fi
exit "$net"
EOF
    cat > "$ROOT/bin/journalctl" <<'EOF'
#!/usr/bin/env bash
echo "$*" >> "$FAKE/journalctl.log"
cat "$FAKE/journal" 2>/dev/null
exit 0
EOF
    cat > "$ROOT/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
echo "$*" >> "$FAKE/systemctl.log"
args=" $* "
case "${1:-}" in
    is-active)
        [[ "${2:-}" == --quiet ]] && shift
        if [[ "${2:-}" == teleport.service ]]; then [[ -f "$FAKE/active" ]]; exit; fi
        echo active; exit 0 ;;
    is-enabled) echo disabled; exit 1 ;;
    show) exit 0 ;;
    start)
        [[ "$args" == *" teleport.service "* ]] || exit 0
        case "$(cat "$FAKE/agent")" in
            fail) exit 1 ;;
            join) mkdir -p "$BISQUITE_TELEPORT_ROOT/var/lib/teleport"
                  echo 55555555-6666 > "$BISQUITE_TELEPORT_ROOT/var/lib/teleport/host_uuid" ;;
            refuse) cp "$FAKE/journal.refuse" "$FAKE/journal" ;;
            hang) printf '%s\n' 'ERRO Failed to establish connection to cluster. identity:Instance error:[dial tcp 10.0.0.1:443: connect: connection refused]' > "$FAKE/journal" ;;
        esac
        touch "$FAKE/active" ;;
    stop) [[ "$args" == *" teleport.service "* ]] && rm -f "$FAKE/active" ;;
    disable) [[ "$args" == *" --now "* && "$args" == *" teleport.service "* ]] && rm -f "$FAKE/active" ;;
esac
exit 0
EOF
    chmod +x "$ROOT/bin/curl" "$ROOT/bin/journalctl" "$ROOT/bin/systemctl"
    use_root "$ROOT"
}
# Switch to a scratch root made earlier by new_root.
use_root(){
    ROOT="$1"; FAKE="$ROOT/fake"
    STAGED="$ROOT/usr/lib/bisquite-teleport"
    export BISQUITE_TELEPORT_ROOT="$ROOT" BISQUITE_CONF_ROOT="$ROOT" FAKE TELEPORT_BIN="$ROOT/bin/teleport"
    export PATH="$ROOT/bin:$BASE_PATH"
    PENDING="$ROOT/var/lib/bisquite/teleport/join-pending"
    REFUSED="$ROOT/var/lib/bisquite/teleport/join-refused"
    SECRET_FILE="$ROOT/var/lib/bisquite/teleport/registration-secret"
    HOST_UUID="$ROOT/var/lib/teleport/host_uuid"
}
BASE_PATH="$PATH"
tp(){ bash "$STAGED/bisquite-teleport" "$@"; }
# The firstboot line of Stvor: printf without a newline into join.
stvor_line(){ printf '%s' "$SECRET" | tp join "${STVOR_ARGS[@]}" "$@"; }
get(){ bash -c 'set -euo pipefail; source "$1"; shift; "$@"' _ "$REPO/lib/bisquite-conf" conf_get teleport "$1"; }
field(){ sed -n "s/^$1=//p" "$2"; }
slog(){ cat "$FAKE/systemctl.log" 2>/dev/null; }
refusal_journal(){ printf '%s\n' "INFO Starting Teleport" "$1" > "$FAKE/journal.refuse"; }

# ===========================================================================
echo "== нет сети при первой загрузке: строка Stvor выходит с кодом 0 =="
for code in 6 7 28 60; do
    new_root
    echo "$code" > "$FAKE/net"
    msg="$(stvor_line 2>&1)"; rc=$?
    eq "curl $code: код 0" 0 "$rc"
    check "curl $code: файл ожидания есть" test -f "$PENDING"
done
# The last root of the loop is curl 60: a fresh machine's clock may be wrong.
check "curl 60: причина — TLS/часы" grep -q 'TLS' "$PENDING"

new_root
echo 6 > "$FAKE/net"
msg="$(stvor_line 2>&1)"; rc=$?
eq "DNS: код 0" 0 "$rc"
check "DNS: понятное сообщение" grep -q 'сеть или прокси .* недоступны' <<< "$msg"
check "DNS: сообщение называет status" grep -q 'bisquite-teleport status' <<< "$msg"
check_not "секрет не попал в вывод" grep -qF "$SECRET" <<< "$msg"
eq "секрет сохранён" "$SECRET" "$(cat "$SECRET_FILE")"
eq "файл секрета 0600" 600 "$(stat -c %a "$SECRET_FILE")"
eq "файл ожидания 0600" 600 "$(stat -c %a "$PENDING")"
eq "каталог состояния 0700" 700 "$(stat -c %a "$(dirname "$PENDING")")"
eq "config 0600" 600 "$(stat -c %a "$ROOT/etc/bisquite/teleport/config")"
check_not "секрета нет в файле ожидания" grep -qF "$SECRET" "$PENDING"
check_not "секрета нет в config" grep -qF "$SECRET" "$ROOT/etc/bisquite/teleport/config"
eq "параметры: прокси" "$PROXY" "$(get TELEPORT_PROXY)"
eq "параметры: метод" bound_keypair "$(get TELEPORT_JOIN_METHOD)"
eq "параметры: имя токена" robot-lab-1 "$(get TELEPORT_TOKEN)"
eq "параметры: env" dev "$(get TELEPORT_ENV)"
eq "параметры: имя ноды" lab-1 "$(get TELEPORT_NODENAME)"
eq "параметры: метки" space=sp-0123abcd "$(get TELEPORT_LABELS)"
eq "попытка 1" 1 "$(field attempts "$PENDING")"
SINCE1="$(field since "$PENDING")"
check "причина — DNS" grep -q '^error=.*DNS' "$PENDING"
check "teleport.yaml ссылается на секрет" grep -q 'registration_secret_path' "$ROOT/etc/teleport.yaml"
check "юниты повтора включены" grep -qx 'enable bisquite-teleport-join.service bisquite-teleport-join.timer' <<< "$(slog)"
check "таймер запущен без ожидания" grep -qx 'start --no-block bisquite-teleport-join.timer' <<< "$(slog)"
check_not "агент не стартовал" grep -qx 'start teleport.service' <<< "$(slog)"
check_not "агент не включён" grep -qx 'enable teleport.service' <<< "$(slog)"
check_not "rollback убран" test -e "$ROOT/var/lib/bisquite/teleport/rollback"
msg="$(tp status 2>&1)"
check "status: ждёт сети" grep -q 'Регистрация: ждёт сети — попыток 1' <<< "$msg"
check "status: последняя ошибка" grep -q 'последняя попытка: .*DNS' <<< "$msg"
check_not "status не печатает секрет" grep -qF "$SECRET" <<< "$msg"

DNS_ROOT="$ROOT"
new_root
echo html > "$FAKE/net"
msg="$(stvor_line 2>&1)"; rc=$?
eq "ответ не от Teleport: код 0" 0 "$rc"
check "ответ не от Teleport: ждёт" grep -q '^error=.*не от прокси Teleport' "$PENDING"
check_not "ответ не от Teleport: агент не стартовал" grep -qx 'start teleport.service' <<< "$(slog)"

echo "== тик повтора: сети всё ещё нет =="
# Back to the DNS root of above.
use_root "$DNS_ROOT"
: > "$FAKE/systemctl.log"
msg="$(tp join-retry 2>&1)"; rc=$?
eq "тик без сети — код 0" 0 "$rc"
eq "попытка 2" 2 "$(field attempts "$PENDING")"
check "тик пишет номер попытки" grep -q 'попыток: 2' <<< "$msg"
check_not "тик без сети агента не стартует" grep -q 'start teleport.service' <<< "$(slog)"
echo 28 > "$FAKE/net"
tp join-retry 2>/dev/null
eq "попытка 3" 3 "$(field attempts "$PENDING")"
check "последняя ошибка — таймаут" grep -q '^error=.*таймаут' "$PENDING"
eq "since — время первой попытки" "$SINCE1" "$(field since "$PENDING")"
eq "секрет на месте" "$SECRET" "$(cat "$SECRET_FILE")"

echo "== тик повтора: сеть появилась, регистрация =="
echo up > "$FAKE/net"; echo join > "$FAKE/agent"; : > "$FAKE/systemctl.log"
msg="$(tp join-retry 2>&1)"; rc=$?
eq "тик с сетью — код 0" 0 "$rc"
check "зарегистрирован" test -s "$HOST_UUID"
check_not "секрет стёрт" test -e "$SECRET_FILE"
check_not "файл ожидания убран" test -e "$PENDING"
check_not "отказа нет" test -e "$REFUSED"
check_not "teleport.yaml без пути секрета" grep -q 'registration_secret_path' "$ROOT/etc/teleport.yaml"
check "агент стартовал" grep -qx 'start teleport.service' <<< "$(slog)"
check "агент включён" grep -qx 'enable teleport.service' <<< "$(slog)"
check "таймер выключен" grep -qx 'disable --now bisquite-teleport-join.timer' <<< "$(slog)"
check "служба повтора выключена (без --now)" grep -qx 'disable bisquite-teleport-join.service' <<< "$(slog)"
eq "имя токена осталось" robot-lab-1 "$(get TELEPORT_TOKEN)"
check "status: зарегистрирован" grep -q 'Регистрация: есть' <<< "$(tp status 2>&1)"

echo "== идемпотентность =="
: > "$FAKE/systemctl.log"
msg="$(tp join-retry 2>&1)"; rc=$?
eq "тик без ожидания — код 0" 0 "$rc"
check "тик без ожидания — нечего повторять" grep -q 'ждущего подключения нет' <<< "$msg"
check_not "тик без ожидания агента не трогает" grep -q 'teleport.service' <<< "$(slog)"
msg="$(stvor_line 2>&1)"; rc=$?
eq "повтор строки Stvor на зарегистрированном — код 0" 0 "$rc"
check "повтор — уже зарегистрирован" grep -q 'уже зарегистрирован' <<< "$msg"
check_not "повтор не пишет секрет заново" test -e "$SECRET_FILE"
eq "регистрация цела" 55555555-6666 "$(cat "$HOST_UUID")"

echo "== агент зарегистрировался сам, пока join ждал =="
new_root
echo 7 > "$FAKE/net"
stvor_line 2>/dev/null
mkdir -p "$ROOT/var/lib/teleport"; echo 77777777-8888 > "$HOST_UUID"
: > "$FAKE/systemctl.log"
check "scrub-token (path-юнит)" tp scrub-token 2>/dev/null
check_not "scrub-token: секрет стёрт" test -e "$SECRET_FILE"
check_not "scrub-token: ожидание снято" test -e "$PENDING"
check "scrub-token: таймер выключен" grep -qx 'disable --now bisquite-teleport-join.timer' <<< "$(slog)"
new_root
echo 7 > "$FAKE/net"
stvor_line 2>/dev/null
mkdir -p "$ROOT/var/lib/teleport"; echo 77777777-8888 > "$HOST_UUID"
msg="$(tp join-retry 2>&1)"; rc=$?
eq "тик после регистрации — код 0" 0 "$rc"
check_not "тик: секрет стёрт" test -e "$SECRET_FILE"
check_not "тик: ожидание снято" test -e "$PENDING"
check_not "тик не перезапускал агента" grep -qx 'start teleport.service' <<< "$(slog)"

echo "== отказ кластера при первом join: не повторять =="
refusals=(
    'ERRO Failed to establish connection to cluster. identity:Instance error:[a valid registration secret is required]|секрет регистрации неверен или истёк'
    'ERRO Can not join the cluster, the token is expired or not found. Regenerate the token and try again.|токен регистрации не найден'
    'ERRO Failed to establish connection to cluster. error:[access denied: no recovery attempts remaining]|токен уже использован'
    'ERRO Failed to establish connection to cluster. error:[can not join the cluster, the token does not allow role Node]|токен не подходит'
)
for entry in "${refusals[@]}"; do
    new_root
    echo up > "$FAKE/net"; echo refuse > "$FAKE/agent"
    refusal_journal "${entry%|*}"
    : > "$FAKE/systemctl.log"
    msg="$(stvor_line 2>&1)"; rc=$?
    want="${entry##*|}"
    check "отказ «$want»: код ≠ 0" test "$rc" -ne 0
    check "отказ «$want»: причина в выводе" grep -qF "$want" <<< "$msg"
    check "отказ «$want»: подсказка про новый секрет в Stvor" grep -q 'карточке робота Stvor' <<< "$msg"
    check "отказ «$want»: причина записана" grep -qF "reason=$want" "$REFUSED"
    eq "отказ «$want»: файл отказа 0600" 600 "$(stat -c %a "$REFUSED")"
    check_not "отказ «$want»: ожидания нет" test -e "$PENDING"
    check_not "отказ «$want»: секрет стёрт" test -e "$SECRET_FILE"
    check_not "отказ «$want»: повторы не включены" grep -q 'enable bisquite-teleport-join' <<< "$(slog)"
    check "отказ «$want»: агент выключен" grep -qx 'disable --now teleport.service' <<< "$(slog)"
    check_not "отказ «$want»: агент не активен" test -e "$FAKE/active"
done
msg="$(tp status 2>&1)"
check "status: отказ и причина" grep -q 'Регистрация: отказ кластера — токен не подходит' <<< "$msg"
check "status: ответ агента" grep -q 'ответ агента: .*does not allow role' <<< "$msg"
check "status: что делать" grep -q 'карточке робота Stvor' <<< "$msg"

echo "== сеть, потом отказ: тик не повторяет =="
new_root
echo 6 > "$FAKE/net"
stvor_line 2>/dev/null
echo up > "$FAKE/net"; echo refuse > "$FAKE/agent"
refusal_journal 'ERRO Failed to establish connection to cluster. error:[a valid registration secret is required]'
: > "$FAKE/systemctl.log"
msg="$(tp join-retry 2>&1)"; rc=$?
check "тик с отказом — код ≠ 0" test "$rc" -ne 0
check "тик: причина записана" grep -q 'reason=секрет регистрации неверен или истёк' "$REFUSED"
check_not "тик: ожидание снято" test -e "$PENDING"
check_not "тик: секрет стёрт" test -e "$SECRET_FILE"
check "тик: таймер выключен" grep -qx 'disable --now bisquite-teleport-join.timer' <<< "$(slog)"
check "тик: агент выключен" grep -qx 'disable --now teleport.service' <<< "$(slog)"

echo "== прокси отвечает, регистрация не подтвердилась, отказа нет: ждать =="
new_root
echo up > "$FAKE/net"; echo hang > "$FAKE/agent"
msg="$(stvor_line 2>&1)"; rc=$?
eq "без подтверждения — код 0" 0 "$rc"
check "без подтверждения — ожидание" test -f "$PENDING"
check "ошибка агента записана" grep -q '^error=прокси отвечает, регистрация не подтвердилась.*connection refused' "$PENDING"
eq "без подтверждения — секрет на месте" "$SECRET" "$(cat "$SECRET_FILE")"
check "без подтверждения — повторы включены" grep -qx 'enable bisquite-teleport-join.service bisquite-teleport-join.timer' <<< "$(slog)"
check "агент оставлен работать" test -e "$FAKE/active"
: > "$FAKE/systemctl.log"
tp join-retry 2>/dev/null
check_not "тик не стартует работающий агент заново" grep -qx 'start teleport.service' <<< "$(slog)"
eq "тик: попытка 2" 2 "$(field attempts "$PENDING")"
# The agent registers on its own between the ticks.
mkdir -p "$ROOT/var/lib/teleport"; echo 99999999-0000 > "$HOST_UUID"
tp join-retry 2>/dev/null
check_not "после регистрации ожидание снято" test -e "$PENDING"
check_not "после регистрации секрет стёрт" test -e "$SECRET_FILE"

echo "== агент не стартовал: ждать, не отказ =="
new_root
echo up > "$FAKE/net"; echo fail > "$FAKE/agent"
msg="$(stvor_line 2>&1)"; rc=$?
eq "старт не удался — код 0" 0 "$rc"
check "старт не удался — ожидание" grep -q 'teleport.service не стартовал' "$PENDING"
check_not "старт не удался — не отказ" test -e "$REFUSED"

echo "== новый join снимает отказ =="
new_root
echo up > "$FAKE/net"; echo refuse > "$FAKE/agent"
refusal_journal 'ERRO Failed to establish connection to cluster. error:[a valid registration secret is required]'
stvor_line 2>/dev/null
check "отказ записан" test -f "$REFUSED"
rm -f "$FAKE/journal"; echo join > "$FAKE/agent"
msg="$(stvor_line 2>&1)"; rc=$?
eq "join с новым секретом — код 0" 0 "$rc"
check_not "отказ снят" test -e "$REFUSED"
check "зарегистрирован" test -s "$HOST_UUID"
check_not "секрет стёрт" test -e "$SECRET_FILE"

echo "== тик без секрета и без кластера =="
new_root
echo 7 > "$FAKE/net"
stvor_line 2>/dev/null
rm -f "$SECRET_FILE"
msg="$(tp join-retry 2>&1)"; rc=$?
check "тик без секрета — код ≠ 0" test "$rc" -ne 0
check "тик без секрета — отказ записан" grep -q 'reason=секрета регистрации на машине нет' "$REFUSED"
check_not "тик без секрета — ожидание снято" test -e "$PENDING"

echo "== блокировка: join и тик не идут вместе =="
new_root
echo 7 > "$FAKE/net"
stvor_line 2>/dev/null
mkdir -p "$ROOT/var/lib/bisquite/teleport"
exec 7> "$ROOT/var/lib/bisquite/teleport/.join.lock"
flock -n 7
msg="$(tp join-retry 2>&1)"; rc=$?
eq "тик под чужой блокировкой — код 0" 0 "$rc"
check "тик под чужой блокировкой — пропуск" grep -q 'идёт другой join' <<< "$msg"
eq "тик под чужой блокировкой — попытка не засчитана" 1 "$(field attempts "$PENDING")"
msg="$(BISQUITE_TELEPORT_JOIN_TIMEOUT=-59 stvor_line 2>&1)"; rc=$?
check "join под чужой блокировкой — отказ" test "$rc" -ne 0
check "join под чужой блокировкой — текст" grep -q 'идёт другой join' <<< "$msg"
exec 7>&-

echo "== leave снимает ожидание =="
: > "$FAKE/systemctl.log"
check "leave" tp leave 2>/dev/null
check_not "leave: ожидание снято" test -e "$PENDING"
check_not "leave: секрет стёрт" test -e "$SECRET_FILE"
check "leave: таймер повтора выключен" grep -qx 'disable --now bisquite-teleport-join.timer' <<< "$(slog)"
check "status после leave: нет" grep -q 'Регистрация: нет' <<< "$(tp status 2>&1)"

echo "== без systemd (сборка образа): как раньше =="
new_root
export BISQUITE_TELEPORT_SYSTEMD=0
echo 7 > "$FAKE/net"
msg="$(stvor_line 2>&1)"; rc=$?
eq "без systemd — код 0" 0 "$rc"
check "без systemd — агент стартует при загрузке" grep -q 'systemd не запущен' <<< "$msg"
check_not "без systemd — ожидания нет" test -e "$PENDING"
check_not "без systemd — systemctl не зовётся" test -s "$FAKE/systemctl.log"
export BISQUITE_TELEPORT_SYSTEMD=1

echo "== зарегистрированная машина: недоступный прокси — отказ, как раньше =="
new_root
echo up > "$FAKE/net"; echo join > "$FAKE/agent"
stvor_line 2>/dev/null
echo 7 > "$FAKE/net"
msg="$(stvor_line TELEPORT_FORCE=1 2>&1)"; rc=$?
check "FORCE без сети на зарегистрированной — код ≠ 0" test "$rc" -ne 0
check "FORCE без сети — регистрацию не трогает" grep -q 'рабочую регистрацию не трогаю' <<< "$msg"
check_not "FORCE без сети — ожидания нет" test -e "$PENDING"
eq "FORCE без сети — регистрация цела" 55555555-6666 "$(cat "$HOST_UUID")"

echo "== версия кластера из сети не исполняется (арифметика bash) =="
# (( )) evaluates a[$(cmd)] in its operand: a server_version like that would
# run a command as root. The version is checked as X.Y.Z before any use.
evil_versions=('a[$(touch '"$TMP"'/pwn-a)]' '1.2.3$(touch '"$TMP"'/pwn-b)' '18.a[$(touch '"$TMP"'/pwn-c)].0' 'x[$(touch '"$TMP"'/pwn-d)]+1')
for v in "${evil_versions[@]}"; do
    new_root
    printf '{"server_version":"%s"}\n' "$v" > "$FAKE/body"
    echo body > "$FAKE/net"
    msg="$(stvor_line 2>&1)"; rc=$?
    eq "join, версия [$v]: код 0 (ждём)" 0 "$rc"
    check "join, версия [$v]: файл ожидания есть" test -f "$PENDING"
    check "join, версия [$v]: причина — версия не по форме" grep -q 'не по форме' "$PENDING"
    check_not "join, версия [$v]: агент не стартовал" grep -q '^start teleport.service' <<< "$(slog)"
    msg="$(tp join-retry 2>&1)"; rc=$?
    eq "тик, версия [$v]: код 0" 0 "$rc"
    check "тик, версия [$v]: попытка засчитана" test "$(field attempts "$PENDING")" -ge 2
done
for f in pwn-a pwn-b pwn-c pwn-d; do
    check_not "версия из сети не выполнила команду ($f)" test -e "$TMP/$f"
done
# A pre-release or build suffix is a valid version: only X.Y.Z goes on.
new_root
printf '{"server_version":"18.10.0-dev.1+abc"}\n' > "$FAKE/body"
echo body > "$FAKE/net"
msg="$(stvor_line 2>&1)"; rc=$?
eq "версия 18.10.0-dev.1+abc — регистрация" 0 "$rc"
check "версия 18.10.0-dev.1+abc — кластер 18.10.0" grep -q 'кластер 18\.10\.0,' <<< "$msg"
# Leading zeros are base 10, not octal.
new_root
printf '{"server_version":"18.010.08"}\n' > "$FAKE/body"
echo body > "$FAKE/net"
msg="$(stvor_line 2>&1)"; rc=$?
eq "версия 18.010.08 — регистрация (база 10)" 0 "$rc"

echo "== числовые переменные окружения проверяются до арифметики =="
for var in BISQUITE_TELEPORT_JOIN_TIMEOUT BISQUITE_TELEPORT_RESTART_TIMEOUT BISQUITE_TELEPORT_NOW; do
    new_root
    echo 7 > "$FAKE/net"
    msg="$(env "$var=a[\$(touch $TMP/pwn-$var)]" bash "$STAGED/bisquite-teleport" join "${STVOR_ARGS[@]}" <<< "$SECRET" 2>&1)"; rc=$?
    check "$var с подстановкой — отказ" test "$rc" -ne 0
    check "$var с подстановкой — текст называет переменную" grep -q "$var" <<< "$msg"
    check_not "$var с подстановкой — команда не выполнена" test -e "$TMP/pwn-$var"
    msg="$(env "$var=12x" bash "$STAGED/bisquite-teleport" status 2>&1)"; rc=$?
    check "$var=12x — отказ" test "$rc" -ne 0
done
# The lock wait of join is JOIN_TIMEOUT + 60: the old tests pass -59.
new_root
echo 7 > "$FAKE/net"
msg="$(BISQUITE_TELEPORT_JOIN_TIMEOUT=-59 stvor_line 2>&1)"; rc=$?
eq "BISQUITE_TELEPORT_JOIN_TIMEOUT=-59 — принят" 0 "$rc"

echo "== юниты повтора =="
U="$SRC/systemd"
check "служба: после network-online" grep -qx 'After=network-online.target' "$U/bisquite-teleport-join.service"
check "служба: Wants network-online" grep -qx 'Wants=network-online.target' "$U/bisquite-teleport-join.service"
check "служба: условие — файл ожидания" grep -qx 'ConditionPathExists=/var/lib/bisquite/teleport/join-pending' "$U/bisquite-teleport-join.service"
check "служба: зовёт join-retry" grep -qx 'ExecStart=/usr/sbin/bisquite-teleport join-retry' "$U/bisquite-teleport-join.service"
check "служба: при каждой загрузке" grep -qx 'WantedBy=multi-user.target' "$U/bisquite-teleport-join.service"
check "таймер: раз в минуту" grep -qx 'OnCalendar=minutely' "$U/bisquite-teleport-join.timer"
check "путь файла ожидания — тот же, что в CLI" grep -q 'JOIN_PENDING="$STATE/join-pending"' "$SRC/bisquite-teleport"
if command -v systemd-analyze >/dev/null 2>&1; then
    check "systemd-analyze verify юнитов повтора" bash -c '
        d="$(mktemp -d)"; cp "$1"/bisquite-teleport-join.* "$d/"
        sed -i "s|/usr/sbin/bisquite-teleport|/bin/true|" "$d/bisquite-teleport-join.service"
        out="$(cd "$d" && systemd-analyze verify --man=no ./bisquite-teleport-join.service ./bisquite-teleport-join.timer 2>&1)"
        rc=$?; rm -r -f -- "$d"; [[ $rc -eq 0 ]] || { echo "$out" >&2; exit 1; }' _ "$U"
fi

echo
echo "проверок: $((pass + fail)), не прошло: $fail"
(( fail == 0 ))
