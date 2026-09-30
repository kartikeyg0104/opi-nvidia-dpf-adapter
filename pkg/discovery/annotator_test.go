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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	testNode         = "kind-worker"
	testSerial       = "MTEXAMPLE0001"
	testPCI          = "0000:03:00.0"
	testProduct      = productBlueField3
	testDPUName      = "bf3-worker"
	testBFBURL       = "https://example.invalid/fw.bfb"
	testFlavor       = "test-flavor"
	testFirmwareName = "test-firmware"
)

func mockDevices() MockEnumerator {
	return MockEnumerator{Devices: []Device{StaticDevice(NVIDIAVendorID, testSerial, testPCI, testProduct)}}
}

func TestAnnotatorStampsSerialOnMatchingNode(t *testing.T) {
	scheme := newDPUScheme(t)
	dpu := newDPU(testDPUName, testNode)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

	a := &Annotator{
		Client:      c,
		Scheme:      scheme,
		Enumerator:  mockDevices(),
		NodeName:    testNode,
		FirmwareURL: testBFBURL,
	}

	if _, err := a.Reconcile(context.Background(), requestFor(dpu)); err != nil {
		t.Fatal(err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(DataProcessingUnitGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: testDPUName}, got); err != nil {
		t.Fatal(err)
	}
	ann := got.GetAnnotations()
	if ann[SerialNumberAnnotation] != testSerial {
		t.Fatalf("serial annotation=%q", ann[SerialNumberAnnotation])
	}
	if ann[BFBURLAnnotation] != testBFBURL {
		t.Fatalf("bfb-url annotation=%q", ann[BFBURLAnnotation])
	}
}

func TestAnnotatorSkipsOtherNodes(t *testing.T) {
	scheme := newDPUScheme(t)
	dpu := newDPU("bf3-other", "other-worker")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

	a := &Annotator{
		Client:     c,
		Scheme:     scheme,
		Enumerator: mockDevices(),
		NodeName:   testNode,
	}

	if _, err := a.Reconcile(context.Background(), requestFor(dpu)); err != nil {
		t.Fatal(err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(DataProcessingUnitGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "bf3-other"}, got); err != nil {
		t.Fatal(err)
	}
	if got.GetAnnotations()[SerialNumberAnnotation] != "" {
		t.Fatalf("stamped a DPU on a different node: %v", got.GetAnnotations())
	}
}

func TestAnnotatorIsIdempotent(t *testing.T) {
	scheme := newDPUScheme(t)
	dpu := newDPU(testDPUName, testNode)
	dpu.SetAnnotations(map[string]string{SerialNumberAnnotation: testSerial})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

	a := &Annotator{
		Client:     c,
		Scheme:     scheme,
		Enumerator: mockDevices(),
		NodeName:   testNode,
	}

	if _, err := a.Reconcile(context.Background(), requestFor(dpu)); err != nil {
		t.Fatal(err)
	}
}

func TestAnnotatorRejectsEmptySerial(t *testing.T) {
	scheme := newDPUScheme(t)
	dpu := newDPU(testDPUName, testNode)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

	a := &Annotator{
		Client:     c,
		Scheme:     scheme,
		Enumerator: MockEnumerator{Devices: []Device{{PCIAddress: testPCI}}},
		NodeName:   testNode,
	}

	if _, err := a.Reconcile(context.Background(), requestFor(dpu)); err == nil {
		t.Fatal("expected error for empty serial")
	}
}

// The serial key must follow the hardware. Stamping the NVIDIA key on an AMD
// card leaves amd-dsc200.yaml without a serial and fails its required field.
func TestAnnotatorStampsVendorSpecificSerialKey(t *testing.T) {
	cases := []struct {
		name     string
		vendorID uint16
		product  string
		want     VendorAnnotations
	}{
		{VendorNVIDIA, NVIDIAVendorID, productBlueField3, VendorAnnotations{
			SerialNumberAnnotation, BFBURLAnnotation, FlavorAnnotation, BFBNameAnnotation}},
		{VendorAMD, AMDVendorID, productPensandoDSC, VendorAnnotations{
			AMDSerialNumberAnnotation, AMDFirmwareURLAnnotation,
			AMDProfileAnnotation, AMDFirmwareNameAnnotation}},
		{VendorMarvell, MarvellVendorID, productMarvellDPU, VendorAnnotations{
			MarvellSerialNumberAnnotation, MarvellFirmwareURLAnnotation,
			MarvellFlavorAnnotation, MarvellFirmwareNameAnnotation}},
	}
	// Every key any vendor could write, so a case can assert the others stayed off.
	allKeys := make([]string, 0, 4*len(annotationsByVendor))
	for _, v := range annotationsByVendor {
		allKeys = append(allKeys, v.Serial, v.FirmwareURL, v.Flavor, v.FirmwareName)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := newDPUScheme(t)
			dpu := newDPU(testDPUName, testNode)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

			a := &Annotator{
				Client: c,
				Scheme: scheme,
				Enumerator: MockEnumerator{Devices: []Device{
					StaticDevice(tc.vendorID, testSerial, testPCI, tc.product),
				}},
				NodeName:     testNode,
				FirmwareURL:  testBFBURL,
				Flavor:       testFlavor,
				FirmwareName: testFirmwareName,
			}

			if _, err := a.Reconcile(context.Background(), requestFor(dpu)); err != nil {
				t.Fatal(err)
			}

			got := &unstructured.Unstructured{}
			got.SetGroupVersionKind(DataProcessingUnitGVK)
			if err := c.Get(context.Background(), types.NamespacedName{Name: testDPUName}, got); err != nil {
				t.Fatal(err)
			}
			ann := got.GetAnnotations()
			mine := map[string]string{
				tc.want.Serial:       testSerial,
				tc.want.FirmwareURL:  testBFBURL,
				tc.want.Flavor:       testFlavor,
				tc.want.FirmwareName: testFirmwareName,
			}
			for key, want := range mine {
				if ann[key] != want {
					t.Errorf("%s=%q, want %q", key, ann[key], want)
				}
			}
			for _, k := range allKeys {
				if _, isMine := mine[k]; !isMine && ann[k] != "" {
					t.Errorf("stamped another vendor's key %s=%q", k, ann[k])
				}
			}
		})
	}
}

