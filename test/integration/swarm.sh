#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Swarm: same-tag rebuild rolls out to all replicas, unhealthy image is rolled
# back by swarm. Runs in a privileged docker:dind container, so the host daemon
# never enters swarm mode. Usage: swarm.sh <static neptide binary>
set -eu
BIN=$(realpath "$1"); DIR=$(cd "$(dirname "$0")" && pwd)
docker rm -f neptide-swarm-it >/dev/null 2>&1 || true
trap 'docker rm -f neptide-swarm-it >/dev/null 2>&1' EXIT
docker run -d --privileged --name neptide-swarm-it -e DOCKER_TLS_CERTDIR= \
  -v "$BIN:/neptide:ro" -v "$DIR/swarm-inner.sh:/inner.sh:ro" docker:29-dind >/dev/null
docker exec neptide-swarm-it sh /inner.sh
