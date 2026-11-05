#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Compose/standalone: same-tag rebuild is updated, unhealthy image is rolled back.
# Usage: compose.sh <neptide-binary>   (uses the local Docker daemon)
set -u
BIN=$(realpath "$1"); S=$(mktemp -d); R=127.0.0.1:15000; IMG=$R/neptide-it:1
fail() { echo "FAIL: $*"; cleanup; exit 1; }
cleanup() { docker rm -f neptide-it-app neptide-it-app-neptide-old neptide-it-registry >/dev/null 2>&1; docker rmi -f $IMG >/dev/null 2>&1; }
cleanup
docker run -d --name neptide-it-registry -p $R:5000 registry:3.1 >/dev/null
sleep 2
build() { # $1 = version, $2 = healthcheck
  mkdir -p $S/ctx && printf 'FROM alpine:3.23\nRUN echo %s > /version\nHEALTHCHECK --interval=1s --timeout=1s --retries=2 CMD %s\nCMD ["sleep","infinity"]\n' "$1" "$2" > $S/ctx/Dockerfile
  docker build -q -t $IMG $S/ctx >/dev/null 2>&1 && docker push -q $IMG >/dev/null
}
run_neptide() { NEPTIDE_ONCE=true NEPTIDE_STABLE=2s NEPTIDE_HEALTH_TIMEOUT=20s "$BIN" 2>&1 | grep -v 'neptide started'; }
build v1 "test -f /version"
docker run -d --name neptide-it-app --label neptide.enable=true -e FOO=bar --restart unless-stopped $IMG >/dev/null
echo "== up to date"; run_neptide
build v2 "test -f /version"
# A window that opens in two hours: the update must wait.
closed="daily $(date -d '+2 hour' +%H:%M)-$(date -d '+2 hour 1 min' +%H:%M)"
echo "== outside the maintenance window -> expect no update"
out=$(NEPTIDE_WINDOW="$closed" run_neptide); echo "$out"
echo "$out" | grep -q "waits for its maintenance window" || fail "closed window not reported"
[ "$(docker exec neptide-it-app cat /version)" = v1 ] || fail "updated outside the maintenance window"
open="daily $(date -d '-1 min' +%H:%M)-$(date -d '+10 min' +%H:%M)"
echo "== inside the maintenance window -> expect update"; NEPTIDE_WINDOW="$open" run_neptide
[ "$(docker exec neptide-it-app cat /version)" = v2 ] || fail "not updated to v2"
docker inspect -f '{{range .Config.Env}}{{.}} {{end}}' neptide-it-app | grep -q FOO=bar || fail "env lost"
[ "$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' neptide-it-app)" = unless-stopped ] || fail "restart policy lost"
build v3 "false"
echo "== broken image -> expect rollback"; run_neptide
[ "$(docker exec neptide-it-app cat /version)" = v2 ] || fail "rollback did not restore v2"
[ "$(docker inspect -f '{{.State.Status}}' neptide-it-app)" = running ] || fail "old container not running"
[ "$(docker ps -a --filter name=neptide-it-app-neptide-old -q | wc -l)" = 0 ] || fail "parked container left behind"
cleanup; echo PASS
