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

package lifecycle

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func lcmScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	s.AddKnownTypeWithName(DpuOperatorConfigGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		DpuOperatorConfigGVK.GroupVersion().WithKind("DpuOperatorConfigList"),
		&unstructured.UnstructuredList{},
	)
	metav1.AddToGroupVersion(s, DpuOperatorConfigGVK.GroupVersion())
	return s
}

func newConfig(name string, spec map[string]any) *unstructured.Unstructured {
	c := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	c.SetGroupVersionKind(DpuOperatorConfigGVK)
	c.SetName(name)
	return c
}

func TestLifecycleReconcile(t *testing.T) {
	sch := lcmScheme()
	cases := []struct {
		name  string
		obj   *unstructured.Unstructured
		reqNN types.NamespacedName
	}{
		{"with vendor", newConfig("cfg-dpf", map[string]any{"vendor": "dpf"}), types.NamespacedName{Name: "cfg-dpf"}},
		{"no vendor", newConfig("cfg-empty", map[string]any{"logLevel": int64(2)}), types.NamespacedName{Name: "cfg-empty"}},
		{"missing object", newConfig("placeholder", map[string]any{}), types.NamespacedName{Name: "does-not-exist"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// WithStatusSubresource: the LCM writes its version-skew condition
			// via Status().Update, which the fake client only routes when the
			// object is registered as having a status subresource (the real
			// DpuOperatorConfig CRD declares one).
			cl := fake.NewClientBuilder().WithScheme(sch).
				WithObjects(tc.obj).
				WithStatusSubresource(tc.obj).
				Build()
			r := &LifecycleManager{Client: cl, Scheme: sch}
			if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: tc.reqNN}); err != nil {
				t.Fatalf("Reconcile(%s) error: %v", tc.name, err)
			}
		})
	}
}

// configWithVersion builds a config for a vendor with an installed-version
// annotation, which is how the LCM learns the version until Helm/OLM wiring
// lands.
func configWithVersion(name, vendor, installed string) *unstructured.Unstructured {
	c := newConfig(name, map[string]any{"vendor": vendor})
	if installed != "" {
		c.SetAnnotations(map[string]string{VersionAnnotation: installed})
	}
	return c
}

func conditionOfType(t *testing.T, obj *unstructured.Unstructured, want string) map[string]any {
	t.Helper()
	conds, _, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil {
		t.Fatalf("reading conditions: %v", err)
	}
	for _, raw := range conds {
		if c, ok := raw.(map[string]any); ok && c["type"] == want {
			return c
		}
	}
	return nil
}

// TestLifecycleReportsVersionSkew is the behavioural half of the version-skew
// work: the classification is unit-tested in version_test.go, this checks the
// LCM actually publishes it where an operator will see it.
func TestLifecycleReportsVersionSkew(t *testing.T) {
	sch := lcmScheme()
	for _, tc := range []struct {
		name, vendor, installed string
		wantStatus, wantReason  string
	}{
		{"supported", "nvidia", "v25.4.0", "True", string(SkewSupported)},
		{"too old", "nvidia", "v24.1.0", "False", string(SkewTooOld)},
		{"too new", "nvidia", "v26.1.0", "False", string(SkewTooNew)},
		{"unparseable", "nvidia", "latest", "Unknown", string(SkewUnknown)},
		{"absent annotation", "nvidia", "", "Unknown", string(SkewUnknown)},
		{"vendor with no pinned window", "amd", "v1.46.0", "Unknown", string(SkewUnpinned)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := configWithVersion("cfg", tc.vendor, tc.installed)
			cl := fake.NewClientBuilder().WithScheme(sch).
				WithObjects(obj).WithStatusSubresource(obj).Build()
			r := &LifecycleManager{Client: cl, Scheme: sch}

			nn := types.NamespacedName{Name: "cfg"}
			if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			got := &unstructured.Unstructured{}
			got.SetGroupVersionKind(DpuOperatorConfigGVK)
			if err := cl.Get(context.Background(), nn, got); err != nil {
				t.Fatalf("Get: %v", err)
			}

			cond := conditionOfType(t, got, SupportedConditionType)
			if cond == nil {
				t.Fatalf("no %s condition published", SupportedConditionType)
			}
			if cond["status"] != tc.wantStatus {
				t.Errorf("status = %v, want %v", cond["status"], tc.wantStatus)
			}
			if cond["reason"] != tc.wantReason {
				t.Errorf("reason = %v, want %v", cond["reason"], tc.wantReason)
			}
			if msg, _ := cond["message"].(string); msg == "" {
				t.Error("message is empty; the condition says nothing actionable")
			}

			// The daemon owns Ready on this object; the LCM must not have
			// written one.
			if c := conditionOfType(t, got, "Ready"); c != nil {
				t.Errorf("LCM wrote a Ready condition (%v); the dpu-operator daemon owns it", c)
			}
		})
	}
}

