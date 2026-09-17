# Multi-vendor interoperability (Phase 2)

Phase 1 proved OPI→DPF translation is data. Phase 2 asks the harder question:
is the **engine** vendor-neutral, or did it just happen to fit NVIDIA?

The answer is a second mapping document, `config/mappings/amd-dsc200.yaml`, that
turns the same OPI `DataProcessingUnit` into AMD Pensando DSC objects. It ships
with no new reconciler, no `switch` on vendor, and no change to `pkg/mapping` or
`pkg/translation`.

## 1. CRD discovery: what the workspace actually has

Swept `opi-nvidia-dpf-adapter`, `doca-platform`, `dpu-operator`, `lab`, and
`dpf-validation-tests` for CustomResourceDefinitions.

| Vendor / project | CRDs present | Where |
|---|---|---|
| NVIDIA DPF | yes, 47 | `doca-platform/config/{provisioning,dpuservice}/crd/bases` |
| OPI (openshift) | yes, 4 | `dpu-operator/config/crd/bases` |
| **AMD Pensando** | **none** | — |
| **Marvell / OCTEON** | **none** | — |

No file in the workspace declares an API group matching `*.amd.*`,
`*.pensando.*`, `*.marvell.*`, `*.octeon.*` or `*.cavium.*`.

What *is* present is hardware, not schema:

- `lab/hardware/dh1/README.md` — a real **AMD Pensando DSC2-100**: PCI vendor
  `0x1dd8`, `ionic` driver, VPD product `Pensando DSC2-100 100G 2p QSFP56 DPU`,
  serial `MYFLEPK31D02ZH`, firmware `1.46.0-E-28`, management function at
  `0000:1b:00.0`. Every literal in the AMD mapping and its sample comes from here.
- `lab/hardware/dh3/README.md` — a **Marvell CN10x** (`lspci`: Cavium `b900`).
- `lab/sztp/config/{amd,marvell}-*.{xml,sh}` — sZTP boot provisioning, not k8s API.
- `dpu-operator/internal/platform/marvell-dpu.go` — Marvell PCI detection
  (vendor `177d`, host device `b900`, DPU device `a0f7`).

### The Marvell finding, which changes the target order

Marvell does **not** expose a CR surface at all. Its dpu-operator integration is
a privileged VSP pod (`internal/controller/bindata/vsp/marvell-dpu/99.vsp-pod.yaml`)
running `/vsp-mrvl` and speaking **gRPC**; there are no `kubebuilder:object:root`
types anywhere under `internal/daemon/vendor-specific-plugins/marvell`.

A CEL/JSONPath CR-to-CR mapping has nothing to translate *into* for Marvell.
DH3 is therefore not a test of this engine — it is a test of the VSP path
(`pkg/vsp`). **AMD / DH1 was the right first target**, and the ordering in the
Phase 2 brief holds for a reason we can now state precisely.

### What we still need off the lab hosts

The AMD CRDs in `test/conformance/crds/amd/` are **stubs** written against the
DH1 hardware, not AMD-published schema. They are marked
`opi.nvidia.com/stub: "true"` so nothing can mistake them for the real thing.
To replace them, fetch from **dh1** (or from whoever operates the DSC control plane):

1. **The CRDs themselves** — from the cluster that manages the DSC:
   ```bash
   kubectl get crd -o name | grep -Ei 'amd|pensando|dsc|psm'
   kubectl get crd <each-of-those> -o yaml     # > test/conformance/crds/amd/
   ```
2. **The operator bundle**, if the CRDs are not installed anywhere yet: the
   `config/crd/bases/*.yaml` (or `bundle/manifests/*.yaml`) from whatever repo
   or registry image ships the AMD DSC operator. A repo URL is enough; we
   vendor it the way `test/e2e/crds/dpf` vendors NVIDIA's.
3. **One live CR of each kind** (`kubectl get <kind> -o yaml`). Schema tells us
   what is *allowed*; a golden CR tells us what the operator actually *expects* —
   that is where DPF's "`spec.bfb` is a name string, not an object" class of
   surprise lives, and it is the class of bug the stubs cannot predict.
4. **The operator's namespace and RBAC** — the AMD analogue of
   `dpf-operator-system`. The mapping currently defaults to `amd-dpu-system`;
   that name is a guess and is a one-line change in `defaults.namespace`.
5. **Firmware bundle coordinates** — the real URL scheme and version string for
   the `1.46.0-E-28` image on DH1, for `DSCFirmware.spec.image`.

