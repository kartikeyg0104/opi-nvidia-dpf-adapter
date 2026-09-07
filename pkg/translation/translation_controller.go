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
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

const fieldOwner = "opi-nvidia-dpf-adapter"

// annSource records provenance when Kubernetes GC cannot follow an owner
// reference (namespaced owner of a cluster-scoped or cross-namespace child).
const annSource = "translation.opi.nvidia.com/source"

// cleanupFinalizer lets the controller delete annotation-tracked children that
// Kubernetes garbage collection cannot reach before the source disappears.
const cleanupFinalizer = "translation.opi.nvidia.com/cleanup"

// Exported for tests and conformance assertions.
const (
	AnnSource        = annSource
	CleanupFinalizer = cleanupFinalizer
)

// Reconciler watches the OPI GVK named in a FieldMapping and
// applies that mapping to produce DPF objects. Kind-specific logic lives in
// config/mappings/*.yaml, not in this file.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Spec   *mapping.Spec
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=dataprocessingunits,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=dataprocessingunits/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=dataprocessingunits/finalizers,verbs=update
// +kubebuilder:rbac:groups=config.openshift.io,resources=servicefunctionchains,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=servicefunctionchains/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=servicefunctionchains/finalizers,verbs=update
// +kubebuilder:rbac:groups=provisioning.dpu.nvidia.com,resources=dpus;dpudevices;dpuflavors;bfbs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=svc.dpu.nvidia.com,resources=dpuservices,verbs=get;list;watch;create;update;patch;delete

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("mapping", r.Spec.Metadata.Name)

	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(r.Spec.Source.GVK())
	if err := r.Get(ctx, req.NamespacedName, src); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if ts := src.GetDeletionTimestamp(); ts != nil && !ts.IsZero() {
		return r.reconcileDelete(ctx, log, src)
	}

	// Ensure the cleanup finalizer before emitting anything, so a delete that
	// races the first apply can still reclaim annotation-tracked children.
	// Fall through to apply in the same pass so a single reconcile converges.
	if !controllerutil.ContainsFinalizer(src, cleanupFinalizer) {
		controllerutil.AddFinalizer(src, cleanupFinalizer)
		if err := r.Update(ctx, src); err != nil {
			return ctrl.Result{}, err
		}
	}

	emitted, err := mapping.Apply(r.Spec, src.Object)
	if err != nil {
		log.Error(err, "mapping apply failed")
		return ctrl.Result{}, err
	}

	for _, obj := range emitted {
		if err := r.applyOne(ctx, src, obj); err != nil {
			return ctrl.Result{}, fmt.Errorf("apply %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
		log.Info("applied translated object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
	}

	if err := r.mirrorStatus(ctx, src); err != nil {
		return ctrl.Result{}, fmt.Errorf("mirror status: %w", err)
	}
	return ctrl.Result{}, nil
}

// reconcileDelete deletes annotation-tracked children (owner-ref children are
// left to Kubernetes GC) and then drops the finalizer so the source can go.
func (r *Reconciler) reconcileDelete(ctx context.Context, log logr.Logger, src *unstructured.Unstructured) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(src, cleanupFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.cleanupAnnotatedChildren(ctx, log, src); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(src, cleanupFinalizer)
	if err := r.Update(ctx, src); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// cleanupAnnotatedChildren deletes every labeled child whose provenance
// annotation names this source. These are the children that could not receive
// an owner reference (cross-namespace / cluster-vs-namespaced), so GC will not
// reclaim them; owner-ref children carry no annotation and are skipped.
func (r *Reconciler) cleanupAnnotatedChildren(ctx context.Context, log logr.Logger, src *unstructured.Unstructured) error {
	want := annSourceValue(src)
	objs, err := r.listChildObjects(ctx, src)
	if err != nil {
		return err
	}
	for i := range objs {
		child := &objs[i]
		if child.GetAnnotations()[annSource] != want {
			continue
		}
		if err := r.Delete(ctx, child); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete annotated child %s/%s: %w", child.GetKind(), child.GetName(), err)
		}
		log.Info("deleted annotation-tracked child", "kind", child.GetKind(), "name", child.GetName(), "namespace", child.GetNamespace())
	}
	return nil
}

func (r *Reconciler) applyOne(ctx context.Context, src, obj *unstructured.Unstructured) error {
	stampOwner(src, obj, r.Scheme)

	obj.SetManagedFields(nil)
	return r.Apply(ctx,
		client.ApplyConfigurationFromUnstructured(obj),
		client.ForceOwnership,
		client.FieldOwner(fieldOwner),
	)
}

// mirrorStatus rolls the status of emitted children back onto the OPI source,
// interpreting the mapping's data-driven status: block. No-ops when the mapping
// declares no status rules or when the computed status matches what is stored.
func (r *Reconciler) mirrorStatus(ctx context.Context, src *unstructured.Unstructured) error {
	if r.Spec.Status == nil {
		return nil
	}
	objs, err := r.listChildObjects(ctx, src)
	if err != nil {
		return err
	}
	children := make([]any, 0, len(objs))
	for i := range objs {
		children = append(children, objs[i].Object)
	}
	desired, err := mapping.ApplyStatus(r.Spec, src.Object, children)
	if err != nil {
		return err
	}
	if desired == nil {
		return nil
	}
	if !applyStatus(src, desired) {
		return nil
	}
	return r.Status().Update(ctx, src)
}

// listChildObjects returns every emitted child of src, located by the
// translation labels across each distinct target GVK. Target CRDs that are not
// installed are skipped rather than failing.
func (r *Reconciler) listChildObjects(ctx context.Context, src *unstructured.Unstructured) ([]unstructured.Unstructured, error) {
	sel := client.MatchingLabels{
		mapping.LabelMapping:    r.Spec.Metadata.Name,
		mapping.LabelSourceKind: r.Spec.Source.Kind,
		mapping.LabelSourceName: src.GetName(),
	}
	seen := map[schema.GroupVersionKind]bool{}
	var out []unstructured.Unstructured
	for _, e := range r.Spec.Emit {
		gvk := e.Target.GVK()
		if seen[gvk] {
			continue
		}
		seen[gvk] = true

		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List",
		})
		if err := r.List(ctx, list, sel); err != nil {
			if apimeta.IsNoMatchError(err) {
				continue
			}
			return nil, err
		}
		out = append(out, list.Items...)
	}
	return out, nil
}

