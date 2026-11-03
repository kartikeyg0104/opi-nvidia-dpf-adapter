# BlueField reset and ARM worker recovery

DPF's host-agent selects a DPU using the label
`provisioning.dpu.nvidia.com/dpunode-name=<host-node>`. Setting only
`spec.dpuNodeName` leaves that selector empty. The NVIDIA mapping supplies both;
unit selector tests and envtest integration verify the association without
inventing a serial or changing the DPU identity.

An explicit external reboot method is a maintenance hold. A matching DPU can
still be intentionally held rather than reset automatically. The adapter
observer remains read-only with respect to reset execution.

For a reset investigation, record the actual device's supported reset levels,
DPF version, persisted reboot request, host boot ID and ARM agent status. Use
the mechanism supported by the hardware and pinned DPF release. A request,
disconnected SSH, link carrier or firmware-reported running ARM OS does not
prove completion. Verify the persisted request and a fresh ARM agent report
before considering another operation.

The tenant API is a control plane, not a worker. An assigned-DPU count is not a
Kubernetes joined-node count. Recovery requires a real node reporting arm64 and
Ready=True, followed by available CoreDNS replicas and networking DaemonSets.
A stale agent startup report can leave DPF in `DPU Config` despite healthy
host-side controllers.

Check management paths before diagnosing services. Overlapping physical and
virtual-bridge subnets can route SSH through the wrong interface. Compare the
current switch-advertised address, route selection and interface-bound
connectivity. A corrected route proves reachability; authenticated ARM/console
access is still needed to inspect kubelet, containerd and the DPF agent.
Revalidate short-lived bootstrap credentials before a real join attempt.

See [hardware profiles](hardware-profiles.md) for the pinned DPF 26.4 generation
guard. Addresses, serials, boot IDs, credentials and reset evidence belong in
private operational records rather than this repository.
