#!/usr/bin/env bash
# End-to-end test of stvor-agent (plan Task 20, spec §15.3): one container
# debian:12 with systemd as PID 1 (sa-e2e-*, removed on any exit), cream by
# pin, a snapshot of the cream-extensions catalog packed by `cream catalog
# pack` and checked against the pin, teleport with a fake agent, stvor-agent
# from dist/ against test/e2e/fakeportal. The scenario is test/e2e/inside.sh.
#
#   test/e2e/run.sh <cream.deb> <stvor-agent.deb> <agent version> <fakeportal> <log dir>
#
# The snapshot: `git archive` of CE_COMMIT (test/e2e/catalog-pin.env) in the
# neighbouring cream-extensions (CREAM_EXTENSIONS_DIR, default
# ../cream-extensions). The pin is the sha256 of the packed archive — content,
# not the commit. E2E_CE_COMMIT / E2E_CATALOG_SHA256 override the pin for a
# local run, with a warning.
#
# No --privileged: systemd runs with the host cgroupns and cgroup v2 mounted
# read-write (as cream's test/integration). Call under the HEAVY lock (make e2e
# takes it).
set -euo pipefail

cream_deb=${1:?cream.deb}
agent_deb=${2:?stvor-agent.deb}
agent_version=${3:?версия агента}
portal=${4:?fakeportal}
logdir=${5:?каталог лога}
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
# shellcheck source=test/e2e/catalog-pin.env
. "$here/catalog-pin.env"

ce_dir=${CREAM_EXTENSIONS_DIR:-$root/../cream-extensions}
commit=${E2E_CE_COMMIT:-$CE_COMMIT}
sha=${E2E_CATALOG_SHA256:-$CATALOG_SHA256}
if [ "$commit" != "$CE_COMMIT" ] || [ "$sha" != "$CATALOG_SHA256" ]; then
	echo "e2e: ПРЕДУПРЕЖДЕНИЕ — каталог не по пину: $commit, sha256 $sha" >&2
fi
for f in "$cream_deb" "$agent_deb" "$portal"; do
	[ -f "$f" ] || { echo "e2e: нет файла $f" >&2; exit 66; }
done
[ -d "$ce_dir" ] || { echo "e2e: нет cream-extensions в $ce_dir (CREAM_EXTENSIONS_DIR)" >&2; exit 66; }

mkdir -p "$logdir"
work=$(mktemp -d "${TMPDIR:-/tmp}/sa-e2e.XXXXXX")
name=sa-e2e-$$
image=sa-e2e:debian-12
cleanup() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
say() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }

say "снимок cream-extensions $commit"
git -C "$ce_dir" archive "$commit" extensions/debian/teleport extensions/debian/stvor-agent >"$work/ce.tar"

say "образ $image"
docker build -q -t "$image" "$here" >/dev/null

say "контейнер $name"
docker run -d --rm --name "$name" --network none \
	--cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
	--tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
	--memory 1g --pids-limit 2048 "$image" >/dev/null
state=""
for _ in $(seq 1 90); do
	state=$(docker exec "$name" systemctl is-system-running 2>/dev/null || true)
	case "$state" in running | degraded) break ;; esac
	sleep 1
done
case "$state" in
running | degraded) ;;
*)
	say "systemd в $name не загрузился: «$state»"
	docker logs "$name" 2>&1 | tail -n 30 >&2
	exit 1
	;;
esac

docker exec "$name" mkdir -p /it
docker cp "$cream_deb" "$name:/it/cream.deb"
docker cp "$agent_deb" "$name:/it/stvor-agent.deb"
docker cp "$portal" "$name:/it/fakeportal"
docker cp "$here/teleport-fake/teleport" "$name:/it/teleport-fake"
docker cp "$here/inside.sh" "$name:/it/inside.sh"
docker cp "$work/ce.tar" "$name:/it/ce.tar"
# docker cp keeps the host uid: the tree is root's, as on a machine.
docker exec "$name" chown -R root:root /it

say "сценарий → $logdir/scenario.log"
rc=0
docker exec -e "CATALOG_SHA256=$sha" -e "AGENT_VERSION=$agent_version" -e "E2E_XFAIL=${E2E_XFAIL:-}" \
	"$name" bash /it/inside.sh >"$logdir/scenario.log" 2>&1 || rc=$?

# Journals and files for the log, whatever the outcome.
docker exec "$name" journalctl --no-pager -o short-iso \
	>"$logdir/journal.log" 2>&1 || true
docker exec "$name" cat /etc/teleport.yaml >"$logdir/teleport.yaml" 2>&1 || true

grep -E '^(ok|FAIL|xfail|XPASS|FATAL|итог)' "$logdir/scenario.log" >&2 || true
if [ "$rc" -ne 0 ]; then
	say "e2e: НЕ ПРОШЁЛ (код $rc), хвост $logdir/scenario.log:"
	tail -n 40 "$logdir/scenario.log" >&2
	exit 1
fi
say "e2e: прошёл — лог $logdir"