func TestAnnotatorRejectsUnsupportedVendor(t *testing.T) {
	scheme := newDPUScheme(t)
	dpu := newDPU(testDPUName, testNode)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

	a := &Annotator{
		Client: c,
		Scheme: scheme,
		Enumerator: MockEnumerator{Devices: []Device{{
			PCIAddress:   testPCI,
			VendorID:     0x8086, // Intel: enumerable by nobody here
			SerialNumber: testSerial,
		}}},
		NodeName: testNode,
	}

	_, err := a.Reconcile(context.Background(), requestFor(dpu))
	if err == nil {
		t.Fatal("expected an error rather than a silent NVIDIA-keyed annotation")
	}
	if !strings.Contains(err.Error(), "0x8086") {
		t.Errorf("error should name the vendor, got: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(DataProcessingUnitGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: testDPUName}, got); err != nil {
		t.Fatal(err)
	}
	if len(got.GetAnnotations()) != 0 {
		t.Errorf("nothing should have been stamped, got %v", got.GetAnnotations())
	}
}

// The annotator and the mapping documents must agree on every key, in both
// directions: a mapping must read all four of its vendor's keys, and must not
// read any other vendor's. This is the invariant that broke twice -- first the
// serial key, then the firmware URL -- each time silently, because the mapping
// simply saw an absent annotation.
func TestMappingYAMLsMatchVendorAnnotationKeys(t *testing.T) {
	cases := []struct {
		vendor   string
		vendorID uint16
		file     string
	}{
		{VendorNVIDIA, NVIDIAVendorID, "dataprocessingunit.yaml"},
		{VendorAMD, AMDVendorID, "amd-dsc200.yaml"},
		// Marvell has no mapping document: it integrates over VSP gRPC, not
		// CRs. Its reserved keys are asserted unused below instead.
	}

	for _, tc := range cases {
		t.Run(tc.vendor, func(t *testing.T) {
			body := readMapping(t, tc.file)
			keys, ok := AnnotationsFor(tc.vendorID)
			if !ok {
				t.Fatalf("no annotation keys for vendor %#04x", tc.vendorID)
			}
			for _, k := range []string{keys.Serial, keys.FirmwareURL, keys.Flavor, keys.FirmwareName} {
				if !strings.Contains(body, k) {
					t.Errorf("%s does not read %s", tc.file, k)
				}
			}
			for otherID, other := range annotationsByVendor {
				if otherID == tc.vendorID {
					continue
				}
				for _, k := range []string{other.Serial, other.FirmwareURL, other.Flavor, other.FirmwareName} {
					if strings.Contains(body, k) {
						t.Errorf("%s reads another vendor's key %s", tc.file, k)
					}
				}
			}
		})
	}
}

// Marvell's keys are reserved, not wired. If a Marvell mapping ever lands,
// this test fails and whoever adds it moves Marvell into the table above.
func TestMarvellKeysAreStillUnused(t *testing.T) {
	keys, _ := AnnotationsFor(MarvellVendorID)
	for _, f := range []string{"dataprocessingunit.yaml", "amd-dsc200.yaml", "servicefunctionchain.yaml"} {
		body := readMapping(t, f)
		for _, k := range []string{keys.Serial, keys.FirmwareURL, keys.Flavor, keys.FirmwareName} {
			if strings.Contains(body, k) {
				t.Errorf("%s reads reserved Marvell key %s; add Marvell to "+
					"TestMappingYAMLsMatchVendorAnnotationKeys", f, k)
			}
		}
	}
}

func TestParseVendor(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    uint16
		wantErr bool
	}{
		{VendorNVIDIA, NVIDIAVendorID, false},
		{"NVIDIA", NVIDIAVendorID, false},
		{VendorAMD, AMDVendorID, false},
		{"pensando", AMDVendorID, false},
		{VendorMarvell, MarvellVendorID, false},
		{"0x1dd8", AMDVendorID, false},
		{"1dd8", AMDVendorID, false},
		{"0x8086", 0, true}, // valid hex, unsupported vendor
		{"intel", 0, true},
		{"", 0, true},
	} {
		got, err := ParseVendor(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseVendor(%q) = %#04x, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseVendor(%q): %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("ParseVendor(%q) = %#04x, want %#04x", tc.in, got, tc.want)
		}
	}
}

