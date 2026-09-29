#!/usr/bin/env bash
# Coverage gates for CI (roadmap decision ②): total statement coverage and
# per-package floors for the packages where historical defects clustered.
#   TOTAL_MIN    overall statement coverage floor (default 55)
#   CRITICAL_MIN per-critical-package floor        (default 70)
set -euo pipefail
cd "$(dirname "$0")/.."

total_min="${TOTAL_MIN:-55}"
critical_min="${CRITICAL_MIN:-70}"
critical_pkgs=(core/matcher/mix core/cache core/inbound core/common)

# Total statement coverage across all packages that have tests.
go test -coverprofile=/tmp/overture_cov.out $(go list ./core/... ./main/... | tr '\n' ' ') > /dev/null 2>&1
total=$(go tool cover -func=/tmp/overture_cov.out | awk '/^total:/ {gsub("%","",$NF); print $NF}')
echo "total coverage: ${total}% (min ${total_min}%)"
awk -v t="$total" -v m="$total_min" 'BEGIN{exit !(t>=m)}' || { echo "FAIL: total coverage ${total}% below ${total_min}%"; exit 1; }

for pkg in "${critical_pkgs[@]}"; do
  cov=$(go test -cover "./$pkg" 2>/dev/null | grep -oE 'coverage: [0-9.]+%' | grep -oE '[0-9.]+' | head -1)
  echo "  $pkg: ${cov}% (min ${critical_min}%)"
  awk -v c="$cov" -v m="$critical_min" 'BEGIN{exit !(c>=m)}' || { echo "FAIL: $pkg coverage ${cov}% below ${critical_min}%"; exit 1; }
done

echo "COVERAGE_GATES_OK"
