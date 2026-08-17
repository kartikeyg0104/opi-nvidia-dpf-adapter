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

package discovery

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Annotator watches OPI DataProcessingUnit objects on this node and
// stamps hardware identity annotations the translation engine already
// reads. It does not emit DPF objects.
type Annotator struct {
	client.Client
	Scheme     *runtime.Scheme
	Enumerator Enumerator
	NodeName   string
	BFBURL     string
	Flavor     string
	BFBName    string
}

func (a *Annotator) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	dpu := &unstructured.Unstructured{}
	dpu.SetGroupVersionKind(DataProcessingUnitGVK)
	if err := a.Get(ctx, req.NamespacedName, dpu); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	node, _, _ := unstructured.NestedString(dpu.Object, "spec", "nodeName")
	if a.NodeName != "" && node != a.NodeName {
		return ctrl.Result{}, nil
	}

	devices, err := a.Enumerator.Enumerate()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("enumerate dpu hardware: %w", err)
	}
	if len(devices) == 0 {
		log.Info("no NVIDIA DPU on this node; leaving annotations unchanged")
		return ctrl.Result{}, nil
	}

	// Intel returns function 0 of the matching serial. We take the first
	// discovered BlueField until multi-DPU-per-node is required.
	dev := devices[0]
	if dev.SerialNumber == "" {
		return ctrl.Result{}, fmt.Errorf("discovered device %s has empty serial number", dev.PCIAddress)
	}

	desired := map[string]string{
		SerialNumberAnnotation: dev.SerialNumber,
	}
	if a.BFBURL != "" {
		desired[BFBURLAnnotation] = a.BFBURL
	}
	if a.Flavor != "" {
		desired[FlavorAnnotation] = a.Flavor
	}
	if a.BFBName != "" {
		desired[BFBNameAnnotation] = a.BFBName
	}

	if annotationsMatch(dpu.GetAnnotations(), desired) {
		return ctrl.Result{}, nil
	}

	orig := dpu.DeepCopy()
	ann := dpu.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	for k, v := range desired {
		ann[k] = v
	}
	dpu.SetAnnotations(ann)

	if err := a.Patch(ctx, dpu, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, fmt.Errorf("stamp hardware annotations: %w", err)
	}
	log.Info("stamped hardware identity",
		"dpu", dpu.GetName(),
		"serial", dev.SerialNumber,
		"pci", dev.PCIAddress,
		"node", node)
	return ctrl.Result{}, nil
}

func (a *Annotator) SetupWithManager(mgr ctrl.Manager) error {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(DataProcessingUnitGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(src).
		Named("nvidia-hardware-discovery").
		Complete(a)
}

func annotationsMatch(have map[string]string, desired map[string]string) bool {
	for k, v := range desired {
		if have[k] != v {
			return false
		}
	}
	return true
}
