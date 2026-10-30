#!/usr/bin/env bash
# Pornește sau actualizează toate serviciile food_order în Docker.
#   ./up.sh              - toate serviciile (rebuild doar unde s-a schimbat codul)
#   ./up.sh bot core     - doar serviciile date
#   ./up.sh down         - oprește tot
#   ./up.sh logs bot     - urmărește logurile unui serviciu
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
NETWORK=food_order
# ordinea contează: nats primul, ngrok ultimul
SERVICES=(nuts catalog core parser bot ngrock)

compose() { (cd "$ROOT/$1" && docker compose "${@:2}"); }

case "${1:-}" in
  down)
    for ((i=${#SERVICES[@]}-1; i>=0; i--)); do
      echo "==> stop ${SERVICES[i]}"
      compose "${SERVICES[i]}" down
    done
    exit 0 ;;
  logs)
    compose "${2:?serviciu}" logs -f --tail=100
    exit 0 ;;
esac

targets=("$@")
[ ${#targets[@]} -eq 0 ] && targets=("${SERVICES[@]}")

docker network inspect "$NETWORK" >/dev/null 2>&1 || docker network create "$NETWORK"

for s in "${targets[@]}"; do
  [ -f "$ROOT/$s/docker-compose.yml" ] || { echo "nu există $s/docker-compose.yml" >&2; exit 1; }
  echo "==> $s"
  # --build: reconstruiește imaginea dacă s-a schimbat codul (cache-ul Docker sare peste restul)
  # --remove-orphans: curăță containerele care nu mai sunt în compose
  compose "$s" up -d --build --remove-orphans
done

docker image prune -f >/dev/null
docker ps --filter "network=$NETWORK" --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
