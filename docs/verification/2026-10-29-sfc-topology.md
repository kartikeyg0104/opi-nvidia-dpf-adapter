# Service-chain and SFC schema verification — 29 October 2026

The current implementation is not a verified complete physical service chain.
Passing existing tests and additional acceptance failures are recorded
separately below. Private addresses, credentials, board serials and raw
operational records remain outside this repository.

| Environment | Executed verification | Result |
|---|---|---|
| Local macOS arm64 workstation | Mapping, translation and conformance suites; isolated envtest API; race detection | 151 Go cases/subtests passed |
| Lab CPU host dh2, Linux x86_64 | Same source snapshot and same suites; isolated envtest API; race detection | 151 Go cases/subtests passed |
| Actual dh2 management API, k3s v1.36.4+k3s1 | Strict server dry-run admission and existing topology inspection | 14 schema checks and 2 reference checks passed |
| Physical BlueField ARM worker | Tenant registration / workload availability | Blocked: zero tenant nodes, CoreDNS 0/2, nine networking DaemonSets with zero targets |

The Mac's arm64 architecture is not evidence of a tested BlueField ARM worker.
No physical packet-forwarding success is claimed.

## Topology observed

The existing live SFC has one HBN DPUService, one service interface using
`net0` on network `default`, and one DPUServiceChain selecting that interface.
Service-to-interface references and chain selectors resolve. The resources
report declarative readiness despite the empty tenant worker set.

A two-function mapping probe emitted two services, two interfaces and one
chain. Each function receives one default service interface and both appear
on one switch. This does not establish explicit hardware ingress/egress
bindings or a measured ordered multi-hop forwarding path.

## Additional acceptance failures

Four extra acceptance probes ran on both hosts: two passed and two failed.
These failures are not hidden by the green existing suite:

1. Independent SFC sources in the same namespace with identical function names
   generate identical DPUService and DPUServiceInterface names. Child naming
   is not scoped by the source chain. Two functions produced four collisions.
2. Image-only input returns no children and no specific unsupported-input
   error. Its generic ServicesNotReady condition does not identify the actual
   input incompatibility. Existing tests deliberately exercise skipping, so
   those tests do not satisfy this stronger acceptance requirement.

These are known remaining gaps, not fixes included in this branch.

## Upstream schema comparison

Real isolated API servers tested chart admission and status storage:

| CRD source | Chart-only admitted | status.conditions retained |
|---|---|---|
| opiproject/dpu-operator main at 3092bcbe6a03e9aa1644041bdb242bed8466e581 | No: image required | No: conditions pruned |
| Chart fork at c107ef719f74d4f841ceb1ed45bbd293b3739a3c | Yes | No: conditions pruned |
| Adapter fixture with chart and status schema additions | Yes | Yes |

The actual lab CRD also accepts charts and conditions. Malformed chart input
and image/chart mutual-exclusion violations were rejected. The status probe
used server dry-run and was confirmed absent afterward; existing source
conditions were inspected directly.

The pinned chart fork alone does not provide the status-condition schema used
by the adapter fixture and lab. That additional compatibility patch must be
explicit in deployments. Upstream chart PR #6 was still open when checked.

References:

- https://github.com/opiproject/dpu-operator/pull/6
- https://raw.githubusercontent.com/opiproject/dpu-operator/3092bcbe6a03e9aa1644041bdb242bed8466e581/config/crd/bases/config.openshift.io_servicefunctionchains.yaml
- https://raw.githubusercontent.com/kartikeyg0104/dpu-operator/c107ef719f74d4f841ceb1ed45bbd293b3739a3c/config/crd/bases/config.openshift.io_servicefunctionchains.yaml

## Reproduce existing software coverage

Configure envtest assets using the project setup, then run:

```bash
make test
go test -race -count=1 ./pkg/mapping ./pkg/translation ./test/conformance
```

The private operational evidence contains the additional acceptance inputs,
generated object graphs, admission responses, host identities, source hashes
and failing assertions. Those records are intentionally not published here.