Until (1)–(5) land, the mapping is structurally proven but the field names are
provisional. Swapping in real CRDs should change `amd-dsc200.yaml` and the
`wantFields` in the conformance case, and nothing else.

### Harvest result, dh1, 2026-09-17

Ran `hack/fetch-amd-crds.sh` against dh1 (172.22.1.1). **There are no AMD CRDs to
fetch, because there is no Kubernetes on dh1 and no AMD software of any kind.**

| Checked | Found |
|---|---|
| `kubectl`, `k3s`, `kubeadm`, `microk8s` | none installed |
| `docker`, `podman`, `crictl`, containerd/kubelet units | none |
| CRDs | n/a, no cluster |
| Operator YAML under `/root /home /opt /srv /etc /usr/local` | none (`/opt`, `/srv` are empty) |
| `penctl`, `dsctl`, `psmctl`, `pdsctl` | none |
| `/etc/pensando`, `/opt/pensando` | absent |
| dpkg packages matching pensando/dsc/ionic/psm | none (only `amd64-microcode`, a CPU package) |
| helm releases | helm not installed |
| DSC management IP 172.22.3.1 | unreachable, :22/:443/:8888 all closed |

`/root/.bash_history` shows the host has only ever been bootstrapped — `apt
update`, `passwd`, `sshd_config`, `lspci`. No operator has ever been installed.

The **hardware** is real and matches the lab docs exactly: Dell PowerEdge R650,
Pensando DSC2 Elba, PCI vendor `0x1dd8` device `0x1002`, serial
`MYFLEPK31D02ZH`, firmware `1.46.0-E-28`, two `ionic`-bound data functions. Note
the host has been renamed from `dh1` to **`opi`**, and the management function
`1b:00.0` is **not bound to any driver** — it still needs the manual
`echo "1dd8 1004" > /sys/bus/pci/drivers/ionic/new_id` from `lab/hardware/dh1/README.md`.

**The stubs in `test/conformance/crds/amd/` therefore remain stubs.** They were
not replaced, because there is nothing real to replace them with. Getting real
schema is now a sourcing problem — find who ships the AMD DSC operator and
obtain the bundle — not a lab-access problem.

### What the live hardware did change: the vendor guard was broken

The trip was still worth it. The card's PCI VPD Product Name is

    Pensando DSC2-100 100G 2p QSFP56 DPU

not the tidy `DSC2-100` every fixture assumed. The original guard,
`startsWith('DSC')`, evaluates **false** against that string — it starts with
"Pensando". On the real card the mapping would have silently emitted nothing,
while passing every local test.

Fixed to `matches('DSC|Pensando')`, which accepts the raw VPD string and a
normalised product name and matches no DPF product. The conformance case is now
driven by the real VPD string (the isolation spec keeps the normalised form, so
both spellings stay covered); reverting the guard fails the suite.

This is the class of bug the stub CRDs cannot catch and a golden CR would have
caught — see item (3) in the fetch list above.

### Related gap: `pkg/discovery` cannot see this card

`pkg/discovery/pci.go` filters on `vendor != NVIDIAVendorID` (`0x15b3`) and skips
everything else, so `cmd/vsp` cannot enumerate a DSC (`0x1dd8`) and cannot stamp
the `dpu.amd.com/serial-number` annotation the mapping requires. On dh1 those
annotations would have to be applied by hand today.

Unlike the mapping, this *is* core Go: NVIDIA's product naming comes from a
device-ID lookup table (`blueFieldDeviceNames`), and a second vendor needs the
equivalent. It is a prerequisite for a genuinely hands-off run on dh1, and it is
not covered by the "a vendor port is pure data" claim.

### Operational note: SSH to the lab

The lab VPN has a path-MTU blackhole below its 1399 MTU. The default
post-quantum KEX reply is large enough to be dropped, so `ssh` hangs at
`SSH2_MSG_KEX_ECDH_REPLY` with no error. Add to `~/.ssh/config`:

```
Host 172.22.* dh?
    KexAlgorithms curve25519-sha256
```

`hack/fetch-amd-crds.sh` sets this itself. It will matter again for `scp` when
staging the controller binary.

## 2. The mapping

`config/mappings/amd-dsc200.yaml` emits four objects in group `dpu.amd.com/v1alpha1`:

