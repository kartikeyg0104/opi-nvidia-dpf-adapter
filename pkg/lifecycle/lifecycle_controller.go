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

// Package lifecycle holds the Lifecycle Manager (LCM) of the Hybrid pattern.
// The LCM watches DpuOperatorConfig and is responsible for installing and
// pinning the requested vendor's operator (e.g. NVIDIA DPF) via Helm/OLM. This
// phase ships a lightweight placeholder that reads the requested vendor and
// logs the intent; the Helm/OLM wiring lands in a later phase.
package lifecycle

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// DpuOperatorConfigGVK is the cluster-scoped config the LCM reconciles.
var DpuOperatorConfigGVK = schema.GroupVersionKind{
	Group:   "config.openshift.io",
	Version: "v1",
	Kind:    "DpuOperatorConfig",
}

// vendorField is where the requested vendor is read from on the config. The
// current upstream DpuOperatorConfig has no vendor field; this is the agreed
// convention the LCM will honor once the upstream API carries it. Until then
// it resolves empty and the LCM logs that no vendor was requested.
var vendorField = []string{"spec", "vendor"}

// LifecycleManager installs and pins the requested vendor's operator. In this
// phase it only logs intent; it is the placeholder for the Helm/OLM logic.
type LifecycleManager struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=dpuoperatorconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=dpuoperatorconfigs/status,verbs=get

func (r *LifecycleManager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithName("lifecycle")

	cfg := &unstructured.Unstructured{}
	cfg.SetGroupVersionKind(DpuOperatorConfigGVK)
	if err := r.Get(ctx, req.NamespacedName, cfg); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	vendor, _, _ := unstructured.NestedString(cfg.Object, vendorField...)
	if vendor == "" {
		log.Info("DpuOperatorConfig has no requested vendor; nothing to install",
			"config", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	// Placeholder for the real Helm/OLM install-and-pin logic.
	log.Info("would install and pin the vendor operator (placeholder for Helm/OLM)",
		"vendor", vendor,
		"config", req.NamespacedName,
		"action", "ensure-installed-and-pinned")
	return ctrl.Result{}, nil
}

// SetupWithManager wires the LCM to watch DpuOperatorConfig.
func (r *LifecycleManager) SetupWithManager(mgr ctrl.Manager) error {
	cfg := &unstructured.Unstructured{}
	cfg.SetGroupVersionKind(DpuOperatorConfigGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(cfg).
		Named("lifecycle-manager").
		Complete(r)
}
