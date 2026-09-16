#!/usr/bin/env bash
#
# Прогон тестов keel в контейнере Debian 13 — том же, на котором стоит
# Proxmox VE 9.
#
#   ./tests/docker.sh              все тесты
#   ./tests/docker.sh config       только те, где в названии есть "config"
#   ./tests/docker.sh --shell      интерактивная оболочка внутри контейнера
#   ./tests/docker.sh --rebuild    пересобрать образ перед прогоном
#
# Зачем контейнер, когда есть ./tests/run.sh: тесты и так проходят на любом
# bash, но окружение Debian 13 — единственный доступный без живого хоста
# способ проверить допущения, на которых построен keel. Прежде всего —
# что JSON::PP умеет relaxed, а whiptail есть из коробки.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

IMAGE="keel-tests"
DOCKERFILE="tests/docker/Dockerfile"

if ! command -v docker >/dev/null 2>&1; then
  printf 'Не найден docker. Тесты можно прогнать и без него: ./tests/run.sh\n' >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  printf 'Docker установлен, но демон недоступен. Без него: ./tests/run.sh\n' >&2
  exit 1
fi

rebuild=0
shell=0
args=()
for a in "$@"; do
  case "$a" in
    --rebuild) rebuild=1 ;;
    --shell)   shell=1 ;;
    *)         args+=("$a") ;;
  esac
done

if (( rebuild )) || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  printf 'Собираю образ %s из %s\n' "$IMAGE" "$DOCKERFILE"
  docker build -q -t "$IMAGE" -f "$DOCKERFILE" tests/docker
fi

# Каталог проекта — только на чтение. Всё, что тестам нужно писать, они
# пишут во временные каталоги внутри контейнера.
docker_args=(
  --rm
  -v "$PWD:/keel:ro"
  -w /keel
)
if (( shell )); then
  docker_args+=(-it --entrypoint /bin/bash)
  exec docker run "${docker_args[@]}" "$IMAGE"
fi

exec docker run "${docker_args[@]}" "$IMAGE" tests/run.sh "${args[@]}"
