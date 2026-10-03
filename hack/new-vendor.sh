#!/usr/bin/env bash
# Scaffold a new vendor adapter from config/vendor-template/.
#
# This is the "clone instead of re-derive" step: it writes a working mapping
# plus a CRD directory for a new vendor, then prints the two things a scaffold
# cannot do for you (the RBAC marker and the conformance case row).
#
# Usage:
#   hack/new-vendor.sh VENDOR=acme GROUP=dpu.acme.com MODEL=A100 [PREFIX=ACME]
#
#   VENDOR  lowercase short name; used in the filename and the namespace
#   GROUP   the vendor's Kubernetes API group
#   MODEL   the card model, used in the vendor guard (e.g. A100)
#   PREFIX  CamelCase prefix for Kinds and the status condition
#           (default: VENDOR upper-cased) -- ACME gives ACMEDevice, ACMEReady
#   OUT     write under this directory instead of the repo (used by the test
#           that keeps this script honest; creates OUT/config/mappings etc.)
#
# The generated vendor guard is  matches('<MODEL>|<PREFIX>')  -- two alternatives
# for the same reason the AMD one is matches('DSC|Pensando'): the string a card
# reports over PCI VPD rarely matches the catalogue name exactly.
set -euo pipefail

for arg in "$@"; do
  case "$arg" in
    VENDOR=*|GROUP=*|MODEL=*|PREFIX=*|OUT=*) export "${arg?}" ;;
    *) echo "unrecognised argument: $arg" >&2; exit 2 ;;
  esac
done

: "${VENDOR:?VENDOR=<lowercase-short-name> is required, e.g. VENDOR=acme}"
: "${GROUP:?GROUP=<api-group> is required, e.g. GROUP=dpu.acme.com}"
: "${MODEL:?MODEL=<card-model> is required, e.g. MODEL=A100}"
PREFIX="${PREFIX:-$(printf '%s' "$VENDOR" | tr '[:lower:]' '[:upper:]')}"

printf '%s' "$VENDOR" | grep -qE '^[a-z0-9][a-z0-9-]*$' \
  || { echo "VENDOR must be lowercase alphanumeric with dashes: got '$VENDOR'" >&2; exit 2; }

root="$(cd "$(dirname "$0")/.." && pwd)"
tpl="$root/config/vendor-template/fieldmapping.yaml"

# OUT redirects the generated files; the template is always read from the repo.
dest="${OUT:-$root}"
mapping="$dest/config/mappings/${VENDOR}.yaml"
crds="$dest/test/conformance/crds/${VENDOR}"
mkdir -p "$(dirname "$mapping")"

[ -f "$tpl" ] || { echo "template missing: $tpl" >&2; exit 1; }
[ -e "$mapping" ] && { echo "refusing to overwrite existing $mapping" >&2; exit 1; }

# Plain string substitution, longest tokens first so a short token (EV) cannot
# eat the inside of a longer one (EV100). No word boundaries: BSD sed has no \b.
sed -e "s|dpu\.example\.com|${GROUP}|g" \
    -e "s|ExampleVendor EV100 2p QSFP56 DPU|${PREFIX} ${MODEL}|g" \
    -e "s|ExampleVendor EV100|${PREFIX} ${MODEL}|g" \
    -e "s|EVDevice|${PREFIX}Device|g" \
    -e "s|EVFirmware|${PREFIX}Firmware|g" \
    -e "s|EVReady|${PREFIX}Ready|g" \
    -e "s|EV100|${MODEL}|g" \
    -e "s|examplevendor-ev100|${VENDOR}|g" \
    -e "s|ev-firmware|${VENDOR}-firmware|g" \
    -e "s|examplevendor|${VENDOR}|g" \
    -e "s|ExampleVendor|${PREFIX}|g" \
    "$tpl" > "$mapping"

# Lines 1-12 of the template are a banner about the template itself.
sed -i.bak '1,12d' "$mapping" && rm -f "${mapping}.bak"
{
  printf '# %s adapter mapping, scaffolded from config/vendor-template/.\n' "$PREFIX"
  printf '# Next steps: docs/vendor-integration.md\n'
  cat "$mapping"
} > "${mapping}.new" && mv "${mapping}.new" "$mapping"

mkdir -p "$crds"
cat > "$crds/README.md" <<EOF
# ${PREFIX} CRD schemas

Put the vendor's real CRD YAML here, one file per CRD. The conformance suite
installs whatever it finds and derives its scheme from the loaded mappings, so
testing a new vendor needs no Go change.

Until the real schemas can be obtained, stubs modelled on the hardware are
acceptable -- but say so in a comment at the top of each file, as
test/conformance/crds/amd/ does. A stub proves the engine translates; it does
not prove the destination schema is right.
EOF

echo "created ${mapping#"$dest/"}"
echo "created ${crds#"$dest/"}/"
cat <<EOF

Two steps remain that a scaffold cannot do for you:

1. RBAC. controller-gen builds the ClusterRole from markers, so add the group
   to pkg/translation/translation_controller.go:

     //+kubebuilder:rbac:groups=${GROUP},resources=*,verbs=get;list;watch;create;update;patch;delete

   then: make manifests

2. A conformance case row in test/conformance/conformance_test.go, with
   readyCondition: "${PREFIX}Ready" and the field values you expect emitted.

Then: make test
EOF
