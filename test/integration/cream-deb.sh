#!/bin/sh
# Печатает путь к cream_<v>_amd64.deb для интеграционного прогона или пустую строку,
# если прогон пропускается (пин пуст и CREAM_DEB не задан). Пакет по пину скачивается
# в .cache/cream/ и сверяется с sha256 пина; CREAM_DEB берётся как есть, с предупреждением.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=test/integration/cream-pin.env
. "$here/cream-pin.env"

if [ -n "${CREAM_DEB:-}" ]; then
    if [ ! -f "$CREAM_DEB" ]; then
        echo "test-integration: CREAM_DEB=$CREAM_DEB — файла нет" >&2
        exit 1
    fi
    echo "test-integration: ПРЕДУПРЕЖДЕНИЕ — cream не по пину: $CREAM_DEB" \
        "(sha256 $(sha256sum "$CREAM_DEB" | cut -d' ' -f1))" >&2
    printf '%s\n' "$CREAM_DEB"
    exit 0
fi

if [ -z "$CREAM_VERSION" ] || [ -z "$CREAM_SHA256" ]; then
    echo "test-integration: пропуск — пин cream пуст (test/integration/cream-pin.env), CREAM_DEB не задан" >&2
    exit 0
fi

deb=".cache/cream/cream_${CREAM_VERSION}_amd64.deb"
mkdir -p .cache/cream
if [ ! -f "$deb" ] || ! printf '%s  %s\n' "$CREAM_SHA256" "$deb" | sha256sum -c --status; then
    curl -fsSL --proto '=https' -o "$deb.tmp" \
        "$CREAM_URL_BASE/v$CREAM_VERSION/cream_${CREAM_VERSION}_amd64.deb"
    mv "$deb.tmp" "$deb"
fi
if ! printf '%s  %s\n' "$CREAM_SHA256" "$deb" | sha256sum -c --status; then
    echo "test-integration: sha256 $deb не совпал с пином" >&2
    exit 1
fi
printf '%s\n' "$deb"
