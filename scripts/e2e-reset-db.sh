#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
docker compose exec -T db psql -U "${POSTGRES_USER:-geogallery}" -d postgres -v ON_ERROR_STOP=1 \
  -c 'DROP DATABASE IF EXISTS geogallery_e2e WITH (FORCE)' -c 'CREATE DATABASE geogallery_e2e'
