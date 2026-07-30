#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
secret_directory="$(mktemp -d)"
trap 'rm -rf "$secret_directory"' EXIT

printf '%s\n' 'fixture-api-key' >"$secret_directory/global_api_key"
printf '%s\n' 'fixture-postgres-password' >"$secret_directory/postgres_password"
printf '%s\n' 'postgresql://postgres:fixture@postgres:5432/omniwa_auth?sslmode=disable' >"$secret_directory/postgres_auth_dsn"
printf '%s\n' 'postgresql://postgres:fixture@postgres:5432/omniwa_users?sslmode=disable' >"$secret_directory/postgres_users_dsn"
printf '%s\n' 'amqp://fixture:fixture@rabbitmq:5672/default' >"$secret_directory/amqp_url"
printf '%s\n' 'default_user = fixture' 'default_pass = fixture' 'default_vhost = default' >"$secret_directory/rabbitmq.conf"
printf '%s\n' 'fixture-minio-user' >"$secret_directory/minio_root_user"
printf '%s\n' 'fixture-minio-password' >"$secret_directory/minio_root_password"
chmod 0600 "$secret_directory"/*

immutable_digest="sha256:0000000000000000000000000000000000000000000000000000000000000000"
export OMNIWA_IMAGE="example.invalid/omniwa@$immutable_digest"
export POSTGRES_IMAGE="postgres:15-alpine@$immutable_digest"
export RABBITMQ_IMAGE="rabbitmq:4.2.7-management-alpine@$immutable_digest"
export MINIO_IMAGE="quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z@$immutable_digest"
export OMNIWA_ENV_FILE=".env.example"
export OMNIWA_GLOBAL_API_KEY_FILE="$secret_directory/global_api_key"
export OMNIWA_POSTGRES_PASSWORD_FILE="$secret_directory/postgres_password"
export OMNIWA_POSTGRES_AUTH_DSN_FILE="$secret_directory/postgres_auth_dsn"
export OMNIWA_POSTGRES_USERS_DSN_FILE="$secret_directory/postgres_users_dsn"
export OMNIWA_AMQP_URL_FILE="$secret_directory/amqp_url"
export OMNIWA_RABBITMQ_CONFIG_FILE="$secret_directory/rabbitmq.conf"
export OMNIWA_MINIO_ROOT_USER_FILE="$secret_directory/minio_root_user"
export OMNIWA_MINIO_ROOT_PASSWORD_FILE="$secret_directory/minio_root_password"

docker compose --project-directory "$repository_root/docker" \
  --file "$repository_root/docker/docker-compose.yml" config --quiet
docker compose --project-directory "$repository_root/docker" \
  --file "$repository_root/docker/docker-compose.yml" \
  --file "$repository_root/docker/docker-compose.full.yml" config --quiet
docker stack config --compose-file "$repository_root/docker/swarm/docker-stack.yml" >/dev/null

echo "production deployment manifests are valid"
