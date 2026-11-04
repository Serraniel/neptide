#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Data-safe rollback: a broken update that already wrote to the volume is
# rolled back together with the volume's content (neptide.snapshot=true).
# Usage: snapshot.sh <neptide-binary>
set -u
BIN=$(realpath "$1"); S=$(mktemp -d); R=127.0.0.1:15002; IMG=$R/neptide-snap-it:1
fail() { echo "FAIL: $*"; cleanup; exit 1; }
cleanup() {
  docker rm -f neptide-snap-app neptide-snap-app-neptide-old neptide-snap-registry >/dev/null 2>&1
  docker volume rm neptide-snap-data $(docker volume ls -q --filter label=neptide.snapshot.of=neptide-snap-app) >/dev/null 2>&1
  docker rmi -f $IMG >/dev/null 2>&1
}
cleanup
docker run -d --name neptide-snap-registry -p $R:5000 registry:3.1 >/dev/null; sleep 2
build() { # $1 version, $2 healthcheck
  mkdir -p $S/ctx && printf 'FROM alpine:3.23\nHEALTHCHECK --interval=1s --timeout=1s --retries=2 CMD %s\nCMD ["sh","-c","echo %s >> /data/log; sleep infinity"]\n' "$2" "$1" > $S/ctx/Dockerfile
  docker build -q -t $IMG $S/ctx >/dev/null 2>&1 && docker push -q $IMG >/dev/null
}
run() { NEPTIDE_ONCE=true NEPTIDE_STABLE=2s NEPTIDE_HEALTH_TIMEOUT=20s NEPTIDE_SERVICES=false "$BIN" 2>&1 | grep -v 'neptide started'; }
log() { docker run --rm -v neptide-snap-data:/data alpine:3.23 cat /data/log | tr '\n' ' '; }
build v1 true
docker run -d --name neptide-snap-app --label neptide.enable=true --label neptide.snapshot=true -v neptide-snap-data:/data $IMG >/dev/null
sleep 2
build v2 true; run
[ "$(log)" = "v1 v2 " ] || fail "after update the volume is '$(log)', want 'v1 v2 '"
build v3 false; run
case "$(log)" in *v3*) fail "rollback left the broken version's data: '$(log)'";; esac
[ "$(docker inspect -f '{{.State.Status}}' neptide-snap-app)" = running ] || fail "old container not running"
[ "$(docker volume ls -q --filter label=neptide.snapshot.of=neptide-snap-app | wc -l)" -ge 1 ] || fail "no snapshot volume kept"
echo "volume after rollback: $(log)"
cleanup; echo PASS