// TestLifecycleSkewConditionIsStable checks the upsert does not churn. A
// controller that rewrote lastTransitionTime on every reconcile would make the
// field meaningless and generate endless status writes.
func TestLifecycleSkewConditionIsStable(t *testing.T) {
	sch := lcmScheme()
	obj := configWithVersion("cfg", "nvidia", "v25.4.0")
	cl := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(obj).WithStatusSubresource(obj).Build()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &LifecycleManager{Client: cl, Scheme: sch, Now: func() time.Time {
		clock = clock.Add(90 * time.Second)
		return clock
	}}
	nn := types.NamespacedName{Name: "cfg"}

	read := func() map[string]any {
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(DpuOperatorConfigGVK)
		if err := cl.Get(context.Background(), nn, got); err != nil {
			t.Fatalf("Get: %v", err)
		}
		c := conditionOfType(t, got, SupportedConditionType)
		if c == nil {
			t.Fatal("condition missing")
		}
		return c
	}

	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
	}
	first := read()

	// A fourth reconcile with unchanged input must not move the timestamp.
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := read()["lastTransitionTime"]; got != first["lastTransitionTime"] {
		t.Errorf("lastTransitionTime moved on a no-op reconcile: %v -> %v",
			first["lastTransitionTime"], got)
	}

	// Exactly one condition of this type, however many times we reconcile.
	final := &unstructured.Unstructured{}
	final.SetGroupVersionKind(DpuOperatorConfigGVK)
	if err := cl.Get(context.Background(), nn, final); err != nil {
		t.Fatal(err)
	}
	conds, _, _ := unstructured.NestedSlice(final.Object, "status", "conditions")
	n := 0
	for _, raw := range conds {
		if c, ok := raw.(map[string]any); ok && c["type"] == SupportedConditionType {
			n++
		}
	}
	if n != 1 {
		t.Errorf("found %d %s conditions, want exactly 1 (upsert by type)", n, SupportedConditionType)
	}
}

// TestLifecycleSkewPreservesTransitionTimeAcrossReasonChange covers the branch
// the no-op test cannot reach.
//
// An unchanged reconcile returns before touching the condition at all, so it
// proves nothing about lastTransitionTime handling. The branch that matters is
// when the status stays the same but the reason changes -- Unknown/Unknown
// becoming Unknown/Unpinned, say. metav1 says lastTransitionTime marks the last
// time the *status* changed, so it must survive that.
func TestLifecycleSkewPreservesTransitionTimeAcrossReasonChange(t *testing.T) {
	sch := lcmScheme()
	// nvidia + unparseable version: status Unknown, reason Unknown.
	obj := configWithVersion("cfg", "nvidia", "latest")
	cl := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(obj).WithStatusSubresource(obj).Build()
	// An advancing clock: RFC3339 is second-precision, so without this every
	// reconcile in the same wall-clock second looks identical and the test
	// could not distinguish a preserved timestamp from a rewritten one.
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &LifecycleManager{Client: cl, Scheme: sch, Now: func() time.Time {
		clock = clock.Add(90 * time.Second)
		return clock
	}}
	nn := types.NamespacedName{Name: "cfg"}
	ctx := context.Background()

	read := func() map[string]any {
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(DpuOperatorConfigGVK)
		if err := cl.Get(ctx, nn, got); err != nil {
			t.Fatalf("Get: %v", err)
		}
		c := conditionOfType(t, got, SupportedConditionType)
		if c == nil {
			t.Fatal("condition missing")
		}
		return c
	}

	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	before := read()
	if before["status"] != "Unknown" || before["reason"] != string(SkewUnknown) {
		t.Fatalf("precondition: got status=%v reason=%v, want Unknown/Unknown",
			before["status"], before["reason"])
	}

	// Switch to a vendor with no pinned window: still Unknown, but Unpinned.
	cur := &unstructured.Unstructured{}
	cur.SetGroupVersionKind(DpuOperatorConfigGVK)
	if err := cl.Get(ctx, nn, cur); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(cur.Object, "amd", "spec", "vendor"); err != nil {
		t.Fatal(err)
	}
	if err := cl.Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	after := read()
	if after["reason"] != string(SkewUnpinned) {
		t.Fatalf("reason = %v, want %v (the reason should have changed)",
			after["reason"], SkewUnpinned)
	}
	if after["status"] != "Unknown" {
		t.Fatalf("status = %v, want Unknown (unchanged)", after["status"])
	}
	if after["lastTransitionTime"] != before["lastTransitionTime"] {
		t.Errorf("lastTransitionTime moved although status did not change: %v -> %v",
			before["lastTransitionTime"], after["lastTransitionTime"])
	}
}

