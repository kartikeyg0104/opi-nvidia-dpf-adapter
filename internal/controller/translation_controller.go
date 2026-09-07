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

package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

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

// TranslationReconciler watches the OPI GVK named in a FieldMapping and
// applies that mapping to produce DPF objects. Kind-specific logic lives in
// config/mappings/*.yaml, not in this file.
type TranslationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Spec   *mapping.Spec
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=dataprocessingunits,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=dataprocessingunits/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=dataprocessingunits/finalizers,verbs=update
// +kubebuilder:rbac:groups=config.openshift.io,resources=servicefunctionchains,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=servicefunctionchains/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=servicefunctionchains/finalizers,verbs=update
// +kubebuilder:rbac:groups=provisioning.dpu.nvidia.com,resources=dpus;dpudevices;dpuflavors;bfbs,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=svc.dpu.nvidia.com,resources=dpuservices,verbs=get;list;watch;create;update;patch

func (r *TranslationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("mapping", r.Spec.Metadata.Name)

	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(r.Spec.Source.GVK())
	if err := r.Get(ctx, req.NamespacedName, src); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
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

func (r *TranslationReconciler) applyOne(ctx context.Context, src, obj *unstructured.Unstructured) error {
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
func (r *TranslationReconciler) mirrorStatus(ctx context.Context, src *unstructured.Unstructured) error {
	if r.Spec.Status == nil {
		return nil
	}
	children, err := r.listChildren(ctx, src)
	if err != nil {
		return err
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

// listChildren returns every emitted child of src, located by the translation
// labels across each distinct target GVK the mapping emits. Target CRDs that
// are not installed are skipped rather than failing the roll-up.
func (r *TranslationReconciler) listChildren(ctx context.Context, src *unstructured.Unstructured) ([]any, error) {
	sel := client.MatchingLabels{
		mapping.LabelMapping:    r.Spec.Metadata.Name,
		mapping.LabelSourceKind: r.Spec.Source.Kind,
		mapping.LabelSourceName: src.GetName(),
	}
	seen := map[schema.GroupVersionKind]bool{}
	var children []any
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
		for i := range list.Items {
			children = append(children, list.Items[i].Object)
		}
	}
	return children, nil
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
// illegal case we keep a provenance annotation instead of a dangling ref.
func stampOwner(src, obj *unstructured.Unstructured, scheme *runtime.Scheme) {
	if err := controllerutil.SetControllerReference(src, obj, scheme); err == nil {
		return
	}
	ann := obj.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	key := src.GetName()
	if ns := src.GetNamespace(); ns != "" {
		key = ns + "/" + key
	}
	ann[annSource] = src.GetKind() + ":" + key
	obj.SetAnnotations(ann)
}

// SetupWithManager watches the mapping's source GVK and owns each distinct DPF
// target type so a child status change re-triggers status mirroring.
func (r *TranslationReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
func (r *TranslationReconciler) SourceGVK() schema.GroupVersionKind {
	return r.Spec.Source.GVK()
}
