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
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

const fieldOwner = "opi-nvidia-dpf-adapter"
const (
	statusKey = "status"
	typeKey   = "type"
)

// annSource records provenance when Kubernetes GC cannot follow an owner
// reference (namespaced owner of a cluster-scoped or cross-namespace child).
const annSource = "translation.opi.nvidia.com/source"

// conditionsKey is the status field holding metav1 conditions.
const conditionsKey = "conditions"

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

	// TargetsInstalled reports whether every GVK that this reconcile wants
	// to write is served. Nil skips the check (unit tests with no discovery).
	// Production wires this to the manager RESTMapper, which is reset when
	// a lookup misses so a CRD that appeared during LCM install is noticed.
	TargetsInstalled func(context.Context, []schema.GroupVersionKind) (bool, error)

	schemaMu sync.Mutex
	schemas  map[schema.GroupVersionKind]map[string]any
}

// RBAC for the mapped kinds is NOT declared here. It is generated into
// zz_generated_rbac.go from config/mappings by hack/gen-rbac, so onboarding a
// vendor needs no Go edit at all. These markers were previously the one place a
// vendor port was not pure data; `make generate-rbac` replaces that step, and
// `make verify-rbac` fails the build if a mapping changed without it.

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("mapping", r.Spec.Metadata.Name)
	ctx = logf.IntoContext(ctx, log)

	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(r.Spec.Source.GVK())
	if err := r.Get(ctx, req.NamespacedName, src); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if ts := src.GetDeletionTimestamp(); ts != nil && !ts.IsZero() {
		return r.reconcileDelete(ctx, src)
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
	pruneAllowed, err := mapping.ShouldPrune(r.Spec, src.Object)
	if err != nil {
		return ctrl.Result{}, err
	}

	// DPF CRDs show up while the LCM Helm install is still running.
	// VendorOperatorReady is the signal that the required APIs and
	// controllers are up (docs/lcm-operator-status.md). Returning a
	// fixed delay keeps this source queued. An error would use the
	// rate-limiter's fast first retry. An empty result would drop it
	// until some other watch fired.
	if len(emitted) > 0 {
		wait, err := r.lcmBlocks(ctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		apisReady := true
		if !wait && r.TargetsInstalled != nil {
			apisReady, err = r.TargetsInstalled(ctx, uniqueGVKs(emitted))
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		if wait || !apisReady {
			log.Info("DPF is not ready; source stays queued", "lcm", wait, "apis", apisReady)
			return ctrl.Result{RequeueAfter: DPFNotReadyRequeue}, nil
		}
	}

	for _, obj := range emitted {
		if err := r.applyOne(ctx, src, obj); err != nil {
			return ctrl.Result{}, fmt.Errorf("apply %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
		log.Info("applied translated object", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())
	}

	// Children the desired set no longer names must not outlive the change:
	// a shrunk or renamed chain would otherwise leave its old members behind
	// until the whole source is deleted. Prune before mirroring so the
	// conditions reflect exactly the children that remain.
	if pruneAllowed {
		if err := r.pruneStaleChildren(ctx, src, emitted); err != nil {
			return ctrl.Result{}, fmt.Errorf("prune stale children: %w", err)
		}
	}

	if err := r.mirrorStatus(ctx, src); err != nil {
		return ctrl.Result{}, fmt.Errorf("mirror status: %w", err)
	}
	return ctrl.Result{}, nil
}

// pruneStaleChildren deletes children this source previously emitted that the
// current desired set no longer contains: chain members that were removed,
// renamed, or guarded out of the mapping. Kubernetes GC only reclaims children
// when the source itself dies, so a spec update alone would otherwise leak the
// old members forever. Owner-referenced and annotation-tracked children carry
// the same translation labels, so one labeled listing covers both tracks.
func (r *Reconciler) pruneStaleChildren(
	ctx context.Context, src *unstructured.Unstructured, emitted []*unstructured.Unstructured,
) error {
	desired := make(map[string]bool, len(emitted))
	for _, obj := range emitted {
		desired[childKey(obj.GetKind(), obj.GetNamespace(), obj.GetName())] = true
	}
	objs, err := r.listChildObjects(ctx, src)
	if err != nil {
		return err
	}
	wantAnn := annSourceValue(src)
	log := logf.FromContext(ctx)
	for i := range objs {
		child := &objs[i]
		// The label selector spans every namespace this source kind emits
		// into, so a same-named source elsewhere can surface in the listing.
		// Only children this exact source owns are prunable.
		if !ownedBySource(child, src, wantAnn) {
			continue
		}
		if desired[childKey(child.GetKind(), child.GetNamespace(), child.GetName())] {
			continue
		}
		if err := r.Delete(ctx, child); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete stale child %s/%s: %w", child.GetKind(), child.GetName(), err)
		}
		log.Info("deleted stale child no longer in the desired set",
			"kind", child.GetKind(), "name", child.GetName(), "namespace", child.GetNamespace())
	}
	return nil
}

// ownedBySource reports whether child was emitted by src: either it carries a
// controller owner reference to src (the same-namespace track) or the
// provenance annotation stamped when the owner reference was illegal (the
// cross-namespace track). UID equality is what makes a deleted-then-recreated
// source with a matching name start fresh instead of adopting the old tree.
func ownedBySource(child, src *unstructured.Unstructured, wantAnn string) bool {
	for _, ref := range child.GetOwnerReferences() {
		if ref.Kind == src.GetKind() && ref.Name == src.GetName() &&
			ref.UID != "" && ref.UID == src.GetUID() {
			return true
		}
	}
	return child.GetAnnotations()[annSource] == wantAnn
}

func childKey(kind, namespace, name string) string {
	return kind + "|" + namespace + "|" + name
}

// reconcileDelete deletes annotation-tracked children (owner-ref children are
// left to Kubernetes GC) and then drops the finalizer so the source can go.
func (r *Reconciler) reconcileDelete(ctx context.Context, src *unstructured.Unstructured) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(src, cleanupFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.cleanupAnnotatedChildren(ctx, src); err != nil {
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
func (r *Reconciler) cleanupAnnotatedChildren(ctx context.Context, src *unstructured.Unstructured) error {
	log := logf.FromContext(ctx)
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
		log.Info("deleted annotation-tracked child",
			"kind", child.GetKind(), "name", child.GetName(), "namespace", child.GetNamespace())
	}
	return nil
}

func (r *Reconciler) applyOne(ctx context.Context, src, obj *unstructured.Unstructured) error {
	stampOwner(src, obj, r.Scheme)
	log := logf.FromContext(ctx)

	if crdSchema, err := r.liveCRDSchema(ctx, obj.GroupVersionKind()); err == nil && crdSchema != nil {
		for _, path := range pruneUnknownSpec(obj, crdSchema) {
			log.Info("omitted field not on live CRD schema",
				"kind", obj.GetKind(), "name", obj.GetName(), "field", path)
		}
	}

	// Safety net: if the live OpenAPI still rejects a field (stale cache,
	// CRD get forbidden), drop that path and retry.
	for range 8 {
		obj.SetManagedFields(nil)
		err := r.Apply(ctx,
			client.ApplyConfigurationFromUnstructured(obj),
			client.ForceOwnership,
			client.FieldOwner(fieldOwner),
		)
		if err == nil {
			return nil
		}
		path, dropErr := omitLiveUnknownFields(obj, err)
		if dropErr != nil {
			return dropErr
		}
		log.Info("omitted field not declared in live CRD",
			"kind", obj.GetKind(), "name", obj.GetName(), "field", path)
	}
	return fmt.Errorf("apply %s/%s: too many live-unknown fields", obj.GetKind(), obj.GetName())
}

// mirrorStatus rolls the status of emitted children back onto the OPI source,
// interpreting the mapping's data-driven status: block. No-ops when the mapping
// declares no status rules or when the computed status matches what is stored.
func (r *Reconciler) mirrorStatus(ctx context.Context, src *unstructured.Unstructured) error {
	if r.Spec.Status == nil {
		return nil
	}
	// When the source CRD status object has no conditions[], a write is
	// pruned by the apiserver. Skip the update so we do not pretend DPFReady
	// landed. Flip status.schema to "conditions" when upstream adds the field.
	if r.Spec.Status.Schema == mapping.StatusSchemaPruned {
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
func (r *Reconciler) listChildObjects(
	ctx context.Context, src *unstructured.Unstructured,
) ([]unstructured.Unstructured, error) {
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
	cur, _, _ := unstructured.NestedMap(src.Object, statusKey)
	if cur == nil {
		cur = map[string]any{}
	}
	changed := false
	for k, v := range desired {
		if k == conditionsKey {
			continue
		}
		if !reflect.DeepEqual(cur[k], v) {
			cur[k] = v
			changed = true
		}
	}
	if desiredConds, ok := desired[conditionsKey].([]any); ok && len(desiredConds) > 0 {
		existing, _ := cur[conditionsKey].([]any)
		merged, condChanged := mergeConditions(existing, desiredConds)
		if condChanged {
			cur[conditionsKey] = merged
			changed = true
		}
	}
	if changed {
		_ = unstructured.SetNestedMap(src.Object, cur, statusKey)
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
		t, _ := m[typeKey].(string)
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
		t, _ := dm[typeKey].(string)
		if t == "" {
			// A condition without a type is not addressable and would be
			// keyed on "" (clobbering any later typeless entry). metav1
			// requires a non-empty type; drop malformed mapping output
			// rather than mirror an invalid condition onto the source.
			continue
		}
		if strings.EqualFold(t, mapping.ReservedConditionType) {
			// VSP/OPI daemon owns Ready. Never upsert it even if a mapping
			// slipped past Validate.
			continue
		}
		st, hasStatus := dm[statusKey]
		if !hasStatus || st == nil || st == "" {
			// metav1.Condition requires a status (True/False/Unknown). Skip
			// rather than write a condition the apiserver will reject, which
			// would fail the whole status update and stall status mirroring.
			continue
		}
		next := map[string]any{typeKey: t, statusKey: st}
		if v, ok := dm["reason"]; ok {
			next["reason"] = v
		}
		if v, ok := dm["message"]; ok {
			next["message"] = v
		}

		prev, existed := byType[t]
		if existed && prev[statusKey] == st {
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
	var targets []schema.GroupVersionKind
	for _, e := range r.Spec.Emit {
		gvk := e.Target.GVK()
		if seen[gvk] {
			continue
		}
		seen[gvk] = true
		targets = append(targets, gvk)
	}
	// Owns() starts an informer. A missing CRD fails cache sync and stops
	// the whole manager, which drops every source. Own only kinds that
	// discovery already serves. Kinds that appear later are picked up by
	// the CRD watch below, which requeues the source.
	owned, err := ownableTargets(mgr.GetRESTMapper(), targets)
	if err != nil {
		return err
	}
	ownedSet := map[schema.GroupVersionKind]bool{}
	for _, gvk := range owned {
		ownedSet[gvk] = true
		child := &unstructured.Unstructured{}
		child.SetGroupVersionKind(gvk)
		b = b.Owns(child)
	}
	for _, gvk := range targets {
		if !ownedSet[gvk] {
			ctrl.Log.Info("DPF kind is not installed; not watching it yet",
				"mapping", r.Spec.Metadata.Name, "gvk", gvk.String())
		}
	}

	cfg := &unstructured.Unstructured{}
	cfg.SetGroupVersionKind(lcmConfigGVK())
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(crdGVK())
	b = b.Watches(cfg, handler.EnqueueRequestsFromMapFunc(r.enqueueIfLCMReady))
	b = b.Watches(crd, handler.EnqueueRequestsFromMapFunc(r.enqueueIfTargetCRD))
	return b.Complete(r)
}

// SourceGVK is exposed for tests.
func (r *Reconciler) SourceGVK() schema.GroupVersionKind {
	return r.Spec.Source.GVK()
}
