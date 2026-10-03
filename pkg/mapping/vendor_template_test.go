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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// templatePath is the vendor scaffold a new vendor team clones.
	templatePath = "../../config/vendor-template/fieldmapping.yaml"
	// newVendorScript copies that template for a named vendor.
	newVendorScript = "../../hack/new-vendor.sh"
)

// TestVendorTemplateTranslates is the anti-rot guard on the scaffold.
//
// A template that only *parses* is worth little: a vendor team cloning it would
// discover at runtime that its CEL does not compile or its required fields
// never resolve. So this runs the real loader and the real engine over it and
// checks the objects that come out. If an engine change breaks the scaffold,
// this fails here rather than in a new vendor's first afternoon.
func TestVendorTemplateTranslates(t *testing.T) {
	spec, err := LoadFile(templatePath)
	if err != nil {
		t.Fatalf("the vendor template does not load: %v", err)
	}

	// A source object shaped the way the template's own comments say the VSP
	// will stamp it.
	src := map[string]any{
		"metadata": map[string]any{
			"name":      "ev100-worker",
			"namespace": "opi",
			"annotations": map[string]any{
				"dpu.example.com/serial-number": "EVEXAMPLE0001",
				"dpu.example.com/firmware-url":  "https://example.invalid/ev/1.0.0.tar",
			},
		},
		"spec": map[string]any{
			"dpuProductName": "ExampleVendor EV100 2p QSFP56 DPU",
			"nodeName":       "worker-1",
			"isDpuSide":      true,
		},
	}

	objs, err := Apply(spec, src)
	if err != nil {
		t.Fatalf("the vendor template does not translate: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("emitted %d objects, want 2 (EVDevice, EVFirmware)", len(objs))
	}

	byKind := map[string]map[string]any{}
	for _, o := range objs {
		byKind[o.GetKind()] = o.Object
		if got := o.GetNamespace(); got != "opi" {
			t.Errorf("%s namespace=%q, want the source's namespace %q", o.GetKind(), got, "opi")
		}
	}

	// Each assertion pins one of the shape mismatches the template exists to
	// demonstrate, so the demonstration cannot quietly stop being true.
	for _, tc := range []struct {
		kind, path string
		want       any
		shows      string
	}{
		{"EVDevice", "spec.serialNumber", "EVEXAMPLE0001", "annotation read"},
		{"EVDevice", "spec.nodeName", "worker-1", "1:1 JSONPath copy"},
		{"EVFirmware", "spec.image.url", "https://example.invalid/ev/1.0.0.tar", "nested destination path"},
		{"EVFirmware", "spec.deviceRef.name", "ev100-worker-device", "object ref, not a name string"},
		{"EVFirmware", "spec.drain.enabled", false, "inverted boolean"},
		{"EVFirmware", "spec.mode", "dpu", "enum derived from a bool"},
	} {
		obj, ok := byKind[tc.kind]
		if !ok {
			t.Errorf("no %s emitted", tc.kind)
			continue
		}
		got := nested(obj, splitPath(tc.path)...)
		if got != tc.want {
			t.Errorf("%s %s = %#v, want %#v (%s)", tc.kind, tc.path, got, tc.want, tc.shows)
		}
	}
}

// TestVendorTemplateGuardRejectsOtherVendors proves the template's `when`
// guards actually isolate it. A scaffold whose guard matched everything would
// teach every vendor that clones it to claim every card.
func TestVendorTemplateGuardRejectsOtherVendors(t *testing.T) {
	spec, err := LoadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, product := range []string{
		"BlueField-3",
		"Pensando DSC2-100 100G 2p QSFP56 DPU",
		"",
	} {
		t.Run(product, func(t *testing.T) {
			src := map[string]any{
				"metadata": map[string]any{"name": "other", "namespace": "opi"},
				"spec":     map[string]any{"dpuProductName": product, "nodeName": "n1"},
			}
			objs, err := Apply(spec, src)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(objs) != 0 {
				t.Errorf("template claimed a %q card and emitted %d object(s); its "+
					"vendor guard is too loose", product, len(objs))
			}
		})
	}
}

// TestVendorTemplateMirrorsVendorScopedCondition checks the scaffold teaches the
// single-writer status model: a vendor-scoped condition, never the daemon's.
func TestVendorTemplateMirrorsVendorScopedCondition(t *testing.T) {
	spec, err := LoadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Status == nil || len(spec.Status.Conditions) == 0 {
		t.Fatal("template mirrors no status condition; it should demonstrate the roll-up")
	}
	for _, c := range spec.Status.Conditions {
		if c.Type == ReservedConditionType {
			t.Errorf("template mirrors %q, which the dpu-operator daemon owns", c.Type)
		}
	}

	src := map[string]any{
		"metadata": map[string]any{"name": "ev100-worker", "namespace": "opi"},
		"spec":     map[string]any{"dpuProductName": "EV100", "nodeName": "n1"},
	}
	ready := map[string]any{"status": map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
	}}

	st, err := ApplyStatus(spec, src, []any{ready, ready})
	if err != nil {
		t.Fatalf("the template's status roll-up does not evaluate: %v", err)
	}
	conds, ok := st["conditions"].([]any)
	if !ok || len(conds) == 0 {
		t.Fatalf("conditions=%#v", st["conditions"])
	}
	if got := conds[0].(map[string]any)["status"]; got != "True" {
		t.Errorf("roll-up status=%v, want True when every child reports Ready", got)
	}
}

