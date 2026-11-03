package discovery

import (
	"context"
	"reflect"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestObservedProductTracksCardReplacementAndRemoval(t *testing.T) {
	const productKey = "discovery.opiproject.org/product-name"
	a := &Annotator{}
	have := map[string]string{"example.test/keep": "yes"}
	for _, model := range []string{"BlueField-3", "BlueField-2"} {
		device := StaticDevice(NVIDIAVendorID, "MTEXAMPLE0002", "0000:03:00.0", model)
		got, err := a.desiredAnnotations(have, Assignment{Bound: true, Device: device})
		if err != nil {
			t.Fatal(err)
		}
		if got[productKey] != model {
			t.Fatalf("observed product = %q, want current card %q", got[productKey], model)
		}
		have = got
	}
	got, err := a.desiredAnnotations(have, Assignment{Clear: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := got[productKey]; exists {
		t.Fatal("removed card retained its product identity")
	}
	if got["example.test/keep"] != "yes" {
		t.Fatal("inventory cleanup removed unrelated annotations")
	}
}

func TestPCIProfileExcludesOtherVendorsBeforeReadingTheirSerial(t *testing.T) {
	root := t.TempDir()
	writePCIDevice(t, root, "0000:03:00.0", NVIDIAVendorID, 0xa2d6, map[string][]byte{"serial": []byte("MTEXAMPLE0002")})
	// A broken serial on an excluded vendor must not take NVIDIA inventory down.
	writePCIDevice(t, root, "0000:04:00.0", AMDVendorID, 0x1002, nil)
	writePCIDevice(t, root, "0000:05:00.0", MarvellVendorID, 0xb900, map[string][]byte{"serial": []byte("MRVEXAMPLE0001")})
	got, err := (PCIEnumerator{SysfsRoot: root, VendorID: NVIDIAVendorID}).Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SerialNumber != "MTEXAMPLE0002" || got[0].ProductName != "BlueField-2" {
		t.Fatalf("NVIDIA inventory must retain BF2 and exclude other vendors: %v", got)
	}
	if _, err := (PCIEnumerator{SysfsRoot: root}).Enumerate(); err == nil {
		t.Fatal("unfiltered inventory must still report the AMD serial error")
	}
}

func TestVendorProfileDoesNotClaimOrClearForeignOPISources(t *testing.T) {
	scheme := newDPUScheme(t)
	nvidia := newDPU("z-nvidia", testNode)
	amd := newDPU("a-amd", testNode)
	amd.Object["spec"].(map[string]any)["dpuProductName"] = "Pensando DSC"
	amd.SetAnnotations(map[string]string{
		AMDSerialNumberAnnotation:               "DSCEXAMPLE0001",
		"discovery.opiproject.org/product-name": "Pensando DSC",
	})
	want := amd.GetAnnotations()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nvidia, amd).Build()
	a := &Annotator{
		Client: c, Scheme: scheme, Enumerator: mockDevices(), NodeName: testNode,
		VendorID: NVIDIAVendorID, FirmwareURL: testBFBURL,
	}
	for _, src := range []string{"a-amd", "z-nvidia"} {
		if _, err := a.Reconcile(context.Background(), requestFor(getDPU(t, c, src))); err != nil {
			t.Fatal(err)
		}
	}
	if got := getDPU(t, c, "a-amd").GetAnnotations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("NVIDIA profile changed a foreign source: %v", got)
	}
	if got := getDPU(t, c, "z-nvidia").GetAnnotations(); got[SerialNumberAnnotation] != testSerial {
		t.Fatalf("NVIDIA inventory was assigned to the wrong OPI source: %v", got)
	}
}

func TestRemovedProductOnlyIdentityIsCleared(t *testing.T) {
	assignments := AssignDevices([]DPUView{{
		Name: "gone", Annotations: map[string]string{"discovery.opiproject.org/product-name": "BlueField-3"},
	}}, nil)
	if !assignments["gone"].Clear {
		t.Fatal("a stale product stamp must be cleared even without serial/PCI")
	}
}

func TestVendorProfileAcceptsCanonicalDSCModel(t *testing.T) {
	src := newDPU("amd", testNode)
	src.Object["spec"].(map[string]any)["dpuProductName"] = "DSC2-100"
	if !(&Annotator{VendorID: AMDVendorID}).ownsSource(src) {
		t.Fatal("AMD inventory profile rejected the DSC model accepted by its mapping")
	}
	if (&Annotator{VendorID: NVIDIAVendorID}).ownsSource(src) {
		t.Fatal("NVIDIA profile claimed an AMD source")
	}
}
