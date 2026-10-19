#!/bin/sh
# Reproducibility of the release (spec §9.2): two independent make deb runs with
# one SOURCE_DATE_EPOCH (commit time) into different directories and with
# different, empty Go build caches; their SHA256SUMS must match byte for byte.
# Run from the repository root through make repro-check (under HEAVY_LOCK).
set -eu

MAKE="${MAKE:-make}"
out="${REPRO_DIR:-dist/repro}"
epoch="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}"

rm -rf "$out"
for run in a b; do
	echo "== сборка $run"
	# A fresh GOCACHE per build: equal sums must not come from a shared cache.
	rm -rf ".cache/go-build-repro-$run"
	$MAKE --no-print-directory deb-unlocked ARCH= \
		RELEASE_DIR="$out/$run" GOCACHE_DIR="go-build-repro-$run" \
		SOURCE_DATE_EPOCH="$epoch" DEB_LIVE=0
	rm -rf ".cache/go-build-repro-$run"
done

for run in a b; do
	[ -s "$out/$run/SHA256SUMS" ] || { echo "repro-check: нет $out/$run/SHA256SUMS" >&2; exit 1; }
done

echo "== SHA256SUMS (сборка a)"
cat "$out/a/SHA256SUMS"
if cmp -s "$out/a/SHA256SUMS" "$out/b/SHA256SUMS"; then
	echo "repro-check: обе сборки совпали ($(wc -l <"$out/a/SHA256SUMS") файла, SOURCE_DATE_EPOCH=$epoch)"
else
	echo "repro-check: сборки различаются:" >&2
	diff "$out/a/SHA256SUMS" "$out/b/SHA256SUMS" >&2 || true
	exit 1
fi
