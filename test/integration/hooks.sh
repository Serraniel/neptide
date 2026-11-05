#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Update hooks: a pre-update hook runs in the old container before it stops
# and cancels the update when it fails; a failing post-update hook rolls the
# update back.
# Usage: hooks.sh <neptide-binary>   (uses the local Docker daemon)
set -u
BIN=$(realpath "$1"); S=$(mktemp -d); R=127.0.0.1:15004; IMG=$R/neptide-hooks-it:1; C=neptide-hooks-app
fail() { echo "FAIL: $*"; cleanup; exit 1; }
cleanup() {
  docker rm -f $C $C-neptide-old neptide-hooks-registry >/dev/null 2>&1
  docker volume rm neptide-hooks-data >/dev/null 2>&1; docker rmi -f $IMG >/dev/null 2>&1; rm -rf "$S"
}
cleanup; S=$(mktemp -d)
docker run -d --name neptide-hooks-registry -p $R:5000 registry:3.1 >/dev/null; sleep 2
build() { mkdir -p $S/ctx && printf 'FROM alpine:3.23\nRUN echo %s > /version\nCMD ["sleep","infinity"]\n' "$1" > $S/ctx/Dockerfile; docker build -q -t $IMG $S/ctx >/dev/null 2>&1 && docker push -q $IMG >/dev/null; }
run_neptide() { NEPTIDE_ONCE=true NEPTIDE_STABLE=2s NEPTIDE_HEALTH_TIMEOUT=20s NEPTIDE_SERVICES=false "$BIN" 2>&1 | grep -v 'neptide started'; }
start() { # $1 pre hook, $2 post hook
  docker rm -f $C >/dev/null 2>&1
  docker run -d --name $C --label neptide.enable=true --label "neptide.hook.pre-update=$1" --label "neptide.hook.post-update=$2" \
    -v neptide-hooks-data:/data $IMG >/dev/null
}
version() { docker exec $C cat /version; }

build v1; start 'cp /version /data/before-update' 'true'
build v2; echo "== pre-update hook runs in the old container"; run_neptide
[ "$(version)" = v2 ] || fail "not updated"
[ "$(docker run --rm -v neptide-hooks-data:/d alpine:3.23 cat /d/before-update)" = v1 ] || fail "pre-update hook did not run in the old container"

start 'echo no backup space >&2; exit 3' 'true'
build v3; echo "== failing pre-update hook cancels the update"; out=$(run_neptide); echo "$out"
echo "$out" | grep -q "pre-update hook exited with 3: no backup space" || fail "hook failure not reported"
[ "$(version)" = v2 ] || fail "updated although the pre-update hook failed"
[ "$(docker inspect -f '{{.State.Running}}' $C)" = true ] || fail "container not left running"

start 'true' 'test -f /nonexistent'   # runs v3, built above
build v4; echo "== failing post-update hook rolls back"; out=$(run_neptide); echo "$out"
echo "$out" | grep -q "Rolled back container $C" || fail "no rollback"
[ "$(version)" = v3 ] || fail "rollback did not restore v3"
[ "$(docker ps -a --filter name=$C-neptide-old -q | wc -l)" = 0 ] || fail "parked container left behind"
cleanup; echo PASS
