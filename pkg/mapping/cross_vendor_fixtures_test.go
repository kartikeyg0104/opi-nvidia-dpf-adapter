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

package mapping

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const (
	blueFieldSamplePath = "../../config/samples/dataprocessingunit.yaml"
	amdSamplePath       = "../../config/samples/amd-dsc200-dataprocessingunit.yaml"
	dpfMappingPath      = "../../config/mappings/dataprocessingunit.yaml"
	amdMappingPath      = "../../config/mappings/amd-dsc200.yaml"
)

func loadYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("yaml.Unmarshal(%s): %v", path, err)
	}
	return out
}

// TestBlueFieldAndAMDFixturesDoNotCreateEachOthersChildren asserts that:
// 1. Translating a BlueField fixture with the AMD mapping produces 0 objects.
// 2. Translating an AMD fixture with the BlueField mapping produces 0 objects.
// 3. Translating each fixture with its own mapping produces only its own vendor's objects.
func TestBlueFieldAndAMDFixturesDoNotCreateEachOthersChildren(t *testing.T) {
	dpfSpec, err := LoadFile(dpfMappingPath)
	if err != nil {
		t.Fatalf("LoadFile(%s): %v", dpfMappingPath, err)
	}
	amdSpec, err := LoadFile(amdMappingPath)
	if err != nil {
		t.Fatalf("LoadFile(%s): %v", amdMappingPath, err)
	}

	bfFixture := loadYAMLMap(t, blueFieldSamplePath)
	amdFixture := loadYAMLMap(t, amdSamplePath)

	// An annotated BlueField fixture with required serial and PCI annotations
	// for full emission under the DPF mapping.
	bfAnnotated := cloneMap(bfFixture)
	bfAnnotated["metadata"] = map[string]any{
		"name": "bf3-worker",
		"annotations": map[string]any{
			"provisioning.dpu.nvidia.com/serial-number": "MT1234FIXTURE",
			"discovery.opiproject.org/product-name":     "BlueField-3",
			"provisioning.dpu.nvidia.com/pci-address":   "0000-03-00",
			"dpu.nvidia.com/bfb-url":                    "https://example.invalid/fw.bfb",
		},
	}

	t.Run("AMD mapping against BlueField fixture emits nothing", func(t *testing.T) {
		objs, err := Apply(amdSpec, bfFixture)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(objs) != 0 {
			t.Errorf("AMD mapping emitted %d object(s) for BlueField sample fixture: %v", len(objs), objs)
		}

		objs, err = Apply(amdSpec, bfAnnotated)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(objs) != 0 {
			t.Errorf("AMD mapping emitted %d object(s) for annotated BlueField fixture: %v", len(objs), objs)
		}
	})

	t.Run("DPF mapping against AMD fixture emits nothing", func(t *testing.T) {
		objs, err := Apply(dpfSpec, amdFixture)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(objs) != 0 {
			t.Errorf("DPF mapping emitted %d object(s) for AMD sample fixture: %v", len(objs), objs)
		}
	})

	t.Run("Each mapping against its own fixture emits only its own vendor kinds", func(t *testing.T) {
		bfObjs, err := Apply(dpfSpec, bfAnnotated)
		if err != nil {
			t.Fatalf("Apply(dpf): %v", err)
		}
		if len(bfObjs) == 0 {
			t.Fatal("DPF mapping emitted 0 objects for BlueField fixture")
		}
		for _, o := range bfObjs {
			if strings.Contains(o.GetKind(), "DSC") || strings.Contains(o.GroupVersionKind().Group, "amd") {
				t.Errorf("BlueField fixture emitted AMD kind %s (%s)", o.GetKind(), o.GroupVersionKind().Group)
			}
		}

		amdObjs, err := Apply(amdSpec, amdFixture)
		if err != nil {
			t.Fatalf("Apply(amd): %v", err)
		}
		if len(amdObjs) == 0 {
			t.Fatal("AMD mapping emitted 0 objects for AMD fixture")
		}
		for _, o := range amdObjs {
			if strings.HasPrefix(o.GetKind(), "DPU") || strings.Contains(o.GroupVersionKind().Group, "nvidia") {
				t.Errorf("AMD fixture emitted NVIDIA kind %s (%s)", o.GetKind(), o.GroupVersionKind().Group)
			}
		}
	})
}

