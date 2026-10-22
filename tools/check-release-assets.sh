#!/bin/sh
# The assets of a release in <dir> (CI: after make deb on a PR — the same
# build as the release, with the version 0.0.0-dev — and in release-artifacts
# before the upload): both .deb and both OpenWrt tar.gz exist, each is a line
# of SHA256SUMS, SHA256SUMS has nothing else and its sums hold.
#
#   tools/check-release-assets.sh <version> <dir>
set -eu

v=${1:?версия}
dir=${2:?каталог ассетов}
cd "$dir"
n=0
for f in "stvor-agent_${v}_amd64.deb" "stvor-agent_${v}_arm64.deb" \
	"stvor-agent_${v}_linux_amd64.tar.gz" "stvor-agent_${v}_linux_arm64.tar.gz"; do
	[ -s "$f" ] || { echo "::error::нет $f"; exit 1; }
	grep -qx "[0-9a-f]\{64\}  $f" SHA256SUMS || { echo "::error::$f нет в SHA256SUMS"; exit 1; }
	n=$((n + 1))
done
[ "$(wc -l <SHA256SUMS)" -eq "$n" ] || { echo "::error::в SHA256SUMS не $n строк"; exit 1; }
sha256sum -c SHA256SUMS
