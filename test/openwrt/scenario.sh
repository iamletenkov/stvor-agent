#!/bin/sh
# Scenario of make e2e-openwrt inside the container with the OpenWrt 25.12 root
# FS (busybox sh; procd is not PID 1). /t (read-only): the release tar.gz of
# the agent, fakeportal, fakeubus (stands in for /bin/ubus), cream (the fake
# teleport-openwrt). Environment: AGENT_VERSION, AGENT_TARBALL.
#
# One line per check: "ok" or "FAIL"; FATAL stops. Exit 0 — no FAIL.
set -u

PASS=0 FAILS=0
PORTAL=/opt/fakeportal
URL=https://portal.e2e/robot-api/v1/labels
# Test-only secret of a throwaway container: the labels token (43 characters).
LABEL_TOKEN=e2eLabelToken_0123456789abcdefghijklmnopqrs
FAKEUBUS=/tmp/fakeubus
CREAM_DIR=/opt/fakecream

log() { echo "== $*"; }
check() { # check <id> <description> <command…>
	id=$1 what=$2
	shift 2
	if "$@"; then
		echo "ok    $id: $what"
		PASS=$((PASS + 1))
	else
		echo "FAIL  $id: $what"
		FAILS=$((FAILS + 1))
	fi
}
diag() {
	echo "--- диагностика"
	for f in "$FAKEUBUS"/*.log "$CREAM_DIR/calls.log" "$PORTAL/fakeportal.log" "$PORTAL/requests.log"; do
		[ -f "$f" ] && { echo "--- $f"; tail -n 30 "$f"; }
	done
	for f in "$FAKEUBUS"/*/*.json; do
		[ -f "$f" ] && { echo "--- $f"; cat "$f"; echo; }
	done
}
summary() { echo "итог: ok $PASS, FAIL $FAILS"; }
fatal() {
	echo "FATAL $*"
	diag
	summary
	exit 1
}
wait_for() { # wait_for <seconds> <command…>
	n=$1
	shift
	i=0
	while [ "$i" -lt "$n" ]; do
		"$@" && return 0
		sleep 1
		i=$((i + 1))
	done
	"$@"
}
eq() { [ "$1" = "$2" ]; }
not() { ! "$@"; }
# is <expected> <command…>: the output of the command (run on every poll).
is() {
	want=$1
	shift
	[ "$("$@" 2>/dev/null)" = "$want" ]
}
absent() {
	for p; do [ ! -e "$p" ] || return 1; done
}
# perm <path>: "<mode string> <uid> <gid>" (busybox has no stat on OpenWrt).
# shellcheck disable=SC2012 # numeric owner and mode string; names are ours
perm() { ls -lnd "$1" | awk '{print $1, $3, $4}'; }
called() { grep -qxF "$1" "$CREAM_DIR/calls.log" 2>/dev/null; }
instance() { cat "$FAKEUBUS/$1/instance1.json" 2>/dev/null; }
field() { instance "$1" | jsonfilter -e "@.$2"; }
running() { /etc/init.d/"$1" running; }
uid_of() { awk -F: -v u="$1" '$1 == u {print $3}' /etc/passwd; }
gid_of() { awk -F: -v g="$1" '$1 == g {print $3}' /etc/group; }
proc_uid() { awk '/^Uid:/ {print $2}' "/proc/$1/status"; }
tick() { kill -HUP "$(field stvor-agent pid)"; }

# --- 0. the root FS: OpenWrt, /var on tmpfs, /run made at boot -------------
# shellcheck source=/dev/null
. /etc/os-release
log "окружение: $PRETTY_NAME, pid1=$(cat /proc/1/comm)"
[ "$ID" = openwrt ] || fatal "не OpenWrt: $ID"
[ "$(readlink /var)" = tmp ] || fatal "/var не ссылка на tmp"
grep -q ' /tmp tmpfs ' /proc/mounts || fatal "/tmp не tmpfs"
# /run is not in the image: /etc/init.d/boot (START=10, before the agent's
# START=99) makes /var/run and links /run to it at every boot, so the lock
# /run/stvor-agent.lock and /run/stvor-agent-telemetry live on tmpfs. boot does
# not run here: its lines the agent relies on are run as they are written there.
check run-absent "в образе нет /run (его создаёт boot)" not test -e /run
for l in '	mkdir -p /var/lock' '	mkdir -p /var/run' '	ln -s /var/run /run'; do
	grep -qxF "$l" /etc/init.d/boot || fatal "в /etc/init.d/boot нет строки «$l»"
	eval "$l"
done
check run-link "/run — ссылка на /var/run" eq "$(readlink /run)" /var/run
check run-tmpfs "/run лежит на tmpfs (/tmp/run)" eq "$(readlink -f /run)" /tmp/run