// TestBlueFieldAndAMDFixturesDoNotCorruptConditions asserts that cross-vendor
// mappings do not write status or corrupt existing condition blocks on foreign cards.
func TestBlueFieldAndAMDFixturesDoNotCorruptConditions(t *testing.T) {
	dpfSpec, err := LoadFile(dpfMappingPath)
	if err != nil {
		t.Fatalf("LoadFile(%s): %v", dpfMappingPath, err)
	}
	amdSpec, err := LoadFile(amdMappingPath)
	if err != nil {
		t.Fatalf("LoadFile(%s): %v", amdMappingPath, err)
	}

	bfFixture := loadYAMLMap(t, blueFieldSamplePath)
	amdFixture := loadYAMLMap(t, amdSamplePath)

	daemonReady := map[string]any{
		"type":               ReservedConditionType,
		"status":             string(metav1.ConditionTrue),
		"reason":             "DaemonInit",
		"message":            "VSP hardware enumerated",
		"lastTransitionTime": "2026-10-26T00:00:00Z",
		"observedGeneration": int64(1),
	}
	dpfReady := map[string]any{
		"type":               "DPFReady",
		"status":             string(metav1.ConditionTrue),
		"reason":             "AllChildrenReady",
		"message":            "All DPF children ready",
		"lastTransitionTime": "2026-10-26T00:01:00Z",
	}
	dscReady := map[string]any{
		"type":               "DSCReady",
		"status":             string(metav1.ConditionTrue),
		"reason":             "AllChildrenReady",
		"message":            "All AMD children ready",
		"lastTransitionTime": "2026-10-26T00:01:00Z",
	}

	t.Run("AMD mapping skips status writes on BlueField fixture", func(t *testing.T) {
		// Even when passed children, the ownership gate status.when must evaluate false
		// and return nil.
		st, err := ApplyStatus(amdSpec, bfFixture, []any{
			map[string]any{
				"kind":   "DSCDevice",
				"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
			},
		})
		if err != nil {
			t.Fatalf("ApplyStatus: %v", err)
		}
		if st != nil {
			t.Fatalf("AMD mapping wrote status on BlueField fixture: %#v", st)
		}

		// When BlueField fixture already has Ready and DPFReady conditions:
		bfWithConds := cloneMap(bfFixture)
		bfWithConds["status"] = map[string]any{
			"conditions": []any{daemonReady, dpfReady},
		}
		st, err = ApplyStatus(amdSpec, bfWithConds, nil)
		if err != nil {
			t.Fatalf("ApplyStatus: %v", err)
		}
		if st != nil {
			t.Fatalf("AMD mapping wrote status on BlueField fixture with conditions: %#v", st)
		}
	})

	t.Run("DPF mapping skips status writes on AMD fixture", func(t *testing.T) {
		st, err := ApplyStatus(dpfSpec, amdFixture, []any{
			map[string]any{"kind": "DPU", "status": map[string]any{"phase": "Ready"}},
		})
		if err != nil {
			t.Fatalf("ApplyStatus: %v", err)
		}
		if st != nil {
			t.Fatalf("DPF mapping wrote status on AMD fixture: %#v", st)
		}

		// When AMD fixture already has Ready and DSCReady conditions:
		amdWithConds := cloneMap(amdFixture)
		amdWithConds["status"] = map[string]any{
			"conditions": []any{daemonReady, dscReady},
		}
		st, err = ApplyStatus(dpfSpec, amdWithConds, nil)
		if err != nil {
			t.Fatalf("ApplyStatus: %v", err)
		}
		if st != nil {
			t.Fatalf("DPF mapping wrote status on AMD fixture with conditions: %#v", st)
		}
	})

	t.Run("Sequential multi-vendor reconciliation preserves own conditions without corruption", func(t *testing.T) {
		// 1. Start BlueField card with daemon Ready
		bfCard := cloneMap(bfFixture)
		bfCard["metadata"].(map[string]any)["annotations"] = map[string]any{
			"discovery.opiproject.org/product-name": "BlueField-3",
		}
		bfCard["status"] = map[string]any{"conditions": []any{daemonReady}}

		// DPF mapping reconciles its own card:
		dpfChildren := []any{
			map[string]any{"kind": "DPU", "status": map[string]any{"phase": "Ready"}},
			map[string]any{"kind": "DPUFlavor"},
		}
		st, err := ApplyStatus(dpfSpec, bfCard, dpfChildren)
		if err != nil {
			t.Fatalf("DPF ApplyStatus: %v", err)
		}
		if st == nil {
			t.Fatal("DPF mapping returned nil status for BlueField card")
		}
		bfCard["status"] = st

		// Verify Ready preserved and DPFReady added:
		gotReady := conditionByType(t, bfCard["status"].(map[string]any), ReservedConditionType)
		if !reflect.DeepEqual(gotReady, daemonReady) {
			t.Errorf("Ready condition mutated: got %#v, want %#v", gotReady, daemonReady)
		}
		gotDPF := conditionByType(t, bfCard["status"].(map[string]any), "DPFReady")
		if gotDPF["status"] != "True" {
			t.Errorf("DPFReady status = %v, want True", gotDPF["status"])
		}

		// AMD mapping now runs on this BlueField card:
		amdSt, err := ApplyStatus(amdSpec, bfCard, nil)
		if err != nil {
			t.Fatalf("AMD ApplyStatus on BF: %v", err)
		}
		if amdSt != nil {
			t.Fatalf("AMD mapping must return nil for BlueField card, got %#v", amdSt)
		}

		// 2. Start AMD card with daemon Ready
		amdCard := cloneMap(amdFixture)
		amdCard["status"] = map[string]any{"conditions": []any{daemonReady}}

		// AMD mapping reconciles its own card:
		amdChildren := []any{
			map[string]any{
				"kind":   "DSCDevice",
				"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
			},
			map[string]any{"kind": "DSCProfile"},
			map[string]any{
				"kind":   "DSCFirmware",
				"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
			},
			map[string]any{
				"kind":   "DSCNodePolicy",
				"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
			},
		}
		st, err = ApplyStatus(amdSpec, amdCard, amdChildren)
		if err != nil {
			t.Fatalf("AMD ApplyStatus: %v", err)
		}
		if st == nil {
			t.Fatal("AMD mapping returned nil status for AMD card")
		}
		amdCard["status"] = st

		// Verify Ready preserved and DSCReady added:
		gotReady = conditionByType(t, amdCard["status"].(map[string]any), ReservedConditionType)
		if !reflect.DeepEqual(gotReady, daemonReady) {
			t.Errorf("Ready condition mutated: got %#v, want %#v", gotReady, daemonReady)
		}
		gotDSC := conditionByType(t, amdCard["status"].(map[string]any), "DSCReady")
		if gotDSC["status"] != "True" {
			t.Errorf("DSCReady status = %v, want True", gotDSC["status"])
		}

		// DPF mapping now runs on this AMD card:
		dpfSt, err := ApplyStatus(dpfSpec, amdCard, nil)
		if err != nil {
			t.Fatalf("DPF ApplyStatus on AMD: %v", err)
		}
		if dpfSt != nil {
			t.Fatalf("DPF mapping must return nil for AMD card, got %#v", dpfSt)
		}
	})
}