| OPI source | DPF (`dataprocessingunit.yaml`) | AMD (`amd-dsc200.yaml`) |
|---|---|---|
| the card | `DPUDevice` | `DSCDevice` |
| personality | `DPUFlavor` | `DSCProfile` |
| firmware | `BFB` | `DSCFirmware` |
| node binding | `DPU` | `DSCNodePolicy` |

The AMD target shape is deliberately **not** a rename of the DPF shape, so the
conformance case exercises the engine rather than a naming convention:

| Translation | DPF | AMD |
|---|---|---|
| flat string → nested object | `BFB.spec.url` | `DSCFirmware.spec.image.url` |
| name string → object ref | `DPU.spec.dpuDeviceName: "x"` | `DSCNodePolicy.spec.deviceRef.name: "x"` |
| inverted boolean | `spec.nodeEffect.noEffect: true` | `spec.drain.enabled: false` |
| derived enum | hardcoded `dpuMode: dpu` | `spec.isDpuSide ? 'dpu' : 'smartnic'` |
| 1:1 JSONPath, renamed | `spec.nodeName → spec.dpuNodeName` | `spec.dpuProductName → spec.productName` |

### Vendor routing

Both mappings watch `DataProcessingUnit`, so `cmd/main.go` starts a controller
for each and **every** DPU is reconciled by both. Each `emit` therefore carries a
`when` guard keyed on `spec.dpuProductName` — the OPI API's own "vendor and model
name" field:

```yaml
when: "source.spec.?dpuProductName.orValue('').matches('DSC|Pensando')"       # AMD
when: "source.spec.?dpuProductName.orValue('BlueField').startsWith('BlueField')" # DPF
```

DPF's guard defaults to `BlueField` when the field is unset, keeping it the
incumbent for existing CRs that predate this field being load-bearing.

`when` is per-emit today, so each guard is repeated four times. A spec-level
`selector:` would collapse that to one line per mapping — the first engine
change worth making, and deliberately **not** made here so this vendor port
stays pure data.

## 3. Known limitation: condition ownership

The controller upserts `status.conditions` **by type**. Two mappings on one
source must not both write `Ready`, or each would overwrite the other every
reconcile. So `amd-dsc200.yaml` mirrors `DSCReady` instead.

That avoids the write loop but leaves a real reporting defect. A healthy AMD card
reconciled by both mappings ends up with:

```
type=Ready     status=False  reason=AllChildrenReady   # from the DPF mapping, which owns nothing here
type=DSCReady  status=True   reason=AllChildrenReady   # from the AMD mapping
```

`Ready=False` is stable (no hot loop), but the `DataProcessingUnit` CRD's printer
column is `.status.conditions[?(@.type=='Ready')].status`, so **`kubectl get dpu`
shows False for a working AMD card.**

The root cause is that `StatusMapping` has no way to say "this mapping does not
own this source, write nothing". The fix is small and belongs in the engine:

```yaml
status:
  when: "source.spec.?dpuProductName.orValue('').matches('DSC|Pensando')"   # skip mirroring when false
```

That is roughly ten lines across `pkg/mapping/spec.go` and `engine.go`, and it
would let both vendors mirror the canonical `Ready`. It is *not* included here,
because Phase 2's claim is that a vendor port needs no Go — and the honest
version of that claim is "no Go, with this one caveat", not a silently patched
engine. Track it as Phase 2.1.

## 4. What the conformance suite proves

`test/conformance` gained two things:

- **A vendor case row** for AMD: owner refs on all four children, the exact
  translated field values (`wantFields`, including the nested and inverted ones),
  and `DSCReady=True` mirrored back after the children report ready. `DSCProfile`
  has no status subresource — the same quirk `DPUFlavor` has — proving the
  non-blocking readiness roll-up is vendor-independent.
- **A `Multi-vendor isolation` spec**: creates one DSC2-100 and one BlueField-3,
  runs *both* mappings over *both* sources as a mixed cluster would, and asserts
  each source produced its own vendor's objects and **none of the other's**.

The suite now derives its scheme from whatever mappings are loaded
(`gvksFromSpecs`), so a third vendor needs a mapping, its CRDs under
`test/conformance/crds/`, and a case row — no other Go edit.

## 5. The one per-vendor Go touch

`controller-gen` builds the ClusterRole from `+kubebuilder:rbac` comments in
`pkg/translation/translation_controller.go`, so a mapping targeting a new API
group needs that group listed there. It is a comment, not logic — but it is the
one place a vendor port is not pure data. Generating RBAC from the mapping
documents would close it.
