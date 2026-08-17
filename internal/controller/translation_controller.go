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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// TranslationReconciler watches the OPI GVK named in a FieldMapping and
// applies that mapping to produce DPF objects. Kind-specific logic lives in
// config/mappings/*.yaml, not in this file.
type TranslationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Spec   *mapping.Spec
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=dataprocessingunits,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=dataprocessingunits/status,verbs=get
// +kubebuilder:rbac:groups=config.openshift.io,resources=servicefunctionchains,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=servicefunctionchains/status,verbs=get
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
	return ctrl.Result{}, nil
}

func (r *TranslationReconciler) applyOne(ctx context.Context, src, obj *unstructured.Unstructured) error {
	if src.GetNamespace() != "" && obj.GetNamespace() == src.GetNamespace() {
		if err := controllerutil.SetControllerReference(src, obj, r.Scheme); err != nil {
			return err
		}
	} else {
		// Cluster-scoped sources cannot own namespaced children via
		// controller references. Record provenance in annotations instead.
		ann := obj.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann["translation.opi.nvidia.com/source"] = src.GetKind() + ":" + src.GetName()
		obj.SetAnnotations(ann)
		obj.SetOwnerReferences([]metav1.OwnerReference{})
	}

	obj.SetManagedFields(nil)
	return r.Apply(ctx,
		client.ApplyConfigurationFromUnstructured(obj),
		client.ForceOwnership,
		client.FieldOwner(fieldOwner),
	)
}

// SetupWithManager watches the mapping's source GVK.
func (r *TranslationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(r.Spec.Source.GVK())
	return ctrl.NewControllerManagedBy(mgr).
		For(src).
		Named(r.Spec.Metadata.Name).
		Complete(r)
}

// SourceGVK is exposed for tests.
func (r *TranslationReconciler) SourceGVK() schema.GroupVersionKind {
	return r.Spec.Source.GVK()
}
