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
	"testing"
)

func TestPCIEnumeratorFindsBlueFieldFromSerialFile(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:03:00.0", NVIDIAVendorID, 0xa2dc, map[string][]byte{
		"serial": []byte("MT25066004A1\n"),
	})
	writePCIDevice(t, root, "0000:03:00.1", NVIDIAVendorID, 0xa2dc, map[string][]byte{
		"serial": []byte("MT25066004A1\n"),
	})
	writePCIDevice(t, root, "0000:04:00.0", 0x8086, 0x1889, map[string][]byte{
		"serial": []byte("intel-should-skip\n"),
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d devices, want 1 (function 0 only, NVIDIA only)", len(got))
	}
	d := got[0]
	if d.PCIAddress != "0000:03:00.0" {
		t.Errorf("pci=%s", d.PCIAddress)
	}
	if d.SerialNumber != "MT25066004A1" {
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
	writePCIDevice(t, root, "0000:03:00.0", NVIDIAVendorID, 0x9999, map[string][]byte{
		"config": cfg,
	})

	got, err := PCIEnumerator{SysfsRoot: root}.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SerialNumber != "0102030405060708" {
		t.Fatalf("%+v", got)
	}
	if got[0].ProductName != "BlueField" {
		t.Errorf("unknown device id should fall back, got %s", got[0].ProductName)
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
