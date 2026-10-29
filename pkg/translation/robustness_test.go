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

package translation

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/lifecycle"
	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

// typesOf returns the "type" value of every condition in a merged slice.
func typesOf(conds []any) []string {
	out := make([]string, 0, len(conds))
	for _, c := range conds {
		m, _ := c.(map[string]any)
		t, _ := m["type"].(string)
		out = append(out, t)
	}
	return out
}

// --- mergeConditions robustness: malformed mapping output must not be mirrored ---

func TestMergeConditionsSkipsEmptyType(t *testing.T) {
	desired := []any{
		map[string]any{"type": "", "status": "True"}, // no type -> drop
		map[string]any{"status": "True"},             // missing type -> drop
		map[string]any{"type": "DPFReady", "status": "True"},
	}
	merged, changed := mergeConditions(nil, desired)
	if !changed {
		t.Fatal("expected a change for the one valid condition")
	}
	if got := typesOf(merged); len(got) != 1 || got[0] != "DPFReady" {
		t.Fatalf("typeless conditions were written: %v", got)
	}
}

func TestMergeConditionsSkipsMissingOrEmptyStatus(t *testing.T) {
	desired := []any{
		map[string]any{"type": "A"},                    // missing status -> drop
		map[string]any{"type": "B", "status": nil},     // nil status -> drop
		map[string]any{"type": "C", "status": ""},      // empty status -> drop
		map[string]any{"type": "D", "status": "False"}, // valid -> keep
	}
	merged, changed := mergeConditions(nil, desired)
	if !changed {
		t.Fatal("expected a change for condition D")
	}
	if got := typesOf(merged); len(got) != 1 || got[0] != "D" {
		t.Fatalf("conditions with no status were written: %v", got)
	}
}

func TestMergeConditionsSkipsNonMapEntries(t *testing.T) {
	desired := []any{"not-a-map", 42, nil, map[string]any{"type": "DPFReady", "status": "True"}}
	merged, changed := mergeConditions(nil, desired)
	if !changed || len(merged) != 1 {
		t.Fatalf("non-map entries leaked into merge: %#v", merged)
	}
}

func TestMergeConditionsNeverWritesDaemonReady(t *testing.T) {
	desired := []any{
		map[string]any{"type": mapping.ReservedConditionType, "status": "True", "reason": "stolen"},
		map[string]any{"type": "DPFReady", "status": "True"},
	}
	merged, _ := mergeConditions(nil, desired)
	for _, got := range typesOf(merged) {
		if got == mapping.ReservedConditionType {
			t.Fatalf("daemon-owned %q was upserted", mapping.ReservedConditionType)
		}
	}
}

func TestMergeConditionsDuplicateTypeLastWins(t *testing.T) {
	desired := []any{
		map[string]any{"type": "DPFReady", "status": "True", "reason": "first"},
		map[string]any{"type": "DPFReady", "status": "False", "reason": "second"},
	}
	merged, changed := mergeConditions(nil, desired)
	if !changed || len(merged) != 1 {
		t.Fatalf("duplicate type not collapsed: %#v", merged)
	}
	m, _ := merged[0].(map[string]any)
	if m["status"] != "False" || m["reason"] != "second" {
		t.Fatalf("last write did not win: %#v", m)
	}
}

func TestMergeConditionsAllMalformedReportsNoChange(t *testing.T) {
	desired := []any{
		map[string]any{"type": "", "status": "True"},
		map[string]any{"type": "X"},
		"junk",
	}
	merged, changed := mergeConditions(nil, desired)
	if changed || len(merged) != 0 {
		t.Fatalf("expected no change and empty merge, got changed=%v merged=%#v", changed, merged)
	}
}

// --- applyStatus edge cases ---

func TestApplyStatusAllMalformedConditionsNoWrite(t *testing.T) {
	src := &unstructured.Unstructured{Object: map[string]any{}}
	desired := map[string]any{"conditions": []any{
		map[string]any{"type": "", "status": "True"},
		map[string]any{"type": "Y"},
	}}
	if applyStatus(src, desired) {
		t.Fatal("expected no change when every desired condition is malformed")
	}
	if _, found, _ := unstructured.NestedSlice(src.Object, "status", "conditions"); found {
		t.Fatal("malformed conditions must not create status.conditions")
	}
}

