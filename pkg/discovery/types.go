/*
Copyright 2026 Kartikey Gupta.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package discovery is the NVIDIA Vendor-Specific Plugin hardware path.
//
// Intel and Marvell VSPs in openshift/dpu-operator are gRPC servers that
// the in-tree daemon dials over a unix socket (Init, GetDevices,
// CreateNetworkFunction). They identify a DPU by scanning PCI and reading
// the device serial (see intel-netsec GetDpuPcieAddress).
//
// The translation engine in this companion repo does not talk gRPC. It
// reads OPI DataProcessingUnit annotations. This package is the missing
// link: enumerate BlueField hardware, then stamp those annotations so the
// FieldMapping YAML can emit DPF objects with a real serialNumber.
//
// Enumerator is the Intel-shaped seam. MockEnumerator is for kind/local
// e2e. PCIEnumerator scans sysfs for any supported DPU vendor (NVIDIA,
// AMD Pensando, Marvell) without changing the annotator or the mapping YAML.
package discovery

import "k8s.io/apimachinery/pkg/runtime/schema"

// Annotation keys the FieldMapping YAML already consumes. Changing a
// value here without updating config/mappings/dataprocessingunit.yaml
// will starve the translator of hardware identity.
const (
	SerialNumberAnnotation = "provisioning.dpu.nvidia.com/serial-number"
	BFBURLAnnotation       = "dpu.nvidia.com/bfb-url"
	FlavorAnnotation       = "dpu.nvidia.com/flavor"
	BFBNameAnnotation      = "dpu.nvidia.com/bfb"
)

// PCI vendor IDs of the DPU vendors this plugin can enumerate.
//
// Vendor alone is not a sufficient match for every vendor: see
// supportedVendors in pci.go, which pairs each vendor with the device IDs
// that are actually DPU functions.
const (
	// NVIDIAVendorID is Mellanox/NVIDIA, the BlueField vendor.
	NVIDIAVendorID uint16 = 0x15b3
	// AMDVendorID is AMD Pensando, the DSC/Elba vendor. Observed on lab
	// host dh1 (DSC2-100, ionic driver).
	AMDVendorID uint16 = 0x1dd8
	// MarvellVendorID is Marvell/Cavium, the OCTEON vendor. Matches
	// MrvlVendorID in openshift/dpu-operator internal/platform/marvell-dpu.go.
	MarvellVendorID uint16 = 0x177d
)

// DataProcessingUnitGVK is the cluster-scoped OPI CR the annotator patches.
var DataProcessingUnitGVK = schema.GroupVersionKind{
	Group:   "config.openshift.io",
	Version: "v1",
	Kind:    "DataProcessingUnit",
}

// Device is one DPU function discovered on the node.
type Device struct {
	PCIAddress   string
	VendorID     uint16
	DeviceID     uint16
	SerialNumber string
	ProductName  string
}

// Enumerator finds DPUs on this node. Intel's equivalent is
// platform.PciDevices() + ReadDeviceSerialNumber.
type Enumerator interface {
	Enumerate() ([]Device, error)
}
