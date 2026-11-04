#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# A container that shares another's network namespace (network_mode:
# container:X, e.g. behind a VPN container) keeps working when X is updated
# and when X's update is rolled back.
# Usage: netns.sh <neptide-binary>   (uses the local Docker daemon)
set -u
BIN=$(realpath "$1"); S=$(mktemp -d); R=127.0.0.1:15003; IMG=$R/neptide-netns-it:1
fail() { echo "FAIL: $*"; cleanup; exit 1; }
cleanup() {
  docker rm -f neptide-netns-parent neptide-netns-parent-neptide-old neptide-netns-child neptide-netns-child-neptide-old neptide-netns-registry >/dev/null 2>&1
  docker rmi -f $IMG >/dev/null 2>&1; rm -rf "$S"
}
cleanup; S=$(mktemp -d)
docker run -d --name neptide-netns-registry -p $R:5000 registry:3.1 >/dev/null; sleep 2
build() { # $1 = version, $2 = healthcheck
  mkdir -p $S/ctx && printf 'FROM alpine:3.23\nRUN echo %s > /version\nHEALTHCHECK --interval=1s --timeout=1s --retries=2 CMD %s\nCMD ["sleep","infinity"]\n' "$1" "$2" > $S/ctx/Dockerfile
  docker build -q -t $IMG $S/ctx >/dev/null 2>&1 && docker push -q $IMG >/dev/null
}
run_neptide() { NEPTIDE_ONCE=true NEPTIDE_STABLE=2s NEPTIDE_HEALTH_TIMEOUT=20s NEPTIDE_SERVICES=false "$BIN" 2>&1 | grep -v 'neptide started'; }
has_net() { docker exec neptide-netns-child ip -4 -o addr show eth0 2>/dev/null | grep -q inet; }
joined() { # child is in the namespace of the container now called parent
  [ "$(docker inspect -f '{{.HostConfig.NetworkMode}}' neptide-netns-child)" = "container:$(docker inspect -f '{{.Id}}' neptide-netns-parent)" ]
}

build v1 "test -f /version"
docker run -d --name neptide-netns-parent --label neptide.enable=true $IMG >/dev/null
# Not enabled itself: it must be followed anyway.
docker run -d --name neptide-netns-child --network container:neptide-netns-parent -e KEEP=me alpine:3.23 sleep infinity >/dev/null
has_net || fail "child has no network before the test"

build v2 "test -f /version"
echo "== parent updated -> child re-attached"; out=$(run_neptide); echo "$out"
[ "$(docker exec neptide-netns-parent cat /version)" = v2 ] || fail "parent not updated to v2"
echo "$out" | grep -q "re-attached neptide-netns-child" || fail "child not reported as re-attached"
joined || fail "child does not point at the new parent"
has_net || fail "child lost its network after the parent's update"
docker inspect -f '{{range .Config.Env}}{{.}} {{end}}' neptide-netns-child | grep -q KEEP=me || fail "child env lost"

build v3 "false"
echo "== parent rolled back -> child restarted into the restored namespace"; out=$(run_neptide); echo "$out"
echo "$out" | grep -q "Rolled back container neptide-netns-parent" || fail "parent not rolled back"
[ "$(docker exec neptide-netns-parent cat /version)" = v2 ] || fail "rollback did not restore v2"
joined || fail "child does not point at the restored parent"
has_net || fail "child lost its network after the rollback"
[ "$(docker ps -a --filter name=neptide-old -q | wc -l)" = 0 ] || fail "parked container left behind"
cleanup; echo PASS
