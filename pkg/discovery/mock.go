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

// MockEnumerator returns a fixed device list. Use this on kind or any
// machine without a BlueField on the PCI bus. Swap for a PCI enumerator
// on a real worker without changing Annotator.
type MockEnumerator struct {
	Devices []Device
}

func (m MockEnumerator) Enumerate() ([]Device, error) {
	out := make([]Device, len(m.Devices))
	copy(out, m.Devices)
	return out, nil
}

// StaticDevice builds the one-DPU fixture local e2e uses.
func StaticDevice(serial, pci, product string) Device {
	return Device{
		PCIAddress:   pci,
		VendorID:     NVIDIAVendorID,
		SerialNumber: serial,
		ProductName:  product,
	}
}
