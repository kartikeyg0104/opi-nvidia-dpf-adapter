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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DPUReadyConditionType is the NVIDIA DPU dataplane condition on
// DpuOperatorConfig. It is not Ready (daemon-owned) and not
// VendorOperatorReady (CRDs/controllers). Rebooting is a host reset;
// this observer never runs mlxfwreset.
const (
	DPUReadyConditionType     = "VendorDPUReady"
	DPUPhaseRebooting         = "Rebooting"
	DPURequeueAfter           = 5 * time.Second
	hostPowerCycleRequiredKey = "provisioning.dpu.nvidia.com/host-power-cycle-required"
	rebootMethodSystemReset   = "SystemLevelReset"
	rebootMethodFirmware      = "FirmwareReset"
	rebootMethodPowerCycle    = "PowerCycle"
	rebootPhaseSucceeded      = "Succeeded"
)

// DPUReport is a read-only look at provisioning.dpu.nvidia.com/DPU.
type DPUReport struct {
	Ready     bool
	Phase     string
	Method    string
	Names     []string
	Rebooting []string
	Pending   string
	Message   string
}

// ObserveNVIDIADPUs lists NVIDIA DPU objects. It does not patch them,
// power-cycle the host, or run firmware reset.
func ObserveNVIDIADPUs(ctx context.Context, c client.Client) DPUReport {
	if c == nil {
		return DPUReport{Phase: string(SkewUnknown), Message: "no client to list NVIDIA DPUs"}
	}
	if err := ctx.Err(); err != nil {
		return DPUReport{Phase: string(SkewUnknown), Message: err.Error()}
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   nvidiaProvisioningGroup,
		Version: vendorAPIVersion,
		Kind:    dpuKind + "List",
	})
	if err := c.List(ctx, list); err != nil {
		return DPUReport{Phase: string(SkewUnknown), Message: SanitizeLogValue("dpu-list", err.Error())}
	}
	if len(list.Items) == 0 {
		return DPUReport{Phase: string(SkewUnknown), Message: "no NVIDIA DPU objects observed"}
	}

	out := DPUReport{}
	for i := range list.Items {
		dpu := list.Items[i]
		name := dpu.GetNamespace() + "/" + dpu.GetName()
		out.Names = append(out.Names, name)
		phase, _, _ := unstructured.NestedString(dpu.Object, statusKey, "phase")
		if phase == "" {
			phase, _, _ = unstructured.NestedString(dpu.Object, statusKey, statusKey)
		}
		method, _, _ := unstructured.NestedString(dpu.Object, statusKey, "agentStatus", "rebootMethod")
		rebootPhase, _, _ := unstructured.NestedString(dpu.Object, statusKey, "rebootStatus", "phase")
		if out.Phase == "" {
			out.Phase = phase
		}
		if out.Method == "" {
			out.Method = method
		}
		if pending := pendingNvconfigSummary(dpu); pending != "" {
			out.Pending = pending
		}
		ann := dpu.GetAnnotations()
		needsCycle := ann[hostPowerCycleRequiredKey] == annotationTrue
		if nvidiaDPUStalled(phase, method, rebootPhase, needsCycle) {
			out.Rebooting = append(out.Rebooting, name)
			out.Phase = DPUPhaseRebooting
			if method != "" {
				out.Method = method
			}
		}
	}
	if len(out.Rebooting) > 0 {
		out.Message = "NVIDIA DPU " + strings.Join(out.Rebooting, ",") +
			" is Rebooting"
		if out.Method != "" {
			out.Message += " (method " + out.Method + ")"
		}
		if out.Pending != "" {
			out.Message += "; pending NVCONFIG " + out.Pending
		}
		out.Message += "; DPF provisioning is waiting for the required reset. " +
			"This adapter does not run mlxfwreset or reboot the host. " +
			"Pending NVCONFIG is a DPF host-agent step, not an LCM install failure."
		return out
	}
	out.Ready = true
	if out.Phase == "" {
		out.Phase = OperatorReady
	}
	out.Message = "NVIDIA DPU objects are not in Rebooting"
	return out
}

func pendingNvconfigSummary(dpu unstructured.Unstructured) string {
	devices, _, _ := unstructured.NestedSlice(
		dpu.Object, statusKey, "agentStatus", "lastObservedPendingNvconfig", "devices",
	)
	var parts []string
	seen := map[string]struct{}{}
	for _, raw := range devices {
		dev, _ := raw.(map[string]any)
		entries, _ := dev["entries"].([]any)
		for _, e := range entries {
			entry, _ := e.(map[string]any)
			name, _ := entry["name"].(string)
			cur, _ := entry["current"].(string)
			next, _ := entry["next_boot"].(string)
			if name == "" {
				continue
			}
			key := name + "=" + cur + "→" + next
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			parts = append(parts, name+" "+cur+"→"+next)
		}
	}
	return strings.Join(parts, ", ")
}

func nvidiaDPUStalled(phase, method, rebootPhase string, powerCycle bool) bool {
	if powerCycle {
		return true
	}
	// The last agent discovery can outlive the reset. Completed provisioning
	// must take precedence over that historical method.
	if strings.EqualFold(strings.TrimSpace(phase), "Ready") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "rebooting", "perform arm force restart", "checking host reboot required":
		return true
	}
	switch method {
	case rebootMethodSystemReset, rebootMethodFirmware, rebootMethodPowerCycle:
		return !strings.EqualFold(rebootPhase, rebootPhaseSucceeded)
	default:
		return false
	}
}

func dpuNeedsRequeue(rep DPUReport) bool {
	return len(rep.Rebooting) > 0
}

func dpuCondition(rep DPUReport) (status, reason, message string) {
	message = rep.Message
	if message == "" {
		message = "NVIDIA DPU state was not observed"
	}
	switch {
	case len(rep.Rebooting) > 0 || strings.EqualFold(rep.Phase, DPUPhaseRebooting):
		return string(metav1.ConditionFalse), DPUPhaseRebooting, message
	case rep.Ready:
		return string(metav1.ConditionTrue), OperatorReady, message
	default:
		return string(metav1.ConditionUnknown), string(SkewUnknown), message
	}
}
