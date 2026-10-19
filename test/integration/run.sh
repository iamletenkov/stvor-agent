#!/bin/sh
# Интеграционный прогон адаптера internal/teleport с настоящим cream (plan Task 15):
# один контейнер debian:12 (--rm, имя sa-it-*, память ограничена) ставит пакет cream,
# подключает каталог catalog/teleport-fake, ставит фиктивный teleport и гоняет
# собранный тестовый бинарь (go test -c -tags integration).
# Аргументы: <cream.deb> <тестовый бинарь>.
set -eu
deb=$1
bin=$2
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
abs() { case "$1" in /*) printf '%s\n' "$1" ;; *) printf '%s/%s\n' "$(pwd)" "$1" ;; esac; }
deb=$(abs "$deb")
bin=$(abs "$bin")
image=${SA_IT_IMAGE:-debian:12}

docker run --rm --init --name "sa-it-$$" --memory 1g --pids-limit 512 --network none \
    -v "$deb:/tmp/cream.deb:ro" \
    -v "$here/catalog/teleport-fake:/catalog:ro" \
    -v "$here/inside.sh:/it/inside.sh:ro" \
    -v "$bin:/it/teleport.test:ro" \
    -v "$root/internal/teleport/testdata:/it/testdata:ro" \
    "$image" sh /it/inside.sh
