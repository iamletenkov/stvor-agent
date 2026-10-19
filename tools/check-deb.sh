#!/bin/sh
# Contents of one stvor-agent .deb (spec §9.1). Runs in a debian container from
# the repository root (make deb):
#   tools/check-deb.sh VERSION ARCH DIR   — checks DIR/stvor-agent_<v>_<arch>.deb
# Exact file list with modes and owner root, directories 0755, control fields
# (Package, Version, Architecture, Depends, no Pre-Depends/Recommends), the
# maintainer scripts and the unit equal to packaging/, copyright carrying the
# text of LICENSE, an ELF binary of ARCH that is static and, on the host
# architecture, prints the version. Nothing of 3.x (/etc/bisquite, bisquite-*).
set -eu

version="$1" arch="$2" dir="$3"
fail=0
bad() { echo "check-deb: $*" >&2; fail=1; }
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# elf_machine FILE — e_machine of the ELF header (x86-64 = 62, aarch64 = 183).
elf_machine() { od -An -tu2 -j18 -N2 "$1" | tr -d ' '; }
case "$arch" in
amd64) machine=62 uname_m=x86_64 ;;
arm64) machine=183 uname_m=aarch64 ;;
*) echo "check-deb: ARCH=$arch: ожидали amd64 или arm64" >&2; exit 64 ;;
esac

deb="$dir/stvor-agent_${version}_${arch}.deb"
[ -f "$deb" ] || { echo "check-deb: нет $deb" >&2; exit 1; }

dpkg-deb -c "$deb" >"$work/list"
awk '$1 !~ /^d/ {sub(/^\.\//, "", $6); print $1, $2, "/" $6}' "$work/list" | sort >"$work/files"
cat >"$work/want" <<-LIST
	-rw-r--r-- root/root /usr/lib/systemd/system/stvor-agent.service
	-rw-r--r-- root/root /usr/share/doc/stvor-agent/NOTICE
	-rw-r--r-- root/root /usr/share/doc/stvor-agent/copyright
	-rwxr-xr-x root/root /usr/bin/stvor-agent
LIST
sort -o "$work/want" "$work/want"
if ! cmp -s "$work/want" "$work/files"; then
	bad "$deb: состав не совпал со спекой §9.1 (ожидание <, факт >):"
	diff "$work/want" "$work/files" >&2 || true
fi
if awk '$2 != "root/root"' "$work/list" | grep -q .; then
	bad "$deb: есть записи не root/root"
fi
if awk '$1 ~ /^d/ && $1 != "drwxr-xr-x"' "$work/list" | grep -q .; then
	bad "$deb: каталог не 0755"
fi
if grep -Eq 'bisquite|/etc/' "$work/list"; then
	bad "$deb: в составе следы 3.x или /etc: $(grep -E 'bisquite|/etc/' "$work/list" | tr '\n' ' ')"
fi

field() { dpkg-deb -f "$deb" "$1"; }
[ "$(field Package)" = stvor-agent ] || bad "$deb: Package=$(field Package)"
[ "$(field Version)" = "$version" ] || bad "$deb: Version=$(field Version), ожидалось $version"
[ "$(field Architecture)" = "$arch" ] || bad "$deb: Architecture=$(field Architecture)"
[ "$(field Depends)" = "cream (>= 1.0.0)" ] || bad "$deb: Depends=$(field Depends), ожидалось cream (>= 1.0.0)"
for f in Pre-Depends Recommends Conflicts Breaks Replaces Provides; do
	[ -z "$(field "$f")" ] || bad "$deb: лишнее поле $f: $(field "$f")"
done
[ "$(field Section)" = admin ] || bad "$deb: Section=$(field Section)"

dpkg-deb -e "$deb" "$work/control"
for s in postinst prerm postrm; do
	[ -x "$work/control/$s" ] || bad "$deb: нет исполняемого $s"
	cmp -s "$work/control/$s" "packaging/$s" || bad "$deb: $s отличается от packaging/$s"
done
[ ! -e "$work/control/conffiles" ] || bad "$deb: conffiles не ожидались: $(cat "$work/control/conffiles")"

dpkg-deb -x "$deb" "$work/root"
cmp -s "$work/root/usr/lib/systemd/system/stvor-agent.service" packaging/stvor-agent.service ||
	bad "$deb: юнит отличается от packaging/stvor-agent.service"
cmp -s "$work/root/usr/share/doc/stvor-agent/NOTICE" NOTICE || bad "$deb: NOTICE отличается"
# The PolyForm paragraph of copyright is LICENSE, indented by one space, «.» for blank lines.
awk '/^License: PolyForm-Noncommercial-1.0.0$/ && seen++ {on=1; next}
	on && /^$/ {exit}
	on {sub(/^ /, ""); if ($0 == ".") $0 = ""; print}' \
	"$work/root/usr/share/doc/stvor-agent/copyright" >"$work/license"
cmp -s "$work/license" LICENSE || bad "$deb: текст лицензии в copyright отличается от LICENSE"

bin="$work/root/usr/bin/stvor-agent"
[ "$(elf_machine "$bin")" = "$machine" ] || bad "$deb: /usr/bin/stvor-agent собран не под $arch"
# PT_INTERP — only in dynamically linked ELF; a static binary has no «.interp».
if grep -q '/lib.*/ld-linux' "$bin"; then
	bad "$deb: /usr/bin/stvor-agent не статический (есть интерпретатор ld-linux)"
fi
if [ "$(uname -m)" = "$uname_m" ]; then
	got="$("$bin" version --short)" || bad "$deb: stvor-agent version --short упал"
	[ "$got" = "$version" ] || bad "$deb: version --short = '$got', ожидалось '$version'"
fi

[ "$fail" = 0 ] && echo "check-deb: $deb — состав, поля, сценарии, юнит и лицензия верны"
exit "$fail"
