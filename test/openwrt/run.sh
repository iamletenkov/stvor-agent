#!/usr/bin/env bash
# End-to-end test of stvor-agent on OpenWrt (spec OpenWrt §4.3, plan Task 4):
# the root FS of OpenWrt 25.12 x86-64 from the openwrt-builder v0.3.1 release
# (musl, busybox ash, rc.common, jshn — as make test-openwrt of cream) is
# imported into a docker image; inside, test/openwrt/scenario.sh under busybox
# sh installs the agent from its release tar.gz, runs the procd init scripts
# through rc.common against a fake ubus (procd is not PID 1 in a container),
# a fake cream (the teleport-openwrt calls) and test/e2e/fakeportal (labels
# and telemetry over HTTPS).
#
#   test/openwrt/run.sh <stvor-agent_<v>_linux_amd64.tar.gz> <version> <bin dir> <log dir>
#
# <bin dir> holds fakeportal and fakeubus (static, amd64). The root FS is
# downloaded once into .cache/openwrt (or taken from OPENWRT_ROOTFS=<file>) and
# checked against OPENWRT_ROOTFS_SHA256. Exit: 0 — passed; 1 — the scenario
# failed (the log names the check); 2 — setup failure.
set -euo pipefail

tarball=${1:?tar.gz агента}
version=${2:?версия агента}
bins=${3:?каталог fakeportal и fakeubus}
logdir=${4:?каталог лога}
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
: "${OPENWRT_ROOTFS_URL:?нужен OPENWRT_ROOTFS_URL}"
: "${OPENWRT_ROOTFS_SHA256:?нужен OPENWRT_ROOTFS_SHA256}"

die() { echo "e2e-openwrt: $*" >&2; exit 2; }
for f in "$tarball" "$bins/fakeportal" "$bins/fakeubus"; do
	[ -f "$f" ] || die "нет файла $f"
done

rootfs=${OPENWRT_ROOTFS:-$root/.cache/openwrt/rootfs-${OPENWRT_ROOTFS_SHA256:0:16}.tar.gz}
if [ ! -f "$rootfs" ]; then
	mkdir -p "$(dirname "$rootfs")"
	curl -fsSL --retry 3 -o "$rootfs.part" "$OPENWRT_ROOTFS_URL" || die "не скачался $OPENWRT_ROOTFS_URL"
	mv -f "$rootfs.part" "$rootfs"
fi
echo "$OPENWRT_ROOTFS_SHA256  $rootfs" | sha256sum -c --status - ||
	die "sha256 корневой ФС $rootfs не совпала с OPENWRT_ROOTFS_SHA256 — удалите файл и проверьте пин"
image="stvor-agent-test-openwrt:${OPENWRT_ROOTFS_SHA256:0:16}"
docker image inspect "$image" >/dev/null 2>&1 || docker import "$rootfs" "$image" >/dev/null || die "docker import"

mkdir -p "$logdir"
work=$(mktemp -d "${TMPDIR:-/tmp}/sa-owrt.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
cp "$tarball" "$work/"
cp "$bins/fakeportal" "$bins/fakeubus" "$here/cream" "$here/scenario.sh" "$work/"
chmod 0755 "$work/fakeportal" "$work/fakeubus" "$work/cream"

log="$logdir/scenario.log"
echo "== e2e-openwrt: $image, $(basename "$tarball") → $log"
rc=0
docker run --rm --init --platform linux/amd64 --network none \
	--tmpfs /tmp:rw,nosuid,nodev,mode=1777 -v "$work:/t:ro" \
	-e "AGENT_VERSION=$version" -e "AGENT_TARBALL=/t/$(basename "$tarball")" \
	"$image" /bin/sh /t/scenario.sh >"$log" 2>&1 || rc=$?
grep -E '^(ok|FAIL|FATAL|итог)' "$log" || true
if [ "$rc" -ne 0 ]; then
	echo "e2e-openwrt: НЕ ПРОШЁЛ (код $rc), хвост $log:" >&2
	tail -n 40 "$log" >&2
	exit 1
fi
echo "e2e-openwrt: прошёл — лог $log"