// Mock mode must produce a device the annotator can key off, so a laptop can
// rehearse the AMD path with no card on the bus.
func TestStaticDeviceCarriesVendorAndDefaultProduct(t *testing.T) {
	for _, tc := range []struct {
		vendorID    uint16
		wantProduct string
	}{
		{NVIDIAVendorID, productBlueField3},
		{AMDVendorID, productPensandoDSC},
		{MarvellVendorID, productMarvellDPU},
	} {
		d := StaticDevice(tc.vendorID, testSerial, testPCI, "")
		if d.VendorID != tc.vendorID {
			t.Errorf("vendor=%#04x, want %#04x", d.VendorID, tc.vendorID)
		}
		if d.ProductName != tc.wantProduct {
			t.Errorf("product=%q, want %q", d.ProductName, tc.wantProduct)
		}
		if _, ok := AnnotationsFor(d.VendorID); !ok {
			t.Errorf("mock device for %#04x has no annotation keys", tc.vendorID)
		}
	}
	if d := StaticDevice(AMDVendorID, testSerial, testPCI, "Custom"); d.ProductName != "Custom" {
		t.Errorf("explicit product should win, got %q", d.ProductName)
	}
}

func readMapping(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "mappings", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func newDPUScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(DataProcessingUnitGVK, &unstructured.Unstructured{})
	listGVK := DataProcessingUnitGVK.GroupVersion().WithKind("DataProcessingUnitList")
	s.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	metav1.AddToGroupVersion(s, DataProcessingUnitGVK.GroupVersion())
	return s
}

func newDPU(name, node string) *unstructured.Unstructured {
	dpu := &unstructured.Unstructured{}
	dpu.SetGroupVersionKind(DataProcessingUnitGVK)
	dpu.SetName(name)
	_ = unstructured.SetNestedField(dpu.Object, node, "spec", "nodeName")
	_ = unstructured.SetNestedField(dpu.Object, testProduct, "spec", "dpuProductName")
	_ = unstructured.SetNestedField(dpu.Object, false, "spec", "isDpuSide")
	return dpu
}

func requestFor(obj *unstructured.Unstructured) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}}
}
