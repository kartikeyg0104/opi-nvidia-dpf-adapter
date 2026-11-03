# Hybrid integration design

This file is the **single implementation contract** for the OPI ↔ NVIDIA DPF
adapter (TASK 01.06). Later work that contradicts a section below is out of
scope until this document is updated in the same change as
`config/versions.yaml` and the tests that pin it.

The adapter is a companion, not a fork. It owns neither OPI nor DPF APIs.
It watches unstructured OPI objects and server-side-applies unstructured DPF
objects. Kind-specific logic lives in `config/mappings/*.yaml`.

Machine-checkable sources of truth (this document must not drift from them):

| Topic | Source |
|---|---|
| Versions | `config/versions.yaml`, `pkg/lifecycle/version.go` |
| Enablement | `config/samples/dpuoperatorconfig-nvidia.yaml` |
| Chain graph | `config/mappings/servicefunctionchain.yaml` |
| Status writers | `config/condition-writers.yaml` |
| Uninstall | `translation.opi.nvidia.com/cleanup` finalizer, `make undeploy` |

## Version constraints

Compared on 2026-10-22 against lab host dh2 before the pin was written.

| API | Constraint | Reject |
|---|---|---|
| Kubernetes | v1.36.4 (k3s), Go modules v0.36.0 | different minor than `go.mod` |
| OPI | `config.openshift.io/v1` @ `c107ef719f74d4f841ceb1ed45bbd293b3739a3c` | treating Helm `chart` as upstream-main. It is unmerged [opiproject/dpu-operator#6](https://github.com/opiproject/dpu-operator/pull/6), `patchState: unmerged` |
| NVIDIA DPF | **v26.4.0**, support window **[v26.4.0, v27.0.0)** | older than min → `VendorOperatorSupported=False` reason `TooOld`; at or after v27.0.0 → `TooNew` |
| AMD | known, **Unpinned** | no invented window; no Helm/OLM package (`amd.helm.published: false`) |

Do not bump a pin without comparing installed OPI CRDs to
`test/e2e/crds/opi` and mapping `to:` paths to `test/e2e/crds/dpf`.
`make verify-api` / `TestMappingFieldsExistInVendoredCRDs` is the drift gate.

Live-cluster CRDs on dh2 may lag the v26.4 fixtures (Helm `--take-ownership`
left 2026-09-07 schemas). Mappings stay aligned to the **fixtures**, not the
lagging live CRD. The translator reads the live CRD OpenAPI and omits `spec` fields that
are not declared, then still retries if apply reports `unknown field`.
Mapped `to:` paths stay on the fixtures. OPI-owned gaps that this adapter
owns on the pin (`spec.vendor` enum `nvidia|amd`, SFC `status.conditions`)
are declared on the fixtures and applied to the lab CRDs. They are not
free-form strings and they do not use `x-kubernetes-preserve-unknown-fields`.

## Install strategy

1. Cluster already has OPI CRDs and DPF CRDs. This repo does not install them
   (`make install` is a no-op for APIs we do not own).
2. Deploy the translator + node VSP: `make deploy IMG=...`.
3. Enable NVIDIA with `spec.vendor: nvidia` and/or the annotation sample
   (`config/samples/dpuoperatorconfig-nvidia.yaml`). The field is enum
   `nvidia|amd`. A path, URL, or shell fragment is rejected.
4. Apply OPI `DataProcessingUnit` / `ServiceFunctionChain` objects. The
   translator emits DPF children. The VSP stamps serial / BFB URL annotations;
   mappings never invent serials.
5. LCM runs `EnsureInstalled` (intent → preflight → safe config →
   dependency check → installer → readiness) instead of logging
   "would install". The default installer **detects** an existing vendor
   operator via REST mapping. Missing DPF prerequisites
   (`cert-manager`, Argo CD, Kamaji when a `DPUCluster` is type `kamaji`)
   or an unsupported Kubernetes minor become `Blocked` and skip the
   installer.
   Helm settings come only from `DefaultInstallConfig` (chart
   `dpf-operator` @ `v26.4.0`, repo `https://helm.ngc.nvidia.com/nvidia/doca`,
   namespace `dpf-operator-system`, owner `opi-nvidia-dpf-adapter`). Chart
   URLs and helm args on the object are `InvalidConfig`.    HelmInstaller
   (`pkg/lifecycle/helm_client.go`) may install or adopt that pin only
   when `LifecycleManager.Helm` is set **and** the release is owned by
   `opi-nvidia-dpf-adapter` (or `adopt-existing=true` on the exact pin).
   `cmd/main.go` wires `SecretHelmClient` and `ClusterOLMClient`.
   Latest DPF docs (v26.4) publish Helm only (`dpf.olm.published: false`).
   OLM is observe-only: if `operators.coreos.com` exists, version is
   read from the CSV at `dpf-operator-system/dpf-operator`. The adapter
   never creates a Subscription. Helm storage, when present, wins.
   Missing Helm and OLM fall back to DetectingInstaller.
   AMD has no published DSC operator chart (not the Pollara Network
   Operator, not Lago). Helm/OLM return `ErrNoVendorChart` and the
   chain detects `DSCDevice` when present, or `absent` on BlueField.
   Do not apply stub `DSC*` CRDs on this lab.

Kind e2e is the exception: it installs **vendored** CRDs from `test/e2e/crds/`
into an isolated cluster, never the lab.

## Config annotations

Pinned `DpuOperatorConfig.spec` has `logLevel` and `vendor` (`nvidia|amd`).

| Annotation | Meaning |
|---|---|
| `lifecycle.opi.nvidia.com/vendor` | `nvidia` or `amd`. Anything else → `VendorOperatorSupported=False`, reason `UnknownVendor` |
| `lifecycle.opi.nvidia.com/vendor-operator-version` | optional pin, e.g. `v26.4.0`. Human pin beats auto-detect when Helm/OLM lands |

`spec.vendor` is read first. The annotation is the fallback. Both go
through `SanitizeVendorToken` (`[a-z][a-z0-9-]{0,15}`). Sample:
`config/samples/dpuoperatorconfig-nvidia.yaml`.

Hardware identity (VSP → mapping, not LCM):

| Annotation | Lands on |
|---|---|
| `provisioning.dpu.nvidia.com/serial-number` | `DPUDevice.spec.serialNumber`, `DPU.spec.serialNumber` |
| `dpu.nvidia.com/bfb-url` | `BFB.spec.url` |
| `dpu.nvidia.com/bfb` | BFB / DPU name (`bf-bundle` default) |
| `dpu.nvidia.com/flavor` | Flavor / DPU name (`dpf-default-flavor` default) |
| `dpu.nvidia.com/pf-total-sf` | DPUFlavor `PF_TOTAL_SF` (default official 20; `0` rejected) |
| `dpu.nvidia.com/pf-sf-bar-size` | DPUFlavor `PF_SF_BAR_SIZE` (default official 10; `0` rejected) |
| `dpu.nvidia.com/pf-bar2-enable` | `PF_BAR2_ENABLE` (default 0; explicit 0 kept) |
| `dpu.nvidia.com/per-pf-num-sf` | `PER_PF_NUM_SF` (default 1) |
| `dpu.nvidia.com/num-pf-msix-valid` | `NUM_PF_MSIX_VALID` (default 0) |
| `dpu.nvidia.com/pf-num-pf-msix-valid` | `PF_NUM_PF_MSIX_VALID` (default 1) |
| `dpu.nvidia.com/pf-num-pf-msix` | `PF_NUM_PF_MSIX` (default 228) |
| `dpu.nvidia.com/num-of-vfs` | `NUM_OF_VFS` (default 46) |
| `dpu.nvidia.com/lag-resource-allocation` | `LAG_RESOURCE_ALLOCATION` (default 1) |

## Service-chain resource graph

**Chosen model: direct.** Never emit `DPUDeployment`, `DPUServiceTemplate`, or
`DPUServiceConfiguration`. DPF's DPUDeployment controller would create the
same `DPUService` / interface / chain (and DPUSets against BFB/Flavor already
owned by the DataProcessingUnit mapping). Two controllers on one child is
forbidden.

Owner of every generated CR: the OPI `ServiceFunctionChain`.
Namespace: source namespace, else `dpf-operator-system`.

| Generated CR | Name | Namespace | Owner / GC |
|---|---|---|---|
| `DPUService` | `item.name` | source ns else `dpf-operator-system` | controller ownerRef if same ns; annotation + `translation.opi.nvidia.com/cleanup` if cross-ns |
| `DPUServiceInterface` | `item.name + '-ifc'` | same | same |
| `DPUServiceChain` | `source.metadata.name` | same | same |

Image-only NFs are skipped. Image+chart together is rejected by OPI CEL
(PR #6). Chart fields are not on upstream main.

Not generated (DPF-owned descendants — do not SSA): `ServiceInterfaceSet`,
`ServiceInterface`, `ServiceChainSet`, `ServiceChain`, Helm releases.

DataProcessingUnit graph (NVIDIA, same-namespace / `dpf-operator-system` when
cluster-scoped): `DPUDevice` (`<name>-device`), `DPUFlavor`, `BFB`, `DPU`.
Owner is the OPI `DataProcessingUnit`. AMD uses a different mapping and must
not emit these kinds.

## Status ownership

One writer per `(object GVK, condition type)`. Table:
`config/condition-writers.yaml`.

| Object | Type | Writer | Schema |
|---|---|---|---|
| DataProcessingUnit | `Ready` | vsp-opi-daemon | conditions |
| DataProcessingUnit | `DPFReady` | mapping:dataprocessingunit | conditions |
| DataProcessingUnit | `DPFHardwareCompatible` | mapping:dataprocessingunit | conditions |
| DataProcessingUnit | `DSCReady` | mapping:amd-dsc200 | conditions |
| ServiceFunctionChain | `DPFReady` | mapping:servicefunctionchain | conditions |
| DpuOperatorConfig | `Ready` | vsp-opi-daemon | conditions |
| DpuOperatorConfig | `VendorOperatorSupported` | lifecycle | conditions |
| DpuOperatorConfig | `VendorOperatorReady` | lifecycle | conditions |
| DpuOperatorConfig | `VendorOperatorCleanup` | lifecycle | conditions |
| DpuOperatorConfig | `VendorDPUReady` | lifecycle | conditions |

`Ready` is reserved. Mappings that write it fail `Spec.Validate`. The LCM
must not write it either (`AssessReadiness.Ready` is an in-memory flag;
`VendorOperatorReady` reasons are Installing / Ready / Degraded).
Two vendors on the same type fail the writer-table tests. SFC
`status.conditions` is on the pinned fixture; the translator writes
`DPFReady` only. Status has no preserve-unknown-fields.

The detecting installer reports AMD `DSC*` **absent** when
`dscdevices.dpu.amd.com` is not on the cluster. That is required on a
BlueField host. Do not apply the envtest stub CRDs there.

## Uninstall policy

| Action | What happens | What must not happen |
|---|---|---|
| Update, shrink or rename a chain in an OPI source | The controller prunes children the new desired set no longer names (owner-ref or annotation track, matched on source UID), then re-mirrors status | Leave orphaned old children behind; delete children that still appear in the desired set |
| Delete an OPI `DataProcessingUnit` or `ServiceFunctionChain` | Same-ns children GC via controller ownerRef. Cross-ns children deleted by the cleanup finalizer, then the finalizer is removed | Leave annotation-tracked children; delete DPF CRDs; delete other vendors' objects |
| `make undeploy` | Remove translator + VSP Deployment/RBAC from the cluster | Delete OPI or DPF CRDs; helm-uninstall DPF; wipe lab `/etc` |
| `make uninstall` | No-op (this repo owns no CRDs) | `kubectl delete crd` for `config.openshift.io` or `*.dpu.nvidia.com` |
| Remove `DpuOperatorConfig` NVIDIA annotation | LCM stops claiming NVIDIA. Existing DPF CRs stay until their OPI owners are deleted | Adapter uninstalls the DPF operator |
| Delete `DpuOperatorConfig` (LCM cleanup) | Delete the Helm release only when ownership is `controller` (or adopted with `delete-owned`) **and** `lifecycle.opi.nvidia.com/removal-policy=delete-owned`. Missing, conflicting, or Helm `owner=helm` metadata preserves the release. Shared CRDs and dataplane stay while other tenants or OPI objects still reference them | Delete a resource only because it is in scope; helm-uninstall the live lab DPF; delete DPF CRDs; delete another namespace's objects |

This adapter does not change lab servers (no package installs, no CRD
upgrades, no Helm `--take-ownership` of DPF). Job scratch goes in
`/tmp/lab-job-<name>/` and is deleted afterward.

## Rejection summary

| Input | Result |
|---|---|
| `spec.vendor` not `nvidia`/`amd` | apiserver enum/CEL reject |
| vendor token is a path, URL, or injection | `SanitizeVendorToken` → `UnknownVendor` (raw string is not echoed) |
| vendor annotation not `nvidia`/`amd` | `VendorOperatorSupported=False`, `UnknownVendor` |
| AMD DSC* without a board serial | mapping `when` skips emit and status (no fake device) |
| AMD stub CRDs on a BlueField host | delete from the cluster; keep stubs only in envtest |
| DPF version `< v26.4.0` / `>= v27.0.0` | `TooOld` / `TooNew` |
| image-only NetworkFunction | no DPF child |
| image+chart on one NF | OPI CEL reject |
| mapping emits `DPUDeployment` | `TestSFCMappingIsDirectOwnership` fails |
| mapping writes `Ready` | `Validate` fails, controller will not start |
| two writers on one condition type | writer-table load / tests fail |
| chart URL / helm-args / extra --set on DpuOperatorConfig | `VendorOperatorSupported=False`, `InvalidConfig`; Helm settings stay `DefaultInstallConfig` |
| DPF Helm namespace other than `dpf-operator-system` | rejected; release owner is `opi-nvidia-dpf-adapter` |
| Kubernetes not minor v1.36, or no API access | `VendorOperatorSupported=False`, `Blocked`; installer skipped |
| NVIDIA missing cert-manager or Argo CD | `Blocked`; installer skipped (no false `detected`). Operator installs the chart; this adapter does not. |
| Kamaji missing and a `DPUCluster` has `type=kamaji` | `Blocked`; otherwise Kamaji is not required |
