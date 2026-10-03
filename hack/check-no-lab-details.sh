#!/usr/bin/env bash
# Fail if private lab access details or real hardware identifiers are about to
# be committed to this public repo.
#
# This exists because they were, once: lab IPs, an SSH key name, a management
# port scan and two real card serials reached the public default branch and had
# to be purged from history. A grep in CI is cheaper than a second force-push.
#
# Provenance notes ("observed on lab host dh1") are fine and deliberately not
# matched -- a bare internal hostname carries no access path. What is blocked is
# anything that locates or authenticates to the hardware, plus real serials.
#
# Usage: hack/check-no-lab-details.sh
set -euo pipefail

self='hack/check-no-lab-details.sh'

# Each entry: <description>|<extended regex>
patterns=(
  'lab subnet address|(^|[^0-9.])(172\.2[0-9]|10\.7\.8)\.[0-9]{1,3}\.[0-9]{1,3}'
  'link-local address|(^|[^0-9.])169\.254\.[0-9]{1,3}\.[0-9]{1,3}'
  'lab SSH key name|[A-Za-z0-9_-]*_lab_key'
  'personal workstation hostname|Kartikeys?-MacBook'
  'real NVIDIA card serial|MT25066004A1'
  'real AMD card serial|MYFLEPK31D02ZH'
)

status=0
for entry in "${patterns[@]}"; do
  desc=${entry%%|*}
  re=${entry#*|}
  # Tracked files only, and never this script's own pattern table.
  if hits=$(git grep -nIE "$re" -- . ":(exclude)$self" 2>/dev/null); then
    printf '\n%s found:\n%s\n' "$desc" "$hits"
    status=1
  fi
done

if [ "$status" -ne 0 ]; then
  cat >&2 <<'MSG'

This repository is public. Replace the values above with placeholders
(MTEXAMPLE0001, DSCEXAMPLE0001, <your-lab-hosts>) or move the note into the
private lab docs, which are intentionally untracked here.
MSG
  exit 1
fi

echo "no lab access details or real hardware serials in tracked files"
