# Real NVIDIA hardware profile

The default deployment is a Kind mock. Real hardware uses
`config/overlays/hardware-vsp` and the independent `config/opi-daemon` handshake.
Both require an administrator-enrolled node. Inspect `lspci -nn` and
`kubectl get node <host-node> --show-labels` before enrollment.

Known BlueField PCI IDs are `15b3:a2d2` and `15b3:a2d6` (BF2), and
`15b3:a2dc` and `15b3:a2dd` (BF3). Vendor `15b3` alone also matches ordinary
NICs. Neither a ConnectX NIC nor management function `15b3:c2d3` establishes a
BlueField data function. After verifying the actual hardware, enroll its host:

```bash
kubectl label node <host-node> hardware.opiproject.org/profile=nvidia-bluefield
```

Keep this administrator-owned label through card removal/replacement so
discovery can clear old product/serial/PCI identity after unplug or reboot.
Transient NFD PCI labels cannot provide that cleanup guarantee. Retire the
enrollment only after stale OPI identity has been cleared. Unenrolled nodes do
not run these hardware operands. The VSP has no node-write permission.

The hardware overlay passes `--pci --pci-vendor=nvidia`. Both inventory and OPI
source ownership are vendor-restricted, including mixed vendor hosts. Generic
`--pci` remains available for multi-vendor discovery; firmware values must be
selected for the vendor being managed. AMD fixture CRDs are test stubs, not a
production operator installation.

Discovery stamps `discovery.opiproject.org/product-name` from inventory.
`spec.dpuProductName` describes the requested model. Both must equal
`BlueField-3` for the pinned DPF 26.4 provisioning mapping to emit children.
BF2 stays visible in GetDevices and inventory Ready, with provisioning blocked
as `UnsupportedHardware`. Missing observation gives `HardwareIdentityMissing`;
conflicting declaration gives `HardwareIdentityMismatch`. Existing DPF children
are preserved for diagnosis.

The mapping's `pruneWhen` guard preserves those provisioned resources when
hardware becomes ineligible, including with controllers that prune stale
children on spec updates. Explicit source deletion still performs finalizer
cleanup; service-chain shrink/rename cleanup keeps its default behavior.

`DPFHardwareCompatible=True` means generation eligibility for this profile.
It does not validate exact SKU, memory, firmware, host OS, power cabling, worker
registration or packet forwarding. The
[DPF 26.4 platform matrix](https://networking-docs.nvidia.com/dpf/26.4.0/platform-support)
lists tested BF3 models B3240, B3220 and B3210 and their prerequisites. BF2
cannot become a tested BF3 platform through a software flag.

Accept physical operation only after a real ARM worker is Ready, CoreDNS and
required tenant DaemonSets are available, a real SR-IOV resource is allocated,
and measured packets traverse the intended OVS/HBN service chain. Mock e2e
results demonstrate controller behavior only.