func TestApplyStatusWritesNonConditionKeys(t *testing.T) {
	src := &unstructured.Unstructured{Object: map[string]any{}}
	if !applyStatus(src, map[string]any{"emittedObjects": int64(3)}) {
		t.Fatal("expected a change for a scalar status key")
	}
	if got, _, _ := unstructured.NestedInt64(src.Object, "status", "emittedObjects"); got != 3 {
		t.Fatalf("emittedObjects=%d, want 3", got)
	}
}

// --- dead_fields.go edge cases ---

func TestDropFieldGuards(t *testing.T) {
	if dropField(nil, "spec.x") {
		t.Error("dropField(nil) should be false")
	}
	if dropField(&unstructured.Unstructured{}, "spec.x") {
		t.Error("dropField on nil Object should be false")
	}
	if dropField(&unstructured.Unstructured{Object: map[string]any{}}, "") {
		t.Error("dropField with empty path should be false")
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"a": 1}}}
	if dropField(obj, "spec.missing") {
		t.Error("dropField of absent path should be false")
	}
	if dropField(obj, "spec.a.deeper") {
		t.Error("dropField descending into a non-map should be false")
	}
}

func TestPreserveUnknownStopsPruning(t *testing.T) {
	m := map[string]any{"unknown": "keep-me"}
	for name, sch := range map[string]map[string]any{
		"x-kubernetes-preserve-unknown-fields": {"x-kubernetes-preserve-unknown-fields": true},
		"additionalProperties":                 {"additionalProperties": map[string]any{"type": "string"}},
	} {
		if !preserveUnknown(sch) {
			t.Errorf("%s: preserveUnknown should be true", name)
		}
		if dropped := pruneMap(m, sch, "spec"); dropped != nil {
			t.Errorf("%s: pruneMap should drop nothing, got %v", name, dropped)
		}
	}
	if _, ok := m["unknown"]; !ok {
		t.Fatal("preserve-unknown map was pruned")
	}
}

func TestStorageOpenAPISelection(t *testing.T) {
	crd := map[string]any{"spec": map[string]any{"versions": []any{
		map[string]any{"name": "v1alpha1", "storage": false},
		map[string]any{"name": "v1", "storage": true, "schema": map[string]any{
			"openAPIV3Schema": map[string]any{"type": "object", "marker": "chosen"},
		}},
	}}}
	got := storageOpenAPI(crd)
	if got == nil || got["marker"] != "chosen" {
		t.Fatalf("did not pick the storage version schema: %#v", got)
	}
	none := map[string]any{"spec": map[string]any{"versions": []any{
		map[string]any{"name": "v1", "storage": false},
	}}}
	if storageOpenAPI(none) != nil {
		t.Fatal("expected nil when no version is the storage version")
	}
}

func TestNormalizeFieldPath(t *testing.T) {
	cases := map[string]string{
		".spec.security":   "spec.security",
		"/spec/foo":        "spec.foo",
		"spec/bar/baz":     "spec.bar.baz",
		"  spec.trimmed  ": "spec.trimmed",
	}
	for in, want := range cases {
		if got := normalizeFieldPath(in); got != want {
			t.Errorf("normalizeFieldPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- readiness.go edge cases ---

func TestUniqueGVKsDedupes(t *testing.T) {
	mk := func(kind string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: "g", Version: "v1", Kind: kind})
		return u
	}
	got := uniqueGVKs([]*unstructured.Unstructured{mk("A"), mk("A"), mk("B"), mk("A")})
	if len(got) != 2 {
		t.Fatalf("expected 2 unique GVKs, got %d: %v", len(got), got)
	}
}

func TestOwnableTargetsNilMapperPassesThrough(t *testing.T) {
	gvks := []schema.GroupVersionKind{{Group: "g", Version: "v1", Kind: "K"}}
	out, err := ownableTargets(nil, gvks)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != gvks[0] {
		t.Fatalf("nil mapper should pass GVKs through, got %v", out)
	}
}

func TestLcmConditionReady(t *testing.T) {
	ready := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"conditions": []any{
		map[string]any{"type": lifecycle.OperatorReadyConditionType, "status": "True"},
	}}}}
	if !lcmConditionReady(ready) {
		t.Fatal("expected ready=true for OperatorReady=True")
	}
	notReady := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"conditions": []any{
		"junk", // non-map entry must be skipped, not panic
		map[string]any{"type": lifecycle.OperatorReadyConditionType, "status": "False"},
	}}}}
	if lcmConditionReady(notReady) {
		t.Fatal("expected ready=false for OperatorReady=False")
	}
	empty := &unstructured.Unstructured{Object: map[string]any{}}
	if lcmConditionReady(empty) {
		t.Fatal("expected ready=false with no conditions")
	}
}
