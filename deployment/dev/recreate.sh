#!/bin/bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

docker compose down
docker volume prune -f -a
docker compose up -d