// TestLifecycleVendorResolutionPrecedence pins how the vendor is resolved.
//
// spec.vendor must win so the LCM needs no change the day upstream adds the
// field, but the annotation has to work today: the current CRD has no such
// field, so a real apiserver prunes it and spec.vendor always reads empty.
func TestLifecycleVendorResolutionPrecedence(t *testing.T) {
	sch := lcmScheme()
	for _, tc := range []struct {
		name       string
		specVendor string
		annVendor  string
		wantReason string
	}{
		// nvidia has a pinned window, amd does not, so the reason reveals
		// which source the LCM actually used.
		{"spec only", "nvidia", "", string(SkewUnknown)},
		{"annotation only", "", "nvidia", string(SkewUnknown)},
		{"spec wins over annotation", "nvidia", "amd", string(SkewUnknown)},
		{"annotation used when spec is empty", "", "amd", string(SkewUnpinned)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := map[string]any{}
			if tc.specVendor != "" {
				spec["vendor"] = tc.specVendor
			}
			obj := newConfig("cfg", spec)
			if tc.annVendor != "" {
				obj.SetAnnotations(map[string]string{VendorAnnotation: tc.annVendor})
			}
			cl := fake.NewClientBuilder().WithScheme(sch).
				WithObjects(obj).WithStatusSubresource(obj).Build()
			r := &LifecycleManager{Client: cl, Scheme: sch}

			nn := types.NamespacedName{Name: "cfg"}
			if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			got := &unstructured.Unstructured{}
			got.SetGroupVersionKind(DpuOperatorConfigGVK)
			if err := cl.Get(context.Background(), nn, got); err != nil {
				t.Fatalf("Get: %v", err)
			}
			cond := conditionOfType(t, got, SupportedConditionType)
			if cond == nil {
				t.Fatalf("no %s condition published", SupportedConditionType)
			}
			if cond["reason"] != tc.wantReason {
				t.Errorf("reason = %v, want %v (wrong vendor source was used)",
					cond["reason"], tc.wantReason)
			}
		})
	}
}

// TestLifecycleNoVendorPublishesNothing keeps the no-op path a no-op: with
// neither source set there is nothing to report, and inventing a condition
// would imply the LCM had checked something.
func TestLifecycleNoVendorPublishesNothing(t *testing.T) {
	sch := lcmScheme()
	obj := newConfig("cfg", map[string]any{"logLevel": int64(2)})
	cl := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(obj).WithStatusSubresource(obj).Build()
	r := &LifecycleManager{Client: cl, Scheme: sch}

	nn := types.NamespacedName{Name: "cfg"}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(DpuOperatorConfigGVK)
	if err := cl.Get(context.Background(), nn, got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if c := conditionOfType(t, got, SupportedConditionType); c != nil {
		t.Errorf("published %v with no vendor requested", c)
	}
}
