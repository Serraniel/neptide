#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Runs pkg/kuma's integration test against a throwaway Uptime Kuma 2.
set -euo pipefail
cleanup() { docker rm -f neptide-kuma-it >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
docker run -d --name neptide-kuma-it -p 127.0.0.1:13101:3001 louislam/uptime-kuma:2 >/dev/null
U=http://127.0.0.1:13101
until curl -sf "$U/setup-database-info" >/dev/null; do sleep 2; done
curl -sf -X POST -H 'Content-Type: application/json' -d '{"dbConfig":{"type":"sqlite"}}' "$U/setup-database" >/dev/null
until curl -sf "$U/setup-database-info" | grep -q '"needSetup":false' && curl -sf "$U/socket.io/?EIO=4&transport=polling" >/dev/null; do sleep 2; done
KUMA_TEST_URL=$U go test ./pkg/kuma -run TestAgainstKuma -count=1 -v
