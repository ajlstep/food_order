#!/usr/bin/env bash
# Copiază .env.example în .env în fiecare serviciu.
# Un .env care există deja nu e suprascris (ca să nu pierzi token-urile);
# ./init-env.sh -f îl suprascrie oricum.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
FORCE=false
[ "${1:-}" = "-f" ] && FORCE=true

for example in "$ROOT"/*/.env.example; do
  [ -f "$example" ] || continue
  dir="$(dirname "$example")"
  name="$(basename "$dir")"
  if [ -f "$dir/.env" ] && ! $FORCE; then
    echo "skip   $name/.env (există deja)"
  else
    cp "$example" "$dir/.env"
    echo "creat  $name/.env"
  fi
done
