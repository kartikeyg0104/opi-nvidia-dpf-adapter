# Hardware discovery VSP
#
# Intel and Marvell VSPs in openshift/dpu-operator are gRPC servers the
# in-tree daemon dials over a unix socket. They identify a DPU by PCI
# vendor/device ID and serial (intel-netsec GetDpuPcieAddress reads the
# serial from the device and matches InitRequest.DpuIdentifier).
#
# This companion repo's translation engine does not speak that gRPC
# surface. It reads annotations on the OPI DataProcessingUnit:
#
#   provisioning.dpu.nvidia.com/serial-number
#   dpu.nvidia.com/bfb-url
#
# cmd/vsp is the missing link. It enumerates hardware (mock today, PCI
# sysfs next) and patches those annotations. The FieldMapping YAML then
# emits DPF objects with a real serialNumber — no Go if/else in the
# translator.
#
# Full LifeCycle/DeviceService/CreateNetworkFunction gRPC, matching
# Intel/Marvell so dpu-operator can launch this VSP as a vendor pod, is
# a follow-up. Do not put that gRPC server in-tree in dpu-operator.
