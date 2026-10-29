package mapping

import (
	"reflect"
	"testing"
)

func TestDPF264RejectsIneligibleHardwareDespiteReadyChildren(t *testing.T) {
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, declared, observed, reason string
	}{
		{"bf2", "BlueField-2", "BlueField-2", "UnsupportedHardware"},
		{"misdeclared-bf2", "BlueField-3", "BlueField-2", "UnsupportedHardware"},
		{"unknown-model", "BlueField", "BlueField", "UnsupportedHardware"},
		{"missing-observation", "BlueField-3", "", "HardwareIdentityMissing"},
		{"missing-declaration", "", "BlueField-3", "HardwareIdentityMismatch"},
		{"conflicting-declaration", "BlueField-2", "BlueField-3", "HardwareIdentityMismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := blueFieldSource(map[string]any{"discovery.opiproject.org/product-name": tc.observed})
			src["spec"].(map[string]any)["dpuProductName"] = tc.declared
			ready := map[string]any{"type": "Ready", "status": "True", "reason": "DeviceHealthy"}
			src["status"] = map[string]any{"conditions": []any{ready}}
			objects, err := Apply(spec, src)
			if err != nil {
				t.Fatal(err)
			}
			if len(objects) != 0 {
				t.Errorf("ineligible hardware emitted %d provisioning objects", len(objects))
			}
			status, err := ApplyStatus(spec, src, []any{map[string]any{
				"kind": "DPU", "status": map[string]any{"phase": "Ready"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"DPFReady", "DPFHardwareCompatible"} {
				condition := conditionByType(t, status, name)
				if condition["status"] != "False" || condition["reason"] != tc.reason {
					t.Errorf("%s = %v, want False/%s", name, condition, tc.reason)
				}
			}
			if !reflect.DeepEqual(conditionByType(t, status, "Ready"), ready) {
				t.Fatal("compatibility changed the daemon-owned inventory condition")
			}
		})
	}
}

func TestDPF264AcceptsObservedBF3WithoutClaimingDataplaneReady(t *testing.T) {
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := blueFieldSource(map[string]any{"discovery.opiproject.org/product-name": "BlueField-3"})
	src["spec"].(map[string]any)["dpuProductName"] = "BlueField-3"
	objects, err := Apply(spec, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 4 {
		t.Fatalf("eligible generation emitted %d objects, want 4", len(objects))
	}
	status, err := ApplyStatus(spec, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := conditionByType(t, status, "DPFHardwareCompatible"); got["status"] != "True" {
		t.Fatalf("eligible generation compatibility: %v", got)
	}
	if got := conditionByType(t, status, "DPFReady"); got["status"] != "False" {
		t.Fatalf("eligible hardware without children must remain unready: %v", got)
	}
}

func TestDPF264ClearsStaleSuccessWhenAllHardwareIdentityIsLost(t *testing.T) {
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := object(map[string]any{"name": "removed-card"}, map[string]any{})
	src["status"] = map[string]any{"conditions": []any{
		map[string]any{"type": "DPFReady", "status": "True", "reason": "AllChildrenReady"},
		map[string]any{"type": "DPFHardwareCompatible", "status": "True", "reason": "SupportedGeneration"},
	}}
	status, err := ApplyStatus(spec, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status == nil {
		t.Fatal("lost hardware identity retained stale success because status reporting was skipped")
	}
	for _, name := range []string{"DPFReady", "DPFHardwareCompatible"} {
		condition := conditionByType(t, status, name)
		if condition["status"] != "False" || condition["reason"] != "HardwareIdentityMissing" {
			t.Fatalf("stale success was not cleared: %v", condition)
		}
	}
}
