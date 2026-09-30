#!/usr/bin/env bash
# Quarterly dependency-security audit (roadmap phase 3).
# Fail the script if govulncheck reports a reachable vulnerability.
# "go list -m -u" is informational: review newer module versions by hand.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== govulncheck (reachable) =="
if ! command -v govulncheck >/dev/null 2>&1; then
  go install golang.org/x/vuln/cmd/govulncheck@latest
fi
govulncheck ./...

echo
echo "== module versions (current -> available) =="
go list -m -u all
echo
echo "DEP_AUDIT_OK"
