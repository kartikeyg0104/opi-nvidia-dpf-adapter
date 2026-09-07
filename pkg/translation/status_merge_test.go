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
)

func condition(status string) map[string]any {
	return map[string]any{"type": "Ready", "status": status, "reason": "R", "message": "m"}
}

func firstCondition(t *testing.T, src *unstructured.Unstructured) map[string]any {
	t.Helper()
	conds, ok, _ := unstructured.NestedSlice(src.Object, "status", "conditions")
	if !ok || len(conds) == 0 {
		t.Fatalf("no status.conditions on source: %#v", src.Object["status"])
	}
	m, _ := conds[0].(map[string]any)
	return m
}

func TestApplyStatusSetsAndStampsTransitionTime(t *testing.T) {
	src := &unstructured.Unstructured{Object: map[string]any{}}
	desired := map[string]any{"emittedObjects": int64(2), "conditions": []any{condition("True")}}

	if !applyStatus(src, desired) {
		t.Fatal("expected first applyStatus to report a change")
	}
	if got, _, _ := unstructured.NestedInt64(src.Object, "status", "emittedObjects"); got != 2 {
		t.Errorf("emittedObjects=%d, want 2", got)
	}
	c := firstCondition(t, src)
	if c["status"] != "True" {
		t.Errorf("condition status=%v, want True", c["status"])
	}
	ltt, ok := c["lastTransitionTime"].(string)
	if !ok || ltt == "" {
		t.Fatalf("condition missing lastTransitionTime: %#v", c)
	}

	// Idempotent re-apply: no change, transition time preserved.
	if applyStatus(src, map[string]any{"emittedObjects": int64(2), "conditions": []any{condition("True")}}) {
		t.Error("expected no change on identical re-apply")
	}
	if got := firstCondition(t, src)["lastTransitionTime"]; got != ltt {
		t.Errorf("lastTransitionTime changed on no-op: %v != %v", got, ltt)
	}

	// Status flip: change reported, condition now False.
	if !applyStatus(src, map[string]any{"emittedObjects": int64(2), "conditions": []any{condition("False")}}) {
		t.Error("expected change when condition status flips")
	}
	if firstCondition(t, src)["status"] != "False" {
		t.Errorf("condition did not flip to False")
	}
}
