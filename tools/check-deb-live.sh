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
dpkg_q -r stvor-agent || bad "remove без systemctl упал"
dpkg_q --purge stvor-agent || bad "purge без systemctl упал"

fake_systemctl
echo "== systemctl есть, systemd не запущен (сборка образа)"
dpkg_q -i "$deb"
expect_log "установка без systemd" "enable $unit"
install -d -m 0700 /etc/stvor-agent /var/lib/stvor-agent
: >/etc/stvor-agent/token
: >/var/lib/stvor-agent/state.json
dpkg_q -r stvor-agent
expect_log "remove без systemd" "disable $unit"
[ ! -e /usr/bin/stvor-agent ] || bad "remove оставил /usr/bin/stvor-agent"
[ -e /etc/stvor-agent/token ] || bad "remove удалил токен (это делает только purge)"
[ -e /var/lib/stvor-agent/state.json ] || bad "remove удалил состояние (это делает только purge)"
dpkg_q --purge stvor-agent
expect_log "purge без systemd"
[ ! -e /etc/stvor-agent ] || bad "purge оставил /etc/stvor-agent"
[ ! -e /var/lib/stvor-agent ] || bad "purge оставил /var/lib/stvor-agent"

mkdir -p /run/systemd/system
echo "== systemd запущен"
dpkg_q -i "$deb"
expect_log "установка без токена" "daemon-reload" "enable $unit"
install -d -m 0700 /etc/stvor-agent
: >/etc/stvor-agent/token
dpkg_q -i "$deb"
expect_log "обновление с токеном" "daemon-reload" "try-restart $unit"
dpkg_q -r stvor-agent
expect_log "remove" "disable --now $unit" "daemon-reload"
[ -e /etc/stvor-agent/token ] || bad "remove удалил токен"
dpkg_q --purge stvor-agent
expect_log "purge" "daemon-reload"
[ ! -e /etc/stvor-agent ] || bad "purge оставил /etc/stvor-agent"
rmdir /run/systemd/system

[ "$fail" = 0 ] && echo "check-deb-live: $deb — dpkg -i/-r/--purge и вызовы systemctl верны"
exit "$fail"
