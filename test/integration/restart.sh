#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Scheduled restarts (neptide.restart): due in an open window, once per window.
# Usage: restart.sh <neptide-binary>   (uses the local Docker daemon)
set -u
BIN=$(realpath "$1"); C=neptide-restart-it
fail() { echo "FAIL: $*"; docker rm -f $C >/dev/null 2>&1; docker rmi -f $C:local >/dev/null 2>&1; exit 1; }
docker rm -f $C >/dev/null 2>&1
# A locally built image: neptide never updates it, so only the schedule acts.
printf 'FROM alpine:3.23\nCMD ["sleep","infinity"]\n' | docker build -q -t $C:local - >/dev/null
# The window opens a minute from now, after the container has started.
window="daily $(date -d '+1 min' +%H:%M)-$(date -d '+10 min' +%H:%M)"
docker run -d --name $C --label neptide.enable=true --label "neptide.restart=$window" $C:local >/dev/null
before=$(docker inspect -f '{{.State.StartedAt}}' $C)
run_neptide() { NEPTIDE_ONCE=true NEPTIDE_STABLE=2s NEPTIDE_SERVICES=false NEPTIDE_NOTIFY= "$BIN" 2>&1 | grep -v 'neptide started'; }
echo "== window not open yet -> no restart"; run_neptide
[ "$(docker inspect -f '{{.State.StartedAt}}' $C)" = "$before" ] || fail "restarted before the window"
sleep $(( 61 - 10#$(date +%S) + 1 ))   # 10#: "08" is not octal
echo "== window open -> restart"; out=$(run_neptide); echo "$out"
echo "$out" | grep -q "Restarted container $C on schedule" || fail "not restarted in the window"
after=$(docker inspect -f '{{.State.StartedAt}}' $C)
[ "$after" != "$before" ] || fail "StartedAt unchanged"
echo "== same window again -> no second restart"; run_neptide
[ "$(docker inspect -f '{{.State.StartedAt}}' $C)" = "$after" ] || fail "restarted twice in one window"
docker rm -f $C >/dev/null; docker rmi -f $C:local >/dev/null; echo PASS