# procd is not PID 1: its ubus calls go to the fake.
cp /t/fakeubus /bin/ubus
chmod 0755 /bin/ubus

# --- 1. install from the release tar.gz (busybox tar) ----------------------
log "установка из $(basename "$AGENT_TARBALL")"
mkdir -p /opt/sa-dist
tar -xzf "$AGENT_TARBALL" -C /opt/sa-dist || fatal "tar -xzf"
want="LICENSE NOTICE init.d/ init.d/stvor-agent init.d/stvor-agent-telemetry stvor-agent stvor-agent.8.gz"
got=$(tar -tzf "$AGENT_TARBALL" | sort | tr '\n' ' ' | sed 's/ $//')
check tar-list "состав tar.gz: $want" eq "$got" "$want"
cp /opt/sa-dist/stvor-agent /usr/bin/stvor-agent
cp /opt/sa-dist/init.d/stvor-agent /opt/sa-dist/init.d/stvor-agent-telemetry /etc/init.d/
chmod 0755 /usr/bin/stvor-agent /etc/init.d/stvor-agent /etc/init.d/stvor-agent-telemetry
check tar-modes "бинарь и init-скрипты исполняемые в архиве" \
	eq "$(perm /opt/sa-dist/stvor-agent | cut -c1-10) $(perm /opt/sa-dist/init.d/stvor-agent | cut -c1-10)" "-rwxr-xr-x -rwxr-xr-x"
# As postinst of the .deb enables both units: the installer (the extension
# stvor-agent-openwrt) enables the telemetry script once; without its token it
# starts nothing, the agent starts and stops it, `disable` keeps the link.
/etc/init.d/stvor-agent-telemetry enable
check t-rc-d "rc.d: S99stvor-agent-telemetry после установки" \
	eq "$(readlink /etc/rc.d/S99stvor-agent-telemetry)" ../init.d/stvor-agent-telemetry
check t-off "без токена телеметрия не запускается" not running stvor-agent-telemetry
check version "stvor-agent version --short = $AGENT_VERSION (статический бинарь на musl)" \
	eq "$(stvor-agent version --short)" "$AGENT_VERSION"

log "подмены: cream (teleport-openwrt), портал"
cp /t/cream /usr/bin/cream
chmod 0755 /usr/bin/cream
echo "127.0.0.1 portal.e2e" >>/etc/hosts
mkdir -p "$PORTAL"
chmod 0700 "$PORTAL"
printf '%s\n' "$LABEL_TOKEN" >"$PORTAL/token"
printf '{"env":"e2e","space":"sp-a"}\n' >"$PORTAL/labels.json"
/t/fakeportal -dir "$PORTAL" >"$PORTAL/fakeportal.log" 2>&1 &
wait_for 20 test -s "$PORTAL/ca.pem" || fatal "портал не записал ca.pem"
# Go reads /etc/ssl/certs; ca-bundle of OpenWrt keeps the roots there.
cp "$PORTAL/ca.pem" /etc/ssl/certs/fakeportal.pem
[ -f /etc/ssl/certs/ca-certificates.crt ] && cat "$PORTAL/ca.pem" >>/etc/ssl/certs/ca-certificates.crt

# --- 2. before enable: no token — the scripts start nothing ----------------
check off-start "без токена start ничего не запускает" /etc/init.d/stvor-agent start
check off-running "без токена running — не 0" not running stvor-agent

# --- 3. enable: the token on stdin, procd scripts enabled and started ------
log "stvor-agent enable (токен — stdin)"
printf '%s' "$LABEL_TOKEN" | stvor-agent enable "URL=$URL"
check enable-code "enable — код 0" eq "$?" 0
check enable-rc-d "rc.d: S99stvor-agent и K01stvor-agent" \
	eq "$(readlink /etc/rc.d/S99stvor-agent) $(readlink /etc/rc.d/K01stvor-agent)" "../init.d/stvor-agent ../init.d/stvor-agent"
check enable-running "/etc/init.d/stvor-agent running — 0" running stvor-agent
check enable-root "служба агента — root (без user в procd)" eq "$(field stvor-agent user)" ""
check enable-nnp "procd: no_new_privs, term_timeout 120" \
	eq "$(field stvor-agent no_new_privs) $(field stvor-agent term_timeout)" "true 120"
check enable-cmd "procd: command /usr/bin/stvor-agent run" \
	eq "$(instance stvor-agent | jsonfilter -e '@.command[*]' | tr '\n' ' ')" "/usr/bin/stvor-agent run "
check token-mode "файл токена 0600 root" eq "$(perm /etc/stvor-agent/token)" "-rw------- 0 0"
check lock "/run/stvor-agent.lock" test -f /run/stvor-agent.lock
check set-managed "cream run teleport-openwrt set-managed env=e2e space=sp-a" \
	wait_for 30 called "run --lock-timeout 20s teleport-openwrt set-managed env=e2e space=sp-a"
