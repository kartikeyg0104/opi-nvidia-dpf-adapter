/*
Copyright 2026 Kartikey Gupta.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

    10|Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mapping

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
)

func blueFieldSource(annotations map[string]any) map[string]any {
	meta := map[string]any{
		"name": "bf3-worker",
		"annotations": map[string]any{
			"provisioning.dpu.nvidia.com/serial-number": "MTEXAMPLE0001",
			"provisioning.dpu.nvidia.com/pci-address":   "0000-0f-00",
			"dpu.nvidia.com/bfb-url":                    "https://example.invalid/bf.bfb",
			"discovery.opiproject.org/product-name":     "BlueField-3",
		},
	}
	maps.Copy(meta["annotations"].(map[string]any), annotations)
	return object(meta, map[string]any{
		"dpuProductName": "BlueField-3",
		"nodeName":       "dh2",
	})
}

func appliedFlavorNvconfig(t *testing.T, src map[string]any) string {
	t.Helper()
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Apply(spec, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetKind() == "DPUFlavor" {
			return fmt.Sprint(nested(o.Object, "spec", "nvconfig"))
		}
	}
	t.Fatal("no DPUFlavor emitted")
	return ""
}

func TestApplyBlocksDPUWithoutPCIAddress(t *testing.T) {
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := blueFieldSource(nil)
	anns := src["metadata"].(map[string]any)["annotations"].(map[string]any)
	delete(anns, "provisioning.dpu.nvidia.com/pci-address")
	_, err = Apply(spec, src)
	if err == nil || !strings.Contains(err.Error(), "pciAddress") {
		t.Fatalf("missing PCI identity must block DPU emit, got %v", err)
	}
}

func TestApplyFlavorNvconfigMatchesCurrentSFAnnotations(t *testing.T) {
	// dh2 hostagent queued next_boot=0 because the flavor was empty.
	// Matching current firmware (PF_TOTAL_SF=32, PF_SF_BAR_SIZE=8) is the
	// DPF-API way to drop reset_needed without mlxfwreset.
	got := appliedFlavorNvconfig(t, blueFieldSource(map[string]any{
		"dpu.nvidia.com/pf-total-sf":    "32",
		"dpu.nvidia.com/pf-sf-bar-size": "8",
	}))
	if !strings.Contains(got, "PF_TOTAL_SF=32") || !strings.Contains(got, "PF_SF_BAR_SIZE=8") {
		t.Fatalf("flavor nvconfig must honor current-SF annotations, got %s", got)
	}
	if strings.Contains(got, "PF_TOTAL_SF=0") || strings.Contains(got, "PF_SF_BAR_SIZE=0") {
		t.Fatalf("zero SF nvconfig is the reboot stall, got %s", got)
	}
}

func TestApplyFlavorNvconfigRejectsZeroSFAnnotation(t *testing.T) {
	got := appliedFlavorNvconfig(t, blueFieldSource(map[string]any{
		"dpu.nvidia.com/pf-total-sf":    "0",
		"dpu.nvidia.com/pf-sf-bar-size": "0",
	}))
	if !strings.Contains(got, "PF_TOTAL_SF=20") || !strings.Contains(got, "PF_SF_BAR_SIZE=10") {
		t.Fatalf("zero annotation must fall back to official 26.4.0 HBN, got %s", got)
	}
}

func TestApplyFlavorNvconfigMatchesRunningFirmware(t *testing.T) {
	// dh2 host agent, 2026-10-25: next_boot still differs from current on
	// these parameters, so reset_needed stays true after the SF fix.
	// Matching current drops the host-reboot request. Official defaults
	// stay when the annotations are absent.
	got := appliedFlavorNvconfig(t, blueFieldSource(map[string]any{
		"dpu.nvidia.com/pf-bar2-enable":          "1",
		"dpu.nvidia.com/per-pf-num-sf":           "0",
		"dpu.nvidia.com/pf-total-sf":             "32",
		"dpu.nvidia.com/pf-sf-bar-size":          "8",
		"dpu.nvidia.com/num-pf-msix-valid":       "1",
		"dpu.nvidia.com/pf-num-pf-msix-valid":    "0",
		"dpu.nvidia.com/pf-num-pf-msix":          "63",
		"dpu.nvidia.com/num-of-vfs":              "8",
		"dpu.nvidia.com/lag-resource-allocation": "0",
	}))
	for _, want := range []string{
		"PF_BAR2_ENABLE=1",
		"PER_PF_NUM_SF=0",
		"PF_TOTAL_SF=32",
		"PF_SF_BAR_SIZE=8",
		"NUM_PF_MSIX_VALID=1",
		"PF_NUM_PF_MSIX_VALID=0",
		"PF_NUM_PF_MSIX=63",
		"NUM_OF_VFS=8",
		"LAG_RESOURCE_ALLOCATION=0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("running firmware nvconfig missing %s: %s", want, got)
		}
	}
	for _, stale := range []string{"NUM_OF_VFS=46", "PF_NUM_PF_MSIX=228", "LAG_RESOURCE_ALLOCATION=1"} {
		if strings.Contains(got, stale) {
			t.Errorf("flavor still requests a host reset via %s: %s", stale, got)
		}
	}
}

func TestDefaultFlavorPinsOfficialDPF264Nvconfig(t *testing.T) {
	got := appliedFlavorNvconfig(t, blueFieldSource(nil))
	for _, want := range []string{"PF_TOTAL_SF=20", "PF_SF_BAR_SIZE=10", "PF_BAR2_ENABLE=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("default nvconfig missing official 26.4.0 %s: %s", want, got)
		}
	}
}

func TestDPFReadyReasonRebootingWhenDPUPhaseRebooting(t *testing.T) {
	// dh2 DPU stays phase=Rebooting until the host reset DPF asked for.
	// AllChildrenReady as the False reason looks like a mapping bug.
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := object(map[string]any{
		"name":        "bf3-worker",
		"annotations": map[string]any{"discovery.opiproject.org/product-name": "BlueField-3"},
	}, map[string]any{
		"dpuProductName": "BlueField-3",
		"nodeName":       "dh2",
	})
	dpu := map[string]any{
		"kind":   "DPU",
		"status": map[string]any{"phase": "Rebooting"},
	}
	bfb := map[string]any{"kind": "BFB", "status": map[string]any{"phase": "Ready"}}
	st, err := ApplyStatus(spec, src, []any{dpu, bfb, map[string]any{"kind": "DPUFlavor"}})
	if err != nil {
		t.Fatal(err)
	}
	got := conditionByType(t, st, "DPFReady")
	if got["status"] != "False" {
		t.Fatalf("DPFReady=%v, want False while DPU phase is Rebooting", got["status"])
	}
	if got["reason"] != "Rebooting" {
		t.Fatalf("DPFReady reason=%q, want Rebooting (not AllChildrenReady)", got["reason"])
	}
	msg, _ := got["message"].(string)
	if !strings.Contains(msg, "Rebooting") {
		t.Fatalf("DPFReady message=%q, want it to name Rebooting", msg)
	}
}

func TestNVIDIANotReadyConditionsDescribePendingChildren(t *testing.T) {
	for _, tc := range []struct {
		name, file, reason string
		children           []any
	}{
		{"provisioning", "dataprocessingunit.yaml", "ChildrenNotReady", []any{
			map[string]any{"kind": "DPU", "status": map[string]any{"phase": "Initializing"}},
		}},
		{"services", "servicefunctionchain.yaml", "ServicesNotReady", []any{
			map[string]any{"kind": "DPUService", "status": map[string]any{"conditions": []any{
				map[string]any{"type": "Ready", "status": "False"},
			}}},
		}},
		{"unobserved-services", "servicefunctionchain.yaml", "ServicesNotReady", []any{
			map[string]any{"kind": "DPUService"},
		}},
		{"unobserved-device", "dataprocessingunit.yaml", "ChildrenNotReady", []any{
			map[string]any{"kind": "DPUDevice"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := LoadFile(mappingDir + "/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			status, err := ApplyStatus(spec, blueFieldSource(nil), tc.children)
			if err != nil {
				t.Fatal(err)
			}
			got := conditionByType(t, status, "DPFReady")
			if got["status"] != "False" || got["reason"] != tc.reason {
				t.Fatalf("pending children must not report a success reason: %v", got)
			}
		})
	}
}

func TestDPFReadyFalseWhileDPUInitializing(t *testing.T) {
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := object(map[string]any{
		"name":        "bf3-worker",
		"annotations": map[string]any{"discovery.opiproject.org/product-name": "BlueField-3"},
	}, map[string]any{
		"dpuProductName": "BlueField-3",
		"nodeName":       "dh2",
	})
	children := []any{
		map[string]any{"kind": "DPUDevice"},
		map[string]any{"kind": "DPUFlavor"},
		map[string]any{"status": map[string]any{"phase": "Initializing"}},
		map[string]any{"status": map[string]any{"phase": "Initializing"}},
	}
	st, err := ApplyStatus(spec, src, children)
	if err != nil {
		t.Fatal(err)
	}
	if got := dpfReadyStatus(t, st); got != "False" {
		t.Fatalf("DPFReady=%s, want False while DPU/BFB phase is Initializing", got)
	}
}

func TestDPFReadyFalseWhileOSInstalling(t *testing.T) {
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := object(map[string]any{
		"name":        "bf3-worker",
		"annotations": map[string]any{"discovery.opiproject.org/product-name": "BlueField-3"},
	}, map[string]any{
		"dpuProductName": "BlueField-3",
		"nodeName":       "dh2",
	})
	dpu := map[string]any{
		"kind": "DPU",
		"status": map[string]any{
			"phase": "OS Installing",
			"conditions": []any{
				map[string]any{"type": "OSInstalled", "status": "False"},
				map[string]any{"type": "FWConfigured", "status": "True"},
			},
		},
	}
	st, err := ApplyStatus(spec, src, []any{dpu, map[string]any{"kind": "DPUFlavor"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := dpfReadyStatus(t, st); got != "False" {
		t.Fatalf("DPFReady=%s, want False while DPU phase is OS Installing", got)
	}
}

func TestDPFReadyTrueWhenDPUPhaseReadyWithoutReadyCondition(t *testing.T) {
	// Live NVIDIA DPU objects publish BFBPrepared/OSInstalled/… and status.phase,
	// not type=Ready. Requiring Ready made DPFReady stay False after provision.
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := object(map[string]any{
		"name":        "bf3-worker",
		"annotations": map[string]any{"discovery.opiproject.org/product-name": "BlueField-3"},
	}, map[string]any{
		"dpuProductName": "BlueField-3",
		"nodeName":       "dh2",
	})
	dpu := map[string]any{
		"kind": "DPU",
		"status": map[string]any{
			"phase": "Ready",
			"conditions": []any{
				map[string]any{"type": "OSInstalled", "status": "True"},
				map[string]any{"type": "FWConfigured", "status": "True"},
			},
		},
	}
	bfb := map[string]any{"kind": "BFB", "status": map[string]any{"phase": "Ready"}}
	st, err := ApplyStatus(spec, src, []any{dpu, bfb, map[string]any{"kind": "DPUFlavor"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := dpfReadyStatus(t, st); got != "True" {
		t.Fatalf("DPFReady=%s, want True when DPU phase is Ready", got)
	}
}

func TestDPFReadyTrueWhenReadyConditionSet(t *testing.T) {
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := object(map[string]any{
		"name":        "bf3-worker",
		"annotations": map[string]any{"discovery.opiproject.org/product-name": "BlueField-3"},
	}, map[string]any{
		"dpuProductName": "BlueField-3",
		"nodeName":       "dh2",
	})
	ready := map[string]any{
		"status": map[string]any{
			"phase": "Initializing",
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True"},
			},
		},
	}
	st, err := ApplyStatus(spec, src, []any{ready, map[string]any{"kind": "DPUFlavor"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := dpfReadyStatus(t, st); got != "True" {
		t.Fatalf("DPFReady=%s, want True once a child reports Ready", got)
	}
}

func TestApplyStatusKeepsDaemonReadyCondition(t *testing.T) {
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// The OPI CRD requires these fields on every condition and does not
	// preserve unknown ones. The daemon writes them on Ready; a DPF status
	// update has to copy that object through, not replace the list.
	ready := map[string]any{
		"type":               ReservedConditionType,
		"status":             "True",
		"reason":             "DeviceHealthy",
		"message":            "BlueField link up",
		"lastTransitionTime": "2026-10-25T05:37:47Z",
		"observedGeneration": int64(4),
	}
	src := object(map[string]any{
		"name":        "bf3-worker",
		"annotations": map[string]any{"discovery.opiproject.org/product-name": "BlueField-3"},
	}, map[string]any{
		"dpuProductName": "BlueField-3",
		"nodeName":       "dh2",
	})
	src["status"] = map[string]any{"conditions": []any{ready}}

	dpu := map[string]any{
		"kind": "DPU",
		"status": map[string]any{
			"phase": "Ready",
			"conditions": []any{
				map[string]any{"type": "OSInstalled", "status": "True"},
			},
		},
	}
	st, err := ApplyStatus(spec, src, []any{dpu, map[string]any{"kind": "DPUFlavor"}})
	if err != nil {
		t.Fatal(err)
	}
	gotReady := conditionByType(t, st, ReservedConditionType)
	if !reflect.DeepEqual(gotReady, ready) {
		t.Fatalf("Ready after DPF status update = %#v, want %#v", gotReady, ready)
	}
	if got := conditionByType(t, st, "DPFReady")["status"]; got != "True" {
		t.Fatalf("DPFReady=%v, want True", got)
	}
	conds := st["conditions"].([]any)
	if len(conds) != 3 {
		t.Fatalf("conditions=%d, want Ready preserved, DPFReady and compatibility added", len(conds))
	}
}

func conditionByType(t *testing.T, st map[string]any, condType string) map[string]any {
	t.Helper()
	conds, ok := st["conditions"].([]any)
	if !ok {
		t.Fatalf("conditions=%#v", st["conditions"])
	}
	for _, raw := range conds {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == condType {
			return m
		}
	}
	t.Fatalf("no %s condition in %#v", condType, conds)
	return nil
}

func dpfReadyStatus(t *testing.T, st map[string]any) string {
	t.Helper()
	conds, ok := st["conditions"].([]any)
	if !ok || len(conds) == 0 {
		t.Fatalf("conditions=%v", st["conditions"])
	}
	c0, ok := conds[0].(map[string]any)
	if !ok {
		t.Fatalf("condition=%T", conds[0])
	}
	s, _ := c0["status"].(string)
	return s
}
