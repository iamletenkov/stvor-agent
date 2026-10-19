#!/bin/sh
# Внутри контейнера sa-it-*: пакет cream, каталог с фиктивным teleport, тестовый бинарь.
set -eu
dpkg -i /tmp/cream.deb >/dev/null
cream version
cream system init
cream source add sa-fake --path /catalog --sync
cream install teleport
cd /it
SA_IT_CREAM=/usr/bin/cream ./teleport.test -test.v -test.count=1 -test.run '^TestIntegration'
