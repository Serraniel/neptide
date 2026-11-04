#!/bin/sh
# Runs inside docker:dind (see swarm.sh).
set -u
fail() { echo "FAIL: $*"; exit 1; }
versions() { for c in $(docker ps -q --filter label=com.docker.swarm.service.name=app); do docker exec $c cat /version; done | sort -u | tr "\n" " "; }
R=127.0.0.1:5000; IMG=$R/swarm-it:1
until docker info >/dev/null 2>&1; do sleep 1; done
docker swarm init >/dev/null 2>&1
docker run -d -p 5000:5000 --name reg registry:3.1 >/dev/null
sleep 2
build() { mkdir -p /ctx && printf 'FROM alpine:3.23\nRUN echo %s > /version\nHEALTHCHECK --interval=1s --timeout=1s --retries=2 CMD %s\nCMD ["sleep","infinity"]\n' "$1" "$2" > /ctx/Dockerfile && docker build -q -t $IMG /ctx >/dev/null && docker push -q $IMG >/dev/null; }
nt() { NEPTIDE_ONCE=true NEPTIDE_SWARM_TIMEOUT=2m /neptide 2>&1 | grep -v 'neptide started'; }
build v1 "test -f /version"
docker service create -q --name app --replicas 2 --label neptide.enable=true \
  --update-monitor 5s --update-failure-action rollback $IMG >/dev/null
echo "== swarm: rebuilt image -> expect update"; build v2 "test -f /version"; nt
[ "$(versions)" = "v2 " ] || fail "expected all replicas on v2, got: $(versions)"
echo "== swarm: broken image -> expect swarm rollback"; build v3 "false"; nt
[ "$(docker service inspect -f '{{.UpdateStatus.State}}' app)" = rollback_completed ] || fail "swarm did not roll back"
[ "$(versions)" = "v2 " ] || fail "expected v2 after rollback, got: $(versions)"
echo PASS
