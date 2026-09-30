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

package discovery

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultSysfsPCI = "/sys/bus/pci/devices"

const (
	productBlueField2  = "BlueField-2"
	productBlueField3  = "BlueField-3"
	productBlueField   = "BlueField"
	productPensandoDSC = "Pensando DSC"
	productMarvellDPU  = "Marvell DPU"
)

// PCIe Device Serial Number extended capability ID and config-space offset.
const (
	pcieCapDSN      uint16 = 0x0003
	pcieExtCapStart int    = 0x100
)

// vendorSpec describes one DPU vendor: which PCI device IDs are DPU
// functions, and what to call them.
type vendorSpec struct {
	name     string
	products map[uint16]string
	// fallback names a device whose ID is not in products. An empty
	// fallback means "skip unknown device IDs of this vendor".
	fallback string
}

// productName returns the product for deviceID, or "" if this vendor does
// not claim that device.
func (v vendorSpec) productName(deviceID uint16) string {
	if n, ok := v.products[deviceID]; ok {
		return n
	}
	return v.fallback
}

// supportedVendors is the enumerator's whole notion of "is this a DPU".
//
// NVIDIA keeps a fallback: an unrecognised 0x15b3 device still enumerates as
// a generic BlueField, which is the behaviour the annotator has always had.
//
// AMD and Marvell are deliberately strict. On lab host dh1 the AMD DSC2-100
// puts four PCI *bridges* behind the same 0x1dd8 vendor (one Elba upstream
// port and three virtual downstream ports) alongside the two Ethernet
// controllers and the management controller. A bridge has no VPD serial, so
// matching on vendor alone would make readSerial fail and abort enumeration
// of the whole bus. Matching device IDs keeps bridges out entirely.
var supportedVendors = map[uint16]vendorSpec{
	NVIDIAVendorID: {
		name: "NVIDIA",
		products: map[uint16]string{
			0xa2d2: productBlueField2,
			0xa2d6: productBlueField2,
			0xa2dc: productBlueField3,
			0xa2dd: productBlueField3,
		},
		fallback: productBlueField,
	},
	AMDVendorID: {
		name: "AMD Pensando",
		products: map[uint16]string{
			// dh1: 19:00.0 and 1a:00.0, "DSC Ethernet Controller".
			0x1002: productPensandoDSC,
			// dh1: 1b:00.0, "DSC Management Controller" -- the function
			// lab/hardware/dh1/README.md binds with
			// `echo "1dd8 1004" > /sys/bus/pci/drivers/ionic/new_id`.
			0x1004: productPensandoDSC,
		},
	},
	MarvellVendorID: {
		name: "Marvell",
		products: map[uint16]string{
			// MrvlHostDeviceID / MrvlDPUdeviceID in openshift/dpu-operator
			// internal/platform/marvell-dpu.go.
			0xb900: productMarvellDPU,
			0xa0f7: productMarvellDPU,
		},
	},
}

// PCIEnumerator scans sysfs for any supported DPU vendor and reads the board
// serial, matching Intel's GetDpuPcieAddress + ReadDeviceSerialNumber.
// SysfsRoot is overridable so tests can mock /sys/bus/pci/devices without
// hardware.
type PCIEnumerator struct {
	SysfsRoot string
}

func (p PCIEnumerator) sysfsRoot() string {
	if p.SysfsRoot == "" {
		return defaultSysfsPCI
	}
	return p.SysfsRoot
}

func (p PCIEnumerator) Enumerate() ([]Device, error) {
	root := p.sysfsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read pci sysfs %s: %w", root, err)
	}

	var devices []Device
	for _, e := range entries {
		addr := e.Name()
		if pciFunction(addr) != "0" {
			continue
		}
		devDir := filepath.Join(root, addr)
		vendor, err := readPCIID(filepath.Join(devDir, "vendor"))
		if err != nil {
			continue
		}
		spec, ok := supportedVendors[vendor]
		if !ok {
			continue
		}
		deviceID, _ := readPCIID(filepath.Join(devDir, "device"))
		name := spec.productName(deviceID)
		if name == "" {
			// Known vendor, but not a DPU function (e.g. an AMD PCI bridge).
			continue
		}
		serial, err := readSerial(devDir)
		if err != nil {
			return nil, fmt.Errorf("%s device %s: %w", spec.name, addr, err)
		}
		devices = append(devices, Device{
			PCIAddress:   addr,
			VendorID:     vendor,
			DeviceID:     deviceID,
			SerialNumber: serial,
			ProductName:  name,
		})
	}
	return devices, nil
}

func pciFunction(addr string) string {
	i := strings.LastIndex(addr, ".")
	if i < 0 || i == len(addr)-1 {
		return addr
	}
	return addr[i+1:]
}

func readPCIID(path string) (uint16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	v, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return uint16(v), nil
}

// readSerial prefers a sysfs serial file, then VPD keyword SN, then the
// PCIe Device Serial Number extended capability in config space.
func readSerial(devDir string) (string, error) {
	if b, err := os.ReadFile(filepath.Join(devDir, "serial")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}
	if b, err := os.ReadFile(filepath.Join(devDir, "vpd")); err == nil {
		if sn, ok := vpdSerial(b); ok {
			return sn, nil
		}
	}
	if b, err := os.ReadFile(filepath.Join(devDir, "config")); err == nil {
		if sn, ok := configDSN(b); ok {
			return sn, nil
		}
	}
	return "", fmt.Errorf("no serial in serial, vpd, or config DSN")
}

func vpdSerial(data []byte) (string, bool) {
	i := 0
	for i < len(data) {
		if data[i] == 0x78 { // end tag
			return "", false
		}
		if data[i]&0x80 == 0 {
			length := int(data[i] & 0x07)
			i += 1 + length
			continue
		}
		if i+2 >= len(data) {
			return "", false
		}
		tag := data[i] & 0x7f
		length := int(data[i+1]) | int(data[i+2])<<8
		i += 3
		if length < 0 || i+length > len(data) {
			return "", false
		}
		if tag == 0x10 { // VPD-R
			if sn, ok := vpdKeywords(data[i : i+length]); ok {
				return sn, true
			}
		}
		i += length
	}
	return "", false
}

func vpdKeywords(data []byte) (string, bool) {
	i := 0
	for i+3 <= len(data) {
		key := string(data[i : i+2])
		n := int(data[i+2])
		i += 3
		if i+n > len(data) {
			return "", false
		}
		if key == "SN" {
			return strings.TrimSpace(string(data[i : i+n])), true
		}
		i += n
	}
	return "", false
}

func configDSN(cfg []byte) (string, bool) {
	off := pcieExtCapStart
	for off+4 <= len(cfg) {
		hdr := binary.LittleEndian.Uint32(cfg[off : off+4])
		if hdr == 0 {
			return "", false
		}
		id := uint16(hdr & 0xffff)
		next := int((hdr >> 20) & 0xfff)
		if id == pcieCapDSN && off+12 <= len(cfg) {
			return strings.ToUpper(hex.EncodeToString(cfg[off+4 : off+12])), true
		}
		if next == 0 || next <= off {
			return "", false
		}
		off = next
	}
	return "", false
}
