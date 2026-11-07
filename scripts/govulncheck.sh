#!/usr/bin/env bash
# Fails when the code reaches a known vulnerability, except advisories
# accepted below with a reason. Run from the repository root.
set -euo pipefail

# Accepted advisories: ID, then why it is accepted and what removes it.
# None at the moment.
accepted=()

json=$(go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -format json ./...)

# A finding whose first trace frame names a function is reachable from
# our code; findings without one are only imported or required.
reachable=$(jq -r 'select(.finding != null and .finding.trace[0].function != null) | .finding.osv' <<<"$json" | sort -u)

failed=0
for id in $reachable; do
  if printf '%s\n' ${accepted[@]+"${accepted[@]}"} | grep -qx "$id"; then
    echo "accepted: $id"
  else
    echo "reachable vulnerability: $id (https://pkg.go.dev/vuln/$id)"
    failed=1
  fi
done
exit "$failed"
