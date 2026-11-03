# Live NVIDIA adapter verification — 28 October 2026

Checked at 17:40–17:42 UTC against the existing lab deployment. Private host
addresses, device serials, credentials and raw operational evidence are omitted.

| Question | Observed result |
|---|---|
| Is the adapter running? | Yes: manager 2/2; hardware VSP 1/1; handshake daemon 1/1; 16/16 live runtime checks passed. |
| Does its OPI handshake client connect? | Yes: a fresh execution inside the deployed handshake container completed Init/GetDevices over the shared Unix socket and returned one Healthy device. |
| Is a real BlueField detected? | Yes: PCI inventory identifies BlueField-2 data functions, and both Vendor and OPI GetDevices APIs return the corresponding board identity. |
| Is DPF active? | Yes: fresh controller logs show BFB reconciliation in Ready and DPU reconciliation every five seconds. Provisioning remains in DPU Config waiting for a current-boot ARM agent report. |
| Is the physical deployment operational? | No: zero tenant nodes, CoreDNS 0/2 and no running tenant networking workloads. Physical packet forwarding remains unverified. |

The deployed `opi-daemon` binary is an Init/GetDevices handshake client. Its
success does not certify the full upstream OPI CNI/device-plugin daemon or
continuous dataplane operation. Inventory Ready is separate from provisioning
and traffic readiness.

The active hardware profile reports BF2 inventory with
DPFHardwareCompatible=False/UnsupportedHardware. The DPF 26.4 profile requires
matching requested and observed BF3 generation, with further exact-platform
prerequisites still needing validation. Neither healthy management controllers
nor Argo application status substitutes for a joined ARM worker.

The source PR additionally integrates upstream stale-child cleanup. A real
envtest API regression reproduced deletion of the provisioned DPU when hardware
became ineligible. The optional data-driven `pruneWhen` guard preserves those
resources during hardware revalidation, while default service-chain pruning
and explicit source-deletion cleanup remain enabled. This new integration
change is tested separately from the existing live deployment.
