#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Data-safe rollback (neptide.snapshot=true): a broken update that already
# wrote to the container's data is rolled back together with that data,
# while read-only mounts and data other containers use are left alone, and
# single-file binds work.
# Usage: snapshot.sh <neptide-binary>
set -u
# Binds under /tmp are (rightly) never snapshotted: use a directory elsewhere.
BASE=${RUNNER_TEMP:-$HOME}
BIN=$(realpath "$1"); S=$(mktemp -d "$BASE/neptide-snap-it.XXXXXX"); R=127.0.0.1:15002; IMG=$R/neptide-snap-it:1
fail() { echo "FAIL: $*"; cleanup; exit 1; }
cleanup() {
  docker rm -f neptide-snap-app neptide-snap-app-neptide-old neptide-snap-other neptide-snap-registry >/dev/null 2>&1
  docker volume rm neptide-snap-data neptide-snap-shared neptide-snap-registry-data $(docker volume ls -q --filter label=neptide.snapshot.of=neptide-snap-app) >/dev/null 2>&1
  docker rmi -f $IMG >/dev/null 2>&1
  rm -rf "$S"
}
cleanup; S=$(mktemp -d "$BASE/neptide-snap-it.XXXXXX")
docker run -d --name neptide-snap-registry -v neptide-snap-registry-data:/var/lib/registry -p $R:5000 registry:3.1 >/dev/null; sleep 2
build() { # $1 version, $2 healthcheck
  mkdir -p $S/ctx && printf 'FROM alpine:3.23\nHEALTHCHECK --interval=1s --timeout=1s --retries=2 CMD %s\nCMD ["sh","-c","echo %s >> /data/log; echo %s >> /shared/log; echo %s >> /config.yml; sleep infinity"]\n' "$2" "$1" "$1" "$1" > $S/ctx/Dockerfile
  docker build -q -t $IMG $S/ctx >/dev/null 2>&1 && docker push -q $IMG >/dev/null
}
run() { NEPTIDE_ONCE=true NEPTIDE_STABLE=2s NEPTIDE_HEALTH_TIMEOUT=20s NEPTIDE_SERVICES=false "$BIN" 2>&1 | grep -v 'neptide started'; }
vol() { docker run --rm -v "$1":/v alpine:3.23 cat /v/log | tr '\n' ' '; }

mkdir -p $S/ro && echo original > $S/ro/input && echo initial > $S/config.yml
build v1 true
# shared volume: also mounted by another running container
docker run -d --name neptide-snap-other -v neptide-snap-shared:/shared alpine:3.23 sleep infinity >/dev/null
docker run -d --name neptide-snap-app --label neptide.enable=true --label neptide.snapshot=true --label neptide.snapshot.binds=true \
  -v neptide-snap-data:/data -v neptide-snap-shared:/shared -v $S/ro:/ro:ro -v $S/config.yml:/config.yml $IMG >/dev/null
sleep 2
build v2 true; out=$(run); echo "$out"
echo "$out" | grep -q "Updated container" || fail "v2 update did not happen (single-file bind must not break it)"
echo "$out" | grep -q "skips /shared: also used by running container neptide-snap-other" || fail "shared volume not reported as skipped"
echo "$out" | grep -q "skips /ro: read-only" || fail "read-only mount not reported as skipped"
echo "$out" | grep -q "skips /config.yml" && fail "the single-file bind was skipped, it must be snapshotted"
[ "$(vol neptide-snap-data)" = "v1 v2 " ] || fail "data after update: '$(vol neptide-snap-data)'"

echo changed-by-host > $S/ro/input   # the host changes a read-only input meanwhile
build v3 false; run
case "$(vol neptide-snap-data)" in *v3*) fail "rollback left the broken version's data: '$(vol neptide-snap-data)'";; esac
case "$(cat $S/config.yml)" in *v3*) fail "single-file bind not restored: '$(cat $S/config.yml | tr '\n' ' ')'";; esac
[ "$(cat $S/ro/input)" = changed-by-host ] || fail "read-only mount was touched by the restore"
case "$(vol neptide-snap-shared)" in *v3*) ;; *) fail "shared volume was restored under the other container";; esac
[ "$(docker inspect -f '{{.State.Status}}' neptide-snap-app)" = running ] || fail "old container not running"
echo "data after rollback: $(vol neptide-snap-data)| shared: $(vol neptide-snap-shared)| config: $(tr '\n' ' ' < $S/config.yml)"
cleanup; echo PASS
