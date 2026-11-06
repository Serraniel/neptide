#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Runs every Go fuzz target for a while. Usage: tools/fuzz.sh [duration]
# Go fuzzes one target per run, so this finds them with `go test -list`.
# A failing input is written to <package>/testdata/fuzz/<Target>/; commit it
# as a regression seed with the fix.
set -euo pipefail
time=${1:-15s}
failed=0
# A package that does not build must fail here, not be skipped silently.
go vet ./...
for pkg in $(go list ./...); do
  for target in $(go test "$pkg" -list '^Fuzz' | grep '^Fuzz' || true); do
    echo "== $pkg $target ($time)"
    go test "$pkg" -run '^$' -fuzz "^${target}\$" -fuzztime "$time" || failed=1
  done
done
exit $failed
