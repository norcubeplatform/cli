#!/usr/bin/env bash
# Fails when the code reaches a known vulnerability, except advisories
# accepted below with a reason. Run from the repository root.
set -euo pipefail

# Accepted advisories: ID, then why it is accepted and what removes it.
accepted=(
  # golang.org/x/crypto/openpgp is deprecated with no fix. It is linked
  # only through github.com/creativeprojects/go-selfupdate's init; the CLI
  # never verifies or creates PGP data. Goes away when `norcube upgrade`
  # stops using go-selfupdate.
  GO-2026-5932
)

json=$(go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -format json ./...)

# A finding whose first trace frame names a function is reachable from
# our code; findings without one are only imported or required.
reachable=$(jq -r 'select(.finding != null and .finding.trace[0].function != null) | .finding.osv' <<<"$json" | sort -u)

failed=0
for id in $reachable; do
  if printf '%s\n' "${accepted[@]}" | grep -qx "$id"; then
    echo "accepted: $id"
  else
    echo "reachable vulnerability: $id (https://pkg.go.dev/vuln/$id)"
    failed=1
  fi
done
exit "$failed"
