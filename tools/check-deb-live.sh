#!/bin/sh
# Live dpkg run of one stvor-agent .deb (spec §9.1): install, remove and purge
# in a throwaway debian container as root (make deb). systemctl is a fake that
# logs its arguments; «systemd is running» is /run/systemd/system existing,
# exactly what the maintainer scripts test. A foreign ARCH is added as a dpkg
# architecture: the scripts never run the binary, so it installs on any host.
#   tools/check-deb-live.sh VERSION ARCH DIR
set -eu

version="$1" arch="$2" dir="$3"
deb="$dir/stvor-agent_${version}_${arch}.deb"
unit=stvor-agent.service
tunit=stvor-agent-telemetry.service
log=/tmp/systemctl.log
fail=0
bad() { echo "check-deb-live: $*" >&2; fail=1; }
[ "$(id -u)" = 0 ] || { echo "check-deb-live: нужен root (контейнер)" >&2; exit 1; }
[ -f "$deb" ] || { echo "check-deb-live: нет $deb" >&2; exit 1; }
[ ! -e /usr/bin/systemctl ] || { echo "check-deb-live: systemctl уже есть — не тот образ" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
if [ "$arch" != "$(dpkg --print-architecture)" ]; then
	dpkg --add-architecture "$arch"
fi

# expect_log WHAT LINE… — the fake systemctl got exactly these calls; log reset.
expect_log() {
	what="$1"
	shift
	: >"$work/want"
	for l in "$@"; do printf '%s\n' "$l" >>"$work/want"; done
	touch "$log"
	if ! cmp -s "$work/want" "$log"; then
		bad "$what: вызовы systemctl (ожидание <, факт >):"
		diff "$work/want" "$log" >&2 || true
	fi
	: >"$log"
}
dpkg_q() { dpkg "$@" >"$work/dpkg.out" 2>&1 || { cat "$work/dpkg.out" >&2; return 1; }; }
fake_systemctl() {
	printf '#!/bin/sh\nprintf "%%s\\n" "$*" >>%s\n' "$log" >/usr/bin/systemctl
	chmod 0755 /usr/bin/systemctl
}

# Depends: without cream the package does not configure.
if dpkg -i "$deb" >"$work/dpkg.out" 2>&1; then
	bad "без cream пакет установился — Depends не действует"
fi
grep -q 'depends on cream' "$work/dpkg.out" || bad "без cream: нет отказа по зависимости: $(cat "$work/dpkg.out")"
dpkg_q --purge stvor-agent || bad "purge неустановленного пакета упал"

# A stand-in cream 1.0.0 of the same architecture to satisfy Depends (as on a real machine).
mkdir -p "$work/cream/DEBIAN"
printf '%s\n' "Package: cream" "Version: 1.0.0" "Architecture: $arch" \
	"Maintainer: test <test@example.invalid>" "Description: stand-in for the check" \
	>"$work/cream/DEBIAN/control"
dpkg-deb --root-owner-group -b "$work/cream" "$work/cream.deb" >/dev/null
dpkg_q -i "$work/cream.deb"

echo "== без systemctl (образ без systemd)"
dpkg_q -i "$deb" || bad "установка без systemctl упала"
[ "$(stat -c '%a %U:%G' /usr/bin/stvor-agent)" = "755 root:root" ] ||
	bad "/usr/bin/stvor-agent: $(stat -c '%a %U:%G' /usr/bin/stvor-agent)"
[ "$(stat -c '%a' /usr/lib/systemd/system/$unit)" = 644 ] || bad "юнит не 0644"
[ ! -e /etc/stvor-agent ] || bad "пакет создал /etc/stvor-agent (это дело enable)"
# Telemetry (Stvor plan Task 18): the user from sysusers.d (groupadd/useradd without
# systemd-sysusers) and the token directory 0750 root:stvor-telemetry.
getent passwd stvor-telemetry | grep -q ':/usr/sbin/nologin$' || bad "нет пользователя stvor-telemetry с nologin"
[ "$(stat -c '%a %U:%G' /etc/stvor-agent-telemetry)" = "750 root:stvor-telemetry" ] ||
	bad "/etc/stvor-agent-telemetry: $(stat -c '%a %U:%G' /etc/stvor-agent-telemetry)"
[ "$(stat -c '%a' /usr/lib/systemd/system/$tunit)" = 644 ] || bad "юнит телеметрии не 0644"
dpkg_q -r stvor-agent || bad "remove без systemctl упал"
dpkg_q --purge stvor-agent || bad "purge без systemctl упал"

fake_systemctl
echo "== systemctl есть, systemd не запущен (сборка образа)"
dpkg_q -i "$deb"
expect_log "установка без systemd" "enable $unit $tunit"
install -d -m 0700 /etc/stvor-agent /var/lib/stvor-agent
: >/etc/stvor-agent/token
: >/var/lib/stvor-agent/state.json
dpkg_q -r stvor-agent
expect_log "remove без systemd" "disable $unit $tunit"
[ ! -e /usr/bin/stvor-agent ] || bad "remove оставил /usr/bin/stvor-agent"
[ -e /etc/stvor-agent/token ] || bad "remove удалил токен (это делает только purge)"
[ -e /var/lib/stvor-agent/state.json ] || bad "remove удалил состояние (это делает только purge)"
dpkg_q --purge stvor-agent
expect_log "purge без systemd"
[ ! -e /etc/stvor-agent ] || bad "purge оставил /etc/stvor-agent"
[ ! -e /var/lib/stvor-agent ] || bad "purge оставил /var/lib/stvor-agent"
[ ! -e /etc/stvor-agent-telemetry ] || bad "purge оставил /etc/stvor-agent-telemetry"
getent passwd stvor-telemetry >/dev/null || bad "purge удалил пользователя stvor-telemetry"

mkdir -p /run/systemd/system
echo "== systemd запущен"
dpkg_q -i "$deb"
expect_log "установка без токена" "daemon-reload" "enable $unit $tunit"
install -d -m 0700 /etc/stvor-agent
: >/etc/stvor-agent/token
: >/etc/stvor-agent-telemetry/token
dpkg_q -i "$deb"
# Upgrade from a version without the telemetry unit (< 4.1.0) enables it once; a
# dev build (0.0.0-dev) is such a version both ways: prerm first disables it.
if dpkg --compare-versions "$version" lt 4.1.0~; then
	expect_log "обновление с токенами" "disable --now $tunit" "daemon-reload" "enable $tunit" \
		"try-restart $unit" "try-restart $tunit"
else
	expect_log "обновление с токенами" "daemon-reload" "try-restart $unit" "try-restart $tunit"
fi
# Downgrade to a version without the telemetry unit (< 4.1.0): prerm disables and
# stops it — the old package has no such unit, its link would dangle. A stand-in
# 4.0.0 without maintainer scripts and files: only the new prerm/postrm run.
mkdir -p "$work/old/DEBIAN"
printf '%s\n' "Package: stvor-agent" "Version: 4.0.0" "Architecture: $arch" "Depends: cream" \
	"Maintainer: test <test@example.invalid>" "Description: stand-in for the downgrade check" \
	>"$work/old/DEBIAN/control"
dpkg-deb --root-owner-group -b "$work/old" "$work/old.deb" >/dev/null
dpkg_q -i "$work/old.deb" || bad "откат на 4.0.0 упал"
expect_log "откат на 4.0.0" "disable --now $tunit"
dpkg_q -i "$deb"
: >"$log"
dpkg_q -r stvor-agent
expect_log "remove" "disable --now $unit $tunit" "daemon-reload"
[ -e /etc/stvor-agent/token ] || bad "remove удалил токен"
dpkg_q --purge stvor-agent
expect_log "purge" "daemon-reload"
[ ! -e /etc/stvor-agent ] || bad "purge оставил /etc/stvor-agent"
rmdir /run/systemd/system

[ "$fail" = 0 ] && echo "check-deb-live: $deb — dpkg -i/-r/--purge и вызовы systemctl верны"
exit "$fail"
