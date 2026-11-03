# Hardware-aware DPF 26.4 provisioning

The PCI inventory and the pinned provisioning stack have different contracts.
BlueField-2 remains discoverable, but DPF 26.4's published tested adapter list
contains BlueField-3 B3240, B3220 and B3210. Selecting the tested generation is
a prerequisite, not proof of the exact SKU, firmware, host OS or dataplane.

The real NVIDIA deployment must select the persistent administrator-owned node
label `hardware.opiproject.org/profile=nvidia-bluefield`. Enroll a node only
after verifying actual BlueField PCI device identity (including BF2 for
inventory). Keep that enrollment through card removal/reboot so discovery can
clear stale identity even after NFD removes transient PCI labels. Enumerate
only NVIDIA devices even on mixed vendor hosts. Ordinary ConnectX NICs must
not enroll a host. Kind's mock deployment needs no hardware labels.

Discovery stamps `discovery.opiproject.org/product-name` from the enumerated
device, removing it with stale serial/PCI identity. The pinned mapping emits
DPF provisioning children only for matching declared and observed BlueField-3
models. Missing, unknown or mismatched identity fails closed. BF2 produces an
explicit UnsupportedHardware result. A DPFHardwareCompatible condition means
generation eligibility only; Ready remains owned by inventory/handshake, and
DPFReady cannot be true for ineligible hardware, even with old Ready children.
Existing children are preserved when eligibility changes; no automatic reset,
firmware installation, deletion or hardware migration is part of this guard.

Firmware values supplied to a vendor-restricted PCI deployment cannot leak to
other vendors because those devices never enter its inventory. Generic PCI
discovery remains available without a vendor filter.

Physical acceptance still requires an authenticated ARM console, a real Ready
worker, available DNS/CNI/OVS/HBN/SR-IOV workloads and measured service-chain
packet forwarding. Unit, API integration and mock e2e passes cannot substitute.

Reference: https://networking-docs.nvidia.com/dpf/26.4.0/platform-support