// splitPath turns "spec.image.url" into the key sequence nested() wants.
func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return append(out, p[start:])
}

// TestNewVendorScriptProducesWorkingMapping runs hack/new-vendor.sh into a
// temporary directory and puts its output through the real loader and engine.
//
// The generator is a thin substitution over a tested template, which is exactly
// why it needs this: a token the script fails to replace leaves a mapping that
// looks right and claims the wrong hardware. Two such bugs existed before this
// test did -- a leftover Kind, and a vendor guard whose two alternatives
// collapsed into the same word.
func TestNewVendorScriptProducesWorkingMapping(t *testing.T) {
	if _, err := os.Stat(newVendorScript); err != nil {
		t.Skipf("generator not present: %v", err)
	}
	sh, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}

	out := t.TempDir()
	cmd := exec.Command(sh, newVendorScript,
		"VENDOR=acme", "GROUP=dpu.acme.com", "MODEL=A100", "PREFIX=ACME", "OUT="+out)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("new-vendor.sh failed: %v\n%s", err, combined)
	}

	generated := filepath.Join(out, "config", "mappings", "acme.yaml")
	spec, err := LoadFile(generated)
	if err != nil {
		t.Fatalf("generated mapping does not load: %v", err)
	}

	// No template token may survive substitution.
	raw, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{
		"ExampleVendor", "examplevendor", "EV100", "EVDevice", "EVFirmware",
		"EVReady", "example.com", "ev-firmware",
	} {
		if strings.Contains(string(raw), tok) {
			t.Errorf("generated mapping still contains the template token %q", tok)
		}
	}

	// The modeline must survive, and must point at the schema relative to
	// config/mappings -- the template's own path is one directory further out,
	// so a naive copy would leave a link that resolves nowhere.
	if !strings.Contains(string(raw), "$schema=./fieldmapping.schema.json") {
		t.Error("generated mapping has no editor schema modeline, or it points " +
			"at the template's relative path instead of config/mappings")
	}

	// The guard must be two distinct alternatives, not the same word twice:
	// matches('ACME|ACME') is the bug this pins.
	for _, e := range spec.Emit {
		if strings.Contains(e.When, "'ACME|ACME'") {
			t.Errorf("vendor guard collapsed to a single repeated alternative: %s", e.When)
		}
	}

	if spec.Status == nil || len(spec.Status.Conditions) != 1 ||
		spec.Status.Conditions[0].Type != "ACMEReady" {
		t.Errorf("status condition = %#v, want one ACMEReady", spec.Status)
	}

	// And it has to actually translate a card of the model it was generated for.
	objs, err := Apply(spec, map[string]any{
		"metadata": map[string]any{
			"name":      "acme-worker",
			"namespace": "opi",
			"annotations": map[string]any{
				"dpu.acme.com/serial-number": "ACMEEXAMPLE1",
				"dpu.acme.com/firmware-url":  "https://example.invalid/acme/1.0.0.tar",
			},
		},
		"spec": map[string]any{
			"dpuProductName": "Acme A100 2p QSFP56 DPU",
			"nodeName":       "worker-1",
		},
	})
	if err != nil {
		t.Fatalf("generated mapping does not translate: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("generated mapping emitted %d objects, want 2", len(objs))
	}
	for _, o := range objs {
		if g := o.GroupVersionKind().Group; g != "dpu.acme.com" {
			t.Errorf("emitted %s in group %q, want dpu.acme.com", o.GetKind(), g)
		}
	}
}