// applyStatus merges desired (the .status subtree from ApplyStatus) into src,
// upserting conditions by type and preserving lastTransitionTime when a
// condition's status is unchanged. Returns whether anything changed.
func applyStatus(src *unstructured.Unstructured, desired map[string]any) bool {
	cur, _, _ := unstructured.NestedMap(src.Object, "status")
	if cur == nil {
		cur = map[string]any{}
	}
	changed := false
	for k, v := range desired {
		if k == "conditions" {
			continue
		}
		if !reflect.DeepEqual(cur[k], v) {
			cur[k] = v
			changed = true
		}
	}
	if desiredConds, ok := desired["conditions"].([]any); ok && len(desiredConds) > 0 {
		existing, _ := cur["conditions"].([]any)
		merged, condChanged := mergeConditions(existing, desiredConds)
		if condChanged {
			cur["conditions"] = merged
			changed = true
		}
	}
	if changed {
		_ = unstructured.SetNestedMap(src.Object, cur, "status")
	}
	return changed
}

// mergeConditions upserts desired conditions into existing by type. A condition
// keeps its lastTransitionTime while its status is unchanged and gets a fresh
// timestamp when the status flips. Returns the merged slice and whether it
// differs from existing.
func mergeConditions(existing, desired []any) ([]any, bool) {
	byType := map[string]map[string]any{}
	order := []string{}
	for _, e := range existing {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		byType[t] = m
		order = append(order, t)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	changed := false
	for _, d := range desired {
		dm, ok := d.(map[string]any)
		if !ok {
			continue
		}
		t, _ := dm["type"].(string)
		next := map[string]any{"type": t, "status": dm["status"]}
		if v, ok := dm["reason"]; ok {
			next["reason"] = v
		}
		if v, ok := dm["message"]; ok {
			next["message"] = v
		}

		prev, existed := byType[t]
		if existed && prev["status"] == dm["status"] {
			if ltt, ok := prev["lastTransitionTime"]; ok {
				next["lastTransitionTime"] = ltt
			}
		} else {
			next["lastTransitionTime"] = now
		}
		if !existed {
			order = append(order, t)
			changed = true
		} else if !reflect.DeepEqual(prev, next) {
			changed = true
		}
		byType[t] = next
	}

	out := make([]any, 0, len(order))
	for _, t := range order {
		out = append(out, byType[t])
	}
	return out, changed
}

// stampOwner injects a controller OwnerReference from the OPI source onto
// every emitted DPF object. Kubernetes garbage collection then tears down
// DPUService/DPUDevice/… when the parent ServiceFunctionChain or
// DataProcessingUnit is deleted.
//
// Cluster-scoped owners (DataProcessingUnit) may own namespaced children.
// Namespaced owners may only own children in the same namespace; in that
// illegal case we keep a provenance annotation instead of a dangling ref, and
// the cleanup finalizer deletes such children on source deletion.
func stampOwner(src, obj *unstructured.Unstructured, scheme *runtime.Scheme) {
	if err := controllerutil.SetControllerReference(src, obj, scheme); err == nil {
		return
	}
	ann := obj.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[annSource] = annSourceValue(src)
	obj.SetAnnotations(ann)
}

// annSourceValue is the provenance string stamped on annotation-tracked
// children: "<Kind>:<namespace>/<name>" (namespace omitted when cluster-scoped).
func annSourceValue(src *unstructured.Unstructured) string {
	key := src.GetName()
	if ns := src.GetNamespace(); ns != "" {
		key = ns + "/" + key
	}
	return src.GetKind() + ":" + key
}

// SetupWithManager watches the mapping's source GVK and owns each distinct DPF
// target type so a child status change re-triggers status mirroring.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(r.Spec.Source.GVK())

	b := ctrl.NewControllerManagedBy(mgr).
		For(src).
		Named(r.Spec.Metadata.Name)

	seen := map[schema.GroupVersionKind]bool{}
	for _, e := range r.Spec.Emit {
		gvk := e.Target.GVK()
		if seen[gvk] {
			continue
		}
		seen[gvk] = true
		child := &unstructured.Unstructured{}
		child.SetGroupVersionKind(gvk)
		b = b.Owns(child)
	}
	return b.Complete(r)
}

// SourceGVK is exposed for tests.
func (r *Reconciler) SourceGVK() schema.GroupVersionKind {
	return r.Spec.Source.GVK()
}
