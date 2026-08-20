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
# cmd/vsp is the missing link. It enumerates hardware and patches those
# annotations. MockEnumerator is for kind. PCIEnumerator scans
# /sys/bus/pci/devices for vendor 0x15b3 (function 0) and reads serial
# from sysfs `serial`, VPD keyword SN, or the PCIe Device Serial Number
# extended capability. The FieldMapping YAML then emits DPF objects with
# a real serialNumber — no Go if/else in the translator.
#
# On a worker with a BlueField:
#
#   lspci -nn | grep 15b3
#   ls /sys/bus/pci/devices/<bdf>/
#   cat /sys/bus/pci/devices/<bdf>/vendor
#   cat /sys/bus/pci/devices/<bdf>/serial   # if present
#   xxd /sys/bus/pci/devices/<bdf>/vpd      # SN keyword
#
#   go run ./cmd/vsp --metrics-bind-address=0 --pci --node-name="$NODE"
#
# The same process serves LifeCycle/GetDevices on
# /var/run/dpu-daemon/vendor-plugin/vendor-plugin.sock so the in-tree
# dpu-operator daemon can dial it. Stubs come from
# github.com/openshift/dpu-operator/dpu-api (the Go module path still
# used by opiproject/dpu-operator). This repo does not run protoc.
