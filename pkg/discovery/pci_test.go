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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPCIEnumeratorFindsBlueFieldFromSerialFile(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:03:00.0", NVIDIAVendorID, 0xa2dc, map[string][]byte{
		"serial": []byte("MTEXAMPLE0001\n"),
	})
	writePCIDevice(t, root, "0000:03:00.1", NVIDIAVendorID, 0xa2dc, map[string][]byte{
		"serial": []byte("MTEXAMPLE0001\n"),
	})
	writePCIDevice(t, root, "0000:04:00.0", 0x8086, 0x1889, map[string][]byte{
		"serial": []byte("intel-is-not-a-supported-vendor\n"),
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d devices, want 1 (function 0 only, supported vendors only)", len(got))
	}
	d := got[0]
	if d.PCIAddress != "0000:03:00.0" {
		t.Errorf("pci=%s", d.PCIAddress)
	}
	if d.SerialNumber != "MTEXAMPLE0001" {
		t.Errorf("serial=%s", d.SerialNumber)
	}
	if d.ProductName != productBlueField3 {
		t.Errorf("product=%s", d.ProductName)
	}
	if d.VendorID != NVIDIAVendorID || d.DeviceID != 0xa2dc {
		t.Errorf("ids vendor=%#x device=%#x", d.VendorID, d.DeviceID)
	}
}

func TestPCIEnumeratorReadsVPDSerial(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:03:00.0", NVIDIAVendorID, 0xa2d6, map[string][]byte{
		"vpd": buildVPD("MT2119X00001"),
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SerialNumber != "MT2119X00001" {
		t.Fatalf("%+v", got)
	}
	if got[0].ProductName != productBlueField2 {
		t.Errorf("product=%s", got[0].ProductName)
	}
}

func TestPCIEnumeratorReadsConfigDSN(t *testing.T) {
	root := t.TempDir()
	cfg := make([]byte, 0x200)
	// DSN extended cap at 0x100, cap ID 0x0003, next=0
	binary.LittleEndian.PutUint32(cfg[0x100:], uint32(pcieCapDSN))
	copy(cfg[0x104:], []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08})
	writePCIDevice(t, root, "0000:03:00.0", NVIDIAVendorID, 0xa2dc, map[string][]byte{
		"config": cfg,
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SerialNumber != "0102030405060708" {
		t.Fatalf("%+v", got)
	}
	if got[0].ProductName != productBlueField3 {
		t.Errorf("product=%s", got[0].ProductName)
	}
}

func TestPCIEnumeratorFindsAMDPensando(t *testing.T) {
	root := t.TempDir()
	// dh1 shape: two DSC Ethernet Controllers plus the management
	// controller, all function 0, all carrying the same board serial.
	writePCIDevice(t, root, "0000:19:00.0", AMDVendorID, 0x1002, map[string][]byte{
		"vpd": buildVPD("DSCEXAMPLE0001"),
	})
	writePCIDevice(t, root, "0000:1a:00.0", AMDVendorID, 0x1002, map[string][]byte{
		"vpd": buildVPD("DSCEXAMPLE0001"),
	})
	writePCIDevice(t, root, "0000:1b:00.0", AMDVendorID, 0x1004, map[string][]byte{
		"vpd": buildVPD("DSCEXAMPLE0001"),
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d devices, want 1 board serial (three functions share DSCEXAMPLE0001)", len(got))
	}
	if got[0].PCIAddress != "0000:19:00.0" {
		t.Errorf("stable identity should keep the lowest PCI address, got %s", got[0].PCIAddress)
	}
	for _, d := range got {
		if d.VendorID != AMDVendorID {
			t.Errorf("%s vendor=%#x, want %#x", d.PCIAddress, d.VendorID, AMDVendorID)
		}
		if d.SerialNumber != "DSCEXAMPLE0001" {
			t.Errorf("%s serial=%s", d.PCIAddress, d.SerialNumber)
		}
		if d.ProductName != productPensandoDSC {
			t.Errorf("%s product=%s, want %s", d.PCIAddress, d.ProductName, productPensandoDSC)
		}
	}
}

// The DSC2-100 puts four PCI bridges behind vendor 0x1dd8. They have no VPD
// serial, so a vendor-only match would abort the whole scan on real hardware.
func TestPCIEnumeratorSkipsAMDBridgesWithoutSerial(t *testing.T) {
	root := t.TempDir()
	for _, addr := range []string{"0000:17:00.0", "0000:18:00.0", "0000:18:01.0", "0000:18:02.0"} {
		writePCIDevice(t, root, addr, AMDVendorID, 0x1000, nil) // bridge, no serial
	}
	writePCIDevice(t, root, "0000:19:00.0", AMDVendorID, 0x1002, map[string][]byte{
		"vpd": buildVPD("DSCEXAMPLE0001"),
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatalf("bridges must not fail enumeration: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d devices, want only the Ethernet controller", len(got))
	}
	if got[0].PCIAddress != "0000:19:00.0" {
		t.Errorf("pci=%s", got[0].PCIAddress)
	}
}

func TestPCIEnumeratorFindsMarvell(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:01:00.0", MarvellVendorID, 0xb900, map[string][]byte{
		"serial": []byte("MRVL0000CN106\n"),
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d devices, want 1", len(got))
	}
	if got[0].ProductName != productMarvellDPU || got[0].VendorID != MarvellVendorID {
		t.Errorf("%+v", got[0])
	}
}

// One host, three vendors: each card is found exactly once and named by its
// own vendor. This is the property that makes the enumerator multi-vendor.
func TestPCIEnumeratorMixedVendorBus(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:03:00.0", NVIDIAVendorID, 0xa2dc, map[string][]byte{
		"serial": []byte("MTEXAMPLE0001\n"),
	})
	writePCIDevice(t, root, "0000:19:00.0", AMDVendorID, 0x1002, map[string][]byte{
		"vpd": buildVPD("DSCEXAMPLE0001"),
	})
	writePCIDevice(t, root, "0000:01:00.0", MarvellVendorID, 0xa0f7, map[string][]byte{
		"serial": []byte("MRVL0000CN106\n"),
	})
	writePCIDevice(t, root, "0000:04:00.0", 0x8086, 0x1889, map[string][]byte{
		"serial": []byte("intel-is-not-a-supported-vendor\n"),
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	byProduct := map[string]string{}
	for _, d := range got {
		byProduct[d.ProductName] = d.SerialNumber
	}
	want := map[string]string{
		productBlueField3:  "MTEXAMPLE0001",
		productPensandoDSC: "DSCEXAMPLE0001",
		productMarvellDPU:  "MRVL0000CN106",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d devices, want %d: %+v", len(got), len(want), got)
	}
	for product, serial := range want {
		if byProduct[product] != serial {
			t.Errorf("%s serial=%q, want %q", product, byProduct[product], serial)
		}
	}
}

func TestSupportedBlueFieldDeviceIDs(t *testing.T) {
	spec := supportedVendors[NVIDIAVendorID]
	for _, tc := range []struct {
		id   uint16
		want string
	}{
		{0xa2d2, productBlueField2},
		{0xa2d6, productBlueField2},
		{0xa2dc, productBlueField3},
		{0xa2dd, productBlueField3},
	} {
		if got := spec.productName(tc.id); got != tc.want {
			t.Errorf("%#x product=%q, want %q", tc.id, got, tc.want)
		}
	}
	if spec.fallback != "" {
		t.Fatal("NVIDIA fallback would name SoC management (0xc2d3) as a BlueField")
	}
	if spec.productName(0xc2d3) != "" {
		t.Fatal("0xc2d3 is the BlueField SoC management interface, not a DPU PF")
	}
}

func TestPCIEnumeratorSkipsNVIDIAManagementWithoutFailing(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:0f:00.0", NVIDIAVendorID, 0xa2d6, map[string][]byte{
		"vpd": buildVPD("MTEXAMPLE0002"),
	})
	// Function 0 management (seen as 0xc2d3 on some firmware) has no serial.
	writePCIDevice(t, root, "0000:10:00.0", NVIDIAVendorID, 0xc2d3, nil)

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatalf("management function must not abort the bus: %v", err)
	}
	if len(got) != 1 || got[0].PCIAddress != "0000:0f:00.0" || got[0].SerialNumber != "MTEXAMPLE0002" {
		t.Fatalf("%+v", got)
	}
}

