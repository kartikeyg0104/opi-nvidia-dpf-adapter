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
#   # Day 1 (no cluster): bind the real daemon socket and scan PCI.
#   # --pci without --grpc-only calls GetConfigOrDie and needs kubeconfig.
#   sudo ./vsp --pci --grpc-only --node-name="$(hostname)"
#
#   # Only function 0 is enumerated. serial is often missing; VPD SN or
#   # the PCIe DSN in config is the fallback. Root is required for
#   # /var/run and usually for config-space reads.
#
# The same process serves LifeCycle/GetDevices on
# /var/run/dpu-daemon/vendor-plugin/vendor-plugin.sock so the in-tree
# dpu-operator daemon can dial it.
#
# One unix socket multiplexes:
#   - opi_api.lifecycle.v1alpha1.LifeCycleService / DeviceService
#     (what internal/daemon/plugin GrpcPlugin currently dials)
#   - Vendor.LifeCycleService / DeviceService / NetworkFunctionService
#     (dpu-api)
#
# Stubs are imported; this repo does not run protoc. The lifecycle Go
# module path is github.com/opiproject/opi-api/v1/gen/go/lifecycle with a
# replace to github.com/bn222/opi-api — the same commit the daemon uses.
#
# Local socket check without a cluster (macOS cannot bind /var/run):
#
#   go run ./cmd/vsp --grpc-only --serial-number=MT25066004A1 \
#     --grpc-socket=/tmp/vendor-plugin.sock
#   grpcurl -plaintext unix:///tmp/vendor-plugin.sock list
#   grpcurl -plaintext unix:///tmp/vendor-plugin.sock \
#     opi_api.lifecycle.v1alpha1.DeviceService/GetDevices

