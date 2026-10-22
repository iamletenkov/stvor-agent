#!/bin/sh
# The OpenWrt delivery of the agent (spec OpenWrt §4.3): <dir>/stvor-agent_<v>_linux_<arch>.tar.gz
# from dist/stvor-agent-linux-<arch> (static, CGO_ENABLED=0), the procd init
# scripts packaging/openwrt/*, the manual page and the licenses. The archive is
# reproducible: ustar, fixed order, owner 0:0, modes set here, mtime
# SOURCE_DATE_EPOCH, gzip -n. Then its listing is checked. Run in the debian
# container of make tarball (GNU tar, gzip), from the repository root.
#
#   tools/tarball.sh <version> <arch> <out dir> <man dir>
set -eu

v=${1:?версия}
arch=${2:?архитектура}
out=${3:?каталог выпуска}
man=${4:?каталог man}
epoch=${SOURCE_DATE_EPOCH:?нужен SOURCE_DATE_EPOCH}
name="stvor-agent_${v}_linux_${arch}.tar.gz"
# Order of the archive; directories before their files.
files="stvor-agent stvor-agent.8.gz init.d init.d/stvor-agent init.d/stvor-agent-telemetry LICENSE NOTICE"

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
install -m 0755 "dist/stvor-agent-linux-$arch" "$stage/stvor-agent"
install -m 0644 "$man/stvor-agent.8.gz" "$stage/stvor-agent.8.gz"
install -d -m 0755 "$stage/init.d"
install -m 0755 packaging/openwrt/stvor-agent packaging/openwrt/stvor-agent-telemetry "$stage/init.d/"
install -m 0644 LICENSE NOTICE "$stage/"

mkdir -p "$out"
# shellcheck disable=SC2086 # $files is the fixed list above
tar --format=ustar --no-recursion --owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
	-C "$stage" -cf - $files | gzip -9 -n >"$out/$name.part"
mv "$out/$name.part" "$out/$name"

# The listing: names, modes and owners, nothing else.
want="-rwxr-xr-x 0/0 stvor-agent
-rw-r--r-- 0/0 stvor-agent.8.gz
drwxr-xr-x 0/0 init.d/
-rwxr-xr-x 0/0 init.d/stvor-agent
-rwxr-xr-x 0/0 init.d/stvor-agent-telemetry
-rw-r--r-- 0/0 LICENSE
-rw-r--r-- 0/0 NOTICE"
got=$(tar --numeric-owner -tvzf "$out/$name" | awk '{print $1, $2, $6}')
if [ "$got" != "$want" ]; then
	echo "tarball: состав $name не тот:" >&2
	echo "$got" >&2
	exit 1
fi
echo "== $out/$name"
