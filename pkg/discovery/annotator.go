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
	"maps"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Annotator watches OPI DataProcessingUnit objects on this node and
// stamps hardware identity annotations the translation engine already
// reads. The serial-number key is chosen per vendor, so one annotator
// serves NVIDIA, AMD and Marvell cards. It does not emit DPF objects.
type Annotator struct {
	client.Client
	Scheme     *runtime.Scheme
	Enumerator Enumerator
	NodeName   string
	// VendorID limits the OPI sources owned by a vendor-specific PCI deployment.
	// Zero retains generic multi-vendor discovery.
	VendorID uint16
	// FirmwareURL, Flavor and FirmwareName are vendor-neutral VALUES. The
	// annotation KEY each one is written under is chosen per vendor from
	// AnnotationsFor, so the same operand serves a BlueField and a DSC.
	FirmwareURL  string
	Flavor       string
	FirmwareName string
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
	if !a.ownsSource(dpu) {
		return ctrl.Result{RequeueAfter: InventoryRefresh}, nil
	}

	live, err := LiveDevices(a.Enumerator)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("enumerate dpu hardware: %w", err)
	}

	views, err := a.nodeViews(ctx, node)
	if err != nil {
		return ctrl.Result{}, err
	}
	asg := AssignDevices(views, live)[dpu.GetName()]
	desired, err := a.desiredAnnotations(dpu.GetAnnotations(), asg)
	if err != nil {
		return ctrl.Result{}, err
	}

	if annotationsEqual(dpu.GetAnnotations(), desired) {
		return ctrl.Result{RequeueAfter: InventoryRefresh}, nil
	}

	orig := dpu.DeepCopy()
	dpu.SetAnnotations(desired)
	if err := a.Patch(ctx, dpu, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, fmt.Errorf("stamp hardware annotations: %w", err)
	}
	if asg.Bound {
		log.Info("stamped hardware identity",
			"dpu", dpu.GetName(),
			"serial", asg.Device.SerialNumber,
			"product", asg.Device.ProductName,
			"pci", asg.Device.PCIAddress,
			"node", node)
	} else if asg.Clear {
		log.Info("cleared stale hardware identity", "dpu", dpu.GetName(), "node", node)
	}
	return ctrl.Result{RequeueAfter: InventoryRefresh}, nil
}

func (a *Annotator) nodeViews(ctx context.Context, node string) ([]DPUView, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(DataProcessingUnitGVK.GroupVersion().WithKind("DataProcessingUnitList"))
	if err := a.List(ctx, list); err != nil {
		return nil, fmt.Errorf("list DataProcessingUnits: %w", err)
	}
	views := make([]DPUView, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		if !a.ownsSource(item) {
			continue
		}
		itemNode, _, _ := unstructured.NestedString(item.Object, "spec", "nodeName")
		if node != "" && itemNode != node {
			continue
		}
		views = append(views, DPUView{Name: item.GetName(), Annotations: item.GetAnnotations()})
	}
	return views, nil
}

// Restricted deployments require an explicit requested vendor. This prevents
// a NVIDIA-only inventory from claiming or clearing another vendor's source.
func (a *Annotator) ownsSource(dpu *unstructured.Unstructured) bool {
	if a.VendorID == 0 {
		return true
	}
	product, _, _ := unstructured.NestedString(dpu.Object, "spec", "dpuProductName")
	switch a.VendorID {
	case NVIDIAVendorID:
		return strings.HasPrefix(product, "BlueField")
	case AMDVendorID:
		return strings.HasPrefix(product, "Pensando") || strings.HasPrefix(product, "DSC")
	case MarvellVendorID:
		return strings.HasPrefix(product, "Marvell") || strings.HasPrefix(product, "OCTEON")
	default:
		return false
	}
}

func (a *Annotator) desiredAnnotations(have map[string]string, asg Assignment) (map[string]string, error) {
	desired := map[string]string{}
	if have != nil {
		maps.Copy(desired, have)
	}
	if asg.Clear || asg.Bound {
		StripIdentity(desired)
	}
	if !asg.Bound {
		return desired, nil
	}
	ident, err := IdentityAnnotations(asg.Device)
	if err != nil {
		return nil, err
	}
	maps.Copy(desired, ident)
	maps.Copy(desired, a.firmwareFor(asg.Device.VendorID))
	return desired, nil
}

func (a *Annotator) firmwareFor(vendorID uint16) map[string]string {
	keys, ok := AnnotationsFor(vendorID)
	if !ok {
		return nil
	}
	out := map[string]string{}
	if a.FirmwareURL != "" {
		out[keys.FirmwareURL] = a.FirmwareURL
	}
	if a.Flavor != "" {
		out[keys.Flavor] = a.Flavor
	}
	if a.FirmwareName != "" {
		out[keys.FirmwareName] = a.FirmwareName
	}
	return out
}

func (a *Annotator) SetupWithManager(mgr ctrl.Manager) error {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(DataProcessingUnitGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(src).
		Named("nvidia-hardware-discovery").
		Complete(a)
}

func annotationsEqual(have, desired map[string]string) bool {
	if len(have) != len(desired) {
		return len(have) == 0 && len(desired) == 0
	}
	for k, v := range desired {
		if have[k] != v {
			return false
		}
	}
	return true
}