func TestPCIEnumeratorDeduplicatesSameSerial(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:0f:00.0", NVIDIAVendorID, 0xa2d6, map[string][]byte{
		"vpd": buildVPD("MTEXAMPLE0002"),
	})
	writePCIDevice(t, root, "0000:11:00.0", NVIDIAVendorID, 0xa2d6, map[string][]byte{
		"vpd": buildVPD("MTEXAMPLE0002"),
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("same board serial listed %d times: %+v", len(got), got)
	}
	if got[0].PCIAddress != "0000:0f:00.0" {
		t.Errorf("stable identity should keep the lowest PCI address, got %s", got[0].PCIAddress)
	}
}

func TestPCIEnumeratorPermissionDeniedIsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode-0 files; this path is for an unprivileged VSP")
	}
	root := t.TempDir()
	writePCIDevice(t, root, "0000:0f:00.0", NVIDIAVendorID, 0xa2d6, map[string][]byte{
		"vpd": []byte("secret"),
	})
	vpd := filepath.Join(root, "0000:0f:00.0", "vpd")
	if err := os.Chmod(vpd, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(vpd, 0o644) })

	_, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err == nil {
		t.Fatal("expected permission error, not a silent missing serial")
	}
	if !strings.Contains(err.Error(), "permission") && !strings.Contains(err.Error(), "denied") {
		t.Fatalf("want permission error, got %v", err)
	}
}

func TestPCIEnumeratorErrorsWhenNVIDIAHasNoSerial(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:03:00.0", NVIDIAVendorID, 0xa2dc, nil)

	_, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err == nil {
		t.Fatal("expected error when 0x15b3 device has no serial")
	}
}

func TestPCIEnumeratorEmptyBus(t *testing.T) {
	root := t.TempDir()
	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d", len(got))
	}
}

func writePCIDevice(t *testing.T, root, addr string, vendor, device uint16, files map[string][]byte) {
	t.Helper()
	dir := filepath.Join(root, addr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vendor", fmt.Sprintf("0x%04x\n", vendor))
	write("device", fmt.Sprintf("0x%04x\n", device))
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func buildVPD(serial string) []byte {
	kw := append([]byte{'S', 'N', byte(len(serial))}, serial...)
	out := make([]byte, 0, 3+len(kw)+1)
	out = append(out, 0x90, byte(len(kw)), 0x00)
	out = append(out, kw...)
	return append(out, 0x78)
}