check no-debian-ext "расширение Debian teleport не вызывалось" not grep -q ' teleport ' "$CREAM_DIR/calls.log"
check state-opt "state.json в /opt/stvor-agent" wait_for 30 test -s /opt/stvor-agent/state.json
check state-not-var "/var/lib/stvor-agent не создан" not test -e /var/lib/stvor-agent
tgid=$(gid_of stvor-telemetry)
check tuser "пользователь stvor-telemetry создан init-скриптом (без входа)" \
	eq "$(awk -F: '$1 == "stvor-telemetry" {print $7}' /etc/passwd)" /bin/false
check state-dir "/opt/stvor-agent 0750 root:stvor-telemetry" eq "$(perm /opt/stvor-agent)" "drwxr-x--- 0 $tgid"
check machine-id "machine-id: 32 hex, 0640 root:stvor-telemetry" \
	eq "$(grep -cE '^[0-9a-f]{32}$' /opt/stvor-agent/machine-id) $(perm /opt/stvor-agent/machine-id)" "1 -rw-r----- 0 $tgid"
check status "stvor-agent status — код 0" stvor-agent status

# --- 4. telemetry: token from the portal, the service as stvor-telemetry ---
log "телеметрия"
check ttoken "токен телеметрии 0640 root:stvor-telemetry" \
	wait_for 60 is "-rw-r----- 0 $tgid" perm /etc/stvor-agent-telemetry/token
check t-running "/etc/init.d/stvor-agent-telemetry running — 0" wait_for 30 running stvor-agent-telemetry
check t-procd "procd: user/group stvor-telemetry, no_new_privs, term_timeout 30, GOMEMLIMIT" \
	eq "$(field stvor-agent-telemetry user) $(field stvor-agent-telemetry group) $(field stvor-agent-telemetry no_new_privs) $(field stvor-agent-telemetry term_timeout) $(field stvor-agent-telemetry env.GOMEMLIMIT)" \
	"stvor-telemetry stvor-telemetry true 30 40MiB"
tpid=$(field stvor-agent-telemetry pid)
check t-uid "процесс телеметрии под uid stvor-telemetry" eq "$(proc_uid "$tpid")" "$(uid_of stvor-telemetry)"
mid_hash=$(printf '%s' "$(cat /opt/stvor-agent/machine-id)" | sha256sum | cut -d' ' -f1)
check t-sent "метрики дошли до портала (200) с хешем своего machine-id" \
	wait_for 150 grep -q "\"path\":\"/robot-api/v1/metrics\",\"status\":200.*\"machine_id_hash\":\"$mid_hash\"" "$PORTAL/telemetry.log"
check t-state "буфер телеметрии в /opt/stvor-agent/telemetry (владелец stvor-telemetry)" \
	eq "$(perm /opt/stvor-agent/telemetry)" "drwx------ $(uid_of stvor-telemetry) $tgid"
check t-status "статус телеметрии в /run/stvor-agent-telemetry" wait_for 30 test -s /run/stvor-agent-telemetry/status.json
check t-not-var "/var/lib/stvor-agent-telemetry не создан" not test -e /var/lib/stvor-agent-telemetry

# --- 5. the portal moves the robot: the loop applies the new space ---------
log "портал переносит робота в sp-b"
printf '{"env":"e2e","space":"sp-b"}\n' >"$PORTAL/labels.json"
tick
check move "set-managed env=e2e space=sp-b" \
	wait_for 30 called "run --lock-timeout 20s teleport-openwrt set-managed env=e2e space=sp-b"

# --- 6. restart (as after a reboot): the state and the machine-id stay -----
log "restart служб"
mid=$(cat /opt/stvor-agent/machine-id)
/etc/init.d/stvor-agent restart
check restart-running "после restart агент работает" wait_for 10 running stvor-agent
check restart-mid "machine-id не изменился" eq "$(cat /opt/stvor-agent/machine-id)" "$mid"

# --- 7. disable: both services stopped and disabled, tokens gone -----------
log "stvor-agent disable"
stvor-agent disable
check disable-code "disable — код 0" eq "$?" 0
check disable-stopped "агент остановлен" not running stvor-agent
check disable-t-stopped "телеметрия остановлена" not running stvor-agent-telemetry
check disable-rc-d "ссылок rc.d агента нет" absent /etc/rc.d/S99stvor-agent /etc/rc.d/K01stvor-agent
check disable-t-rc-d "ссылка телеметрии осталась (как юнит на Debian): без токена она не стартует" \
	test -L /etc/rc.d/S99stvor-agent-telemetry
check disable-tokens "токенов нет" absent /etc/stvor-agent/token /etc/stvor-agent-telemetry/token

summary
[ "$FAILS" -eq 0 ] || { diag; exit 1; }
