# Vendor integration: onboarding a new DPU vendor

This is the scaffold a new vendor team clones instead of re-deriving the
architecture from the first two vendors' commit history.

It exists because the architecture review asked for exactly that — *"promote the
Vendor Integration Template from convention to scaffold"* — and because
convention had already been tested and found wanting: the AMD port hit three
shape mismatches and one broken vendor guard that nothing in the repo warned
about. Those lessons are now baked into the template rather than written down
and hoped for.

## The five touchpoints

Onboarding one vendor touches five things. Four are data; exactly one is Go, and
it is a comment.

| # | What | Where | Data or Go |
| --- | --- | --- | --- |
| 1 | The field mapping | `config/mappings/<vendor>.yaml` | data |
| 2 | The vendor's CRD schemas | `test/conformance/crds/<vendor>/` | data |
| 3 | A conformance case row | `test/conformance/conformance_test.go` | Go (test only) |
| 4 | RBAC for the new API group | `pkg/translation/translation_controller.go` | Go (a marker comment) |
| 5 | PCI enumeration, *if* the VSP must stamp serials | `pkg/discovery/pci.go`, `types.go` | Go (a table entry) |

There is no per-vendor controller, no `switch obj.GetKind()`, and no vendor
package. The translation controller and the lifecycle manager are generic: they
interpret the mapping documents. That is the property the second vendor was
added to prove, and the conformance suite is what keeps it true.

## Start here

```bash
make new-vendor VENDOR=acme GROUP=dpu.acme.com MODEL=A100 PREFIX=ACME
```

That writes `config/mappings/acme.yaml` from the template and creates
`test/conformance/crds/acme/`, then prints the two steps it cannot do for you
(items 3 and 4 above).

The generated mapping is not a sketch — it loads, validates and translates.
`pkg/mapping/vendor_template_test.go` runs the generator into a temp directory
on every CI run and puts its output through the real loader and the real engine,
so the scaffold cannot rot and the generator cannot silently stop substituting a
token.

## The four things that actually bite

The template demonstrates each of these, because each one cost real time on the
first vendor port.

### 1. The vendor guard is the most important line in the file

Every vendor watches the **same** OPI source kind — that is the point of OPI.
Several mappings therefore reconcile every object of that kind, and each `emit`
needs a `when` guard claiming only its own hardware. Without one, an AMD card
sprouts NVIDIA CRs.

Write the guard against what the card *reports*, not what the catalogue calls
it:

```yaml
when: "source.spec.?dpuProductName.orValue('').matches('DSC|Pensando')"
```

The AMD guard was originally `startsWith('DSC')` and returned false on real
hardware, because the PCI VPD product name is
`Pensando DSC2-100 100G 2p QSFP56 DPU`. Use `matches` with an alternation. The
scaffold generates two alternatives for this reason, and a test fails if they
collapse into the same word.

### 2. Do not invent your own source CRD

The single most common defect across the 28 reviewed submissions was a vendor
replacing `DataProcessingUnit` / `ServiceFunctionChain` with its own CRDs. The
user must keep authoring the OPI kinds; the vendor's CRDs stay invisible.

Note also that `DataProcessingUnit` is **created by the operator**, not by the
user — `DpuDetectorManager.DetectAll` seeds one per detected card. A mapping
consumes it; nothing should expect a human to write one.

### 3. Destination shapes will not match, and that is fine — it is data

Expect all of these, and handle them in the mapping rather than in Go. The
template shows one of each:

| Mismatch | Example |
| --- | --- |
| flat source → nested target | `spec.url` → `spec.image.url` |
| name string → object reference | `dpuDeviceName: foo` → `deviceRef.name: foo` |
| inverted boolean | `nodeEffect.noEffect: true` → `drain.enabled: false` |
| enum derived from a bool | `spec.isDpuSide` → `'dpu'` or `'smartnic'` |

If a mismatch genuinely cannot be expressed, that is a finding about the mapping
spec worth raising — not a reason to add vendor Go.

### 4. `Ready` is reserved — own a vendor-scoped condition

The per-node dpu-operator daemon owns `Ready` on the OPI source. The translation
controller upserts conditions **by type**, so a mapping that also wrote `Ready`
would fight the daemon and flap the condition on every reconcile — and `Ready`
is the CRD's printer column, so losing that race displays a healthy card as not
ready.

Mirror `<PREFIX>Ready` instead. `Spec.Validate` rejects `Ready` outright, so a
mapping that breaks this does not load and the controller will not start.
`docs/multi-vendor.md` §3 has the full model.

Write null-safe CEL in the roll-up (`.?field.orValue(...)`): a child that has
not published status yet must not error it, and a child whose CRD has no status
subresource exposes no conditions at all and should be non-blocking.

## What the conformance suite will hold you to

Adding the case row in step 3 opts the vendor into the shared gate, which runs
in CI via `make test`. It asserts, per vendor:

- controller owner references on every emitted child, as a GC proxy
- the exact translated field values (`wantFields`), including the nested and
  inverted ones — this is what makes it a translation test rather than an
  owner-ref test
- the vendor-scoped condition mirrors `True` once status-capable children report
  ready, and that `Ready` is **absent** on the source
- cleanup on delete, including annotation-tracked cross-namespace children
- multi-vendor isolation: with two vendors' sources in one cluster, each
  produces its own object set and none of the other's

The suite derives its scheme from whatever mappings are loaded, so a third
vendor needs a mapping, CRDs, and a case row — no other Go edit.

## Not yet in the scaffold

Two items from the review's Phase 1 remain open, and the template does not
pretend otherwise:

- **Version-skew behaviour.** The review names it as a conformance dimension.
  There is no implementation to test yet; it needs the lifecycle manager to pin
  and check a vendor operator version first.
- **Detection handshake inside the gate.** It is covered, but as `pkg/vsp` unit
  tests rather than as a conformance dimension.

## Further reading

- `docs/mapping-spec.md` — the full field reference for the mapping document
- `docs/multi-vendor.md` — the AMD port written up as a worked example, including
  what the live hardware changed
- `docs/vsp.md` — the per-node gRPC seam, and when a vendor needs to touch it
