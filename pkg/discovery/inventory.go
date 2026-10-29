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
	"fmt"
	"slices"
	"strings"
	"time"
)

// InventoryRefresh is how often the annotator re-reads the PCI bus so a
// reboot or a card that vanished is noticed without a DPU YAML edit.
const InventoryRefresh = 15 * time.Second

// DPUView is the identity-bearing slice of one DataProcessingUnit the
// assignment algorithm needs. No API types: tests and the annotator both
// build this from annotations.
type DPUView struct {
	Name        string
	Annotations map[string]string
}

// Assignment is what one reconcile should do to one DPU. Bound writes
// live identity. Clear drops stale serial/PCI. Both can be set on a
// single-card replacement that converges in one pass.
type Assignment struct {
	Device Device
	Bound  bool
	Clear  bool
}

// LiveDevices is the only inventory the annotator and VSP may treat as
// hardware. Enumerate errors stay errors (do not clear). Devices that
// fail ValidateIdentity are dropped: a missing serial is not a DPU.
func LiveDevices(e Enumerator) ([]Device, error) {
	if e == nil {
		return nil, fmt.Errorf("no hardware enumerator configured")
	}
	found, err := e.Enumerate()
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(found))
	for _, d := range found {
		if err := ValidateIdentity(d); err != nil {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// IdentityKeys are the annotations that mean "this DPU is a live card".
// Flavor and firmware URL are operator config, not inventory.
func IdentityKeys() []string {
	keys := make([]string, 0, 1+len(annotationsByVendor))
	keys = append(keys, PCIAddressAnnotation, ProductNameAnnotation)
	for _, v := range annotationsByVendor {
		keys = append(keys, v.Serial)
	}
	return keys
}

// HasIdentity reports a serial or PCI stamp. Those stamps are not proof
// the card is still on the bus.
func HasIdentity(ann map[string]string) bool {
	return claimedSerial(ann) != "" || claimedPCI(ann) != "" || strings.TrimSpace(ann[ProductNameAnnotation]) != ""
}

// StripIdentity removes serial/PCI stamps so a disappeared card cannot
// keep provisioning alive.
func StripIdentity(ann map[string]string) {
	if ann == nil {
		return
	}
	for _, k := range IdentityKeys() {
		delete(ann, k)
	}
}

// IdentityAnnotations is the vendor-correct stamp for one live device.
func IdentityAnnotations(d Device) (map[string]string, error) {
	if err := ValidateIdentity(d); err != nil {
		return nil, err
	}
	keys, ok := AnnotationsFor(d.VendorID)
	if !ok {
		return nil, fmt.Errorf("device %s: no annotation keys for PCI vendor %#04x", d.PCIAddress, d.VendorID)
	}
	out := map[string]string{keys.Serial: d.SerialNumber, ProductNameAnnotation: d.ProductName}
	if keys.Serial == SerialNumberAnnotation {
		if pci := DPFPCIAddress(d.PCIAddress); pci != "" {
			out[PCIAddressAnnotation] = pci
		}
	}
	return out, nil
}

// AssignDevices maps each DPU to live hardware. Serial wins, then PCI,
// then unused cards onto DPUs that have never been stamped. A stale
// serial is cleared and is not reused as proof. A single leftover card
// may replace a single cleared DPU (card swap / PCI move after reboot).
func AssignDevices(dpus []DPUView, live []Device) map[string]Assignment {
	out := make(map[string]Assignment, len(dpus))
	bySerial := make(map[string]Device, len(live))
	byPCI := make(map[string]Device, len(live)*2)
	for _, d := range live {
		bySerial[d.SerialNumber] = d
		byPCI[d.PCIAddress] = d
		if p := DPFPCIAddress(d.PCIAddress); p != "" {
			byPCI[p] = d
		}
	}
	used := make(map[string]struct{}, len(live))

	for _, dpu := range dpus {
		s := claimedSerial(dpu.Annotations)
		if s == "" {
			continue
		}
		d, ok := bySerial[s]
		if !ok {
			continue
		}
		if _, taken := used[d.SerialNumber]; taken {
			continue
		}
		out[dpu.Name] = Assignment{Device: d, Bound: true}
		used[d.SerialNumber] = struct{}{}
	}

	for _, dpu := range dpus {
		if out[dpu.Name].Bound {
			continue
		}
		p := claimedPCI(dpu.Annotations)
		if p == "" {
			continue
		}
		d, ok := byPCI[p]
		if !ok {
			continue
		}
		if _, taken := used[d.SerialNumber]; taken {
			continue
		}
		out[dpu.Name] = Assignment{Device: d, Bound: true}
		used[d.SerialNumber] = struct{}{}
	}

	var cleared, empty []DPUView
	for _, dpu := range dpus {
		if out[dpu.Name].Bound {
			continue
		}
		if HasIdentity(dpu.Annotations) {
			out[dpu.Name] = Assignment{Clear: true}
			cleared = append(cleared, dpu)
			continue
		}
		empty = append(empty, dpu)
	}

	leftover := unusedDevices(live, used)
	slices.SortFunc(empty, func(a, b DPUView) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(leftover, func(a, b Device) int { return strings.Compare(a.PCIAddress, b.PCIAddress) })
	n := min(len(empty), len(leftover))
	for i := range n {
		d := leftover[i]
		out[empty[i].Name] = Assignment{Device: d, Bound: true}
		used[d.SerialNumber] = struct{}{}
	}

	leftover = unusedDevices(live, used)
	still := make([]DPUView, 0, len(cleared))
	for _, dpu := range cleared {
		if !out[dpu.Name].Bound {
			still = append(still, dpu)
		}
	}
	if len(still) == 1 && len(leftover) == 1 {
		out[still[0].Name] = Assignment{Device: leftover[0], Bound: true, Clear: true}
	}
	return out
}

func unusedDevices(live []Device, used map[string]struct{}) []Device {
	out := make([]Device, 0, len(live))
	for _, d := range live {
		if _, ok := used[d.SerialNumber]; !ok {
			out = append(out, d)
		}
	}
	return out
}

func claimedSerial(ann map[string]string) string {
	for _, keys := range annotationsByVendor {
		if s := strings.TrimSpace(ann[keys.Serial]); s != "" {
			return s
		}
	}
	return ""
}

func claimedPCI(ann map[string]string) string {
	return strings.TrimSpace(ann[PCIAddressAnnotation])
}
