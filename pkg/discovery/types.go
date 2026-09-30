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

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

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

// Per-vendor annotation keys. Every vendor's FieldMapping expresses the same
// four concepts, but spells them differently, so the annotator must stamp the
// set that matches the hardware it found. Stamping the NVIDIA keys on an AMD
// card leaves config/mappings/amd-dsc200.yaml with no serial and no firmware
// URL, and both of those are `required: true`.
const (
	// AMD keys, read by config/mappings/amd-dsc200.yaml.
	AMDSerialNumberAnnotation = "dpu.amd.com/serial-number"
	AMDFirmwareURLAnnotation  = "dpu.amd.com/firmware-url"
	AMDProfileAnnotation      = "dpu.amd.com/profile"
	AMDFirmwareNameAnnotation = "dpu.amd.com/firmware"
)

// Marvell keys are RESERVED, not verified. Marvell integrates with the DPU
// Operator over VSP gRPC rather than CRs, so no mapping document reads these
// yet; they exist so enumeration and annotation stay symmetric across every
// vendor pkg/discovery can detect. Revisit the spelling when a Marvell
// FieldMapping lands -- nothing depends on these strings today.
const (
	MarvellSerialNumberAnnotation = "dpu.marvell.com/serial-number"
	MarvellFirmwareURLAnnotation  = "dpu.marvell.com/firmware-url"
	MarvellFlavorAnnotation       = "dpu.marvell.com/flavor"
	MarvellFirmwareNameAnnotation = "dpu.marvell.com/firmware"
)

// VendorAnnotations is the annotation key set one vendor's FieldMapping reads.
// The fields are the concepts; the values are that vendor's spelling of them.
type VendorAnnotations struct {
	// Serial is the board serial number. Every mapping requires it.
	Serial string
	// FirmwareURL is where the firmware bundle is fetched from.
	// NVIDIA calls it the BFB URL; AMD calls it the DSC firmware URL.
	FirmwareURL string
	// Flavor is the provisioning profile. NVIDIA: DPUFlavor. AMD: DSCProfile.
	Flavor string
	// FirmwareName is the name of the firmware CR to reference.
	// NVIDIA: the BFB object. AMD: the DSCFirmware object.
	FirmwareName string
}

var annotationsByVendor = map[uint16]VendorAnnotations{
	NVIDIAVendorID: {
		Serial:       SerialNumberAnnotation,
		FirmwareURL:  BFBURLAnnotation,
		Flavor:       FlavorAnnotation,
		FirmwareName: BFBNameAnnotation,
	},
	AMDVendorID: {
		Serial:       AMDSerialNumberAnnotation,
		FirmwareURL:  AMDFirmwareURLAnnotation,
		Flavor:       AMDProfileAnnotation,
		FirmwareName: AMDFirmwareNameAnnotation,
	},
	MarvellVendorID: {
		Serial:       MarvellSerialNumberAnnotation,
		FirmwareURL:  MarvellFirmwareURLAnnotation,
		Flavor:       MarvellFlavorAnnotation,
		FirmwareName: MarvellFirmwareNameAnnotation,
	},
}

// AnnotationsFor returns the annotation key set for a PCI vendor, and whether
// that vendor is one the annotator can stamp. It deliberately does not fall
// back to the NVIDIA keys: silently labelling an unknown card as NVIDIA is the
// bug this lookup exists to prevent.
func AnnotationsFor(vendorID uint16) (VendorAnnotations, bool) {
	keys, ok := annotationsByVendor[vendorID]
	return keys, ok
}

// Canonical --vendor spellings. Also the names AnnotationsFor's callers log.
const (
	VendorNVIDIA  = "nvidia"
	VendorAMD     = "amd"
	VendorMarvell = "marvell"
)

// vendorNames maps the --vendor flag spelling to a PCI vendor ID. The extra
// aliases are the names people actually say for these cards.
var vendorNames = map[string]uint16{
	VendorNVIDIA:  NVIDIAVendorID,
	"bluefield":   NVIDIAVendorID,
	VendorAMD:     AMDVendorID,
	"pensando":    AMDVendorID,
	VendorMarvell: MarvellVendorID,
	"octeon":      MarvellVendorID,
}

// ParseVendor resolves a vendor name ("amd") or hex PCI id ("0x1dd8") to a
// vendor ID, so mock mode can rehearse any supported vendor without hardware.
func ParseVendor(s string) (uint16, error) {
	key := strings.ToLower(strings.TrimSpace(s))
	if id, ok := vendorNames[key]; ok {
		return id, nil
	}
	if v, err := strconv.ParseUint(strings.TrimPrefix(key, "0x"), 16, 16); err == nil {
		if _, ok := annotationsByVendor[uint16(v)]; ok {
			return uint16(v), nil
		}
		return 0, fmt.Errorf("vendor %s is not a supported DPU vendor", s)
	}
	return 0, fmt.Errorf("unknown vendor %q: want nvidia, amd, marvell, or a hex PCI id", s)
}

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
