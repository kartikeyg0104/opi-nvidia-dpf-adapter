//go:build stress

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
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const stressUsers = 2000

type stressBreak struct {
	name    string
	ok      bool
	errN    int32
	panicN  int32
	sample  string
	pointer string
}

func TestStressThousandsUsers(t *testing.T) {
	results := []stressBreak{
		stressParseIntent(),
		stressEnsureSamePin(),
		stressEnsureManyConfigs(),
		stressReconcileSameConfig(),
		stressReconcileManyConfigs(),
		stressHelmUpgradeSameRelease(),
		stressClusterHelmSameRelease(),
		stressClaimSameRelease(t),
		stressWorkerOneQueue(),
	}
	fmt.Println("===== STRESS 2000 USERS =====")
	broke := false
	for _, r := range results {
		status := "OK"
		if !r.ok {
			status = "BREAK"
			broke = true
		}
		line := fmt.Sprintf("%s  %s  err=%d panic=%d\n  pointer: %s\n  sample: %s",
			status, r.name, r.errN, r.panicN, r.pointer, r.sample)
		fmt.Println(line)
		t.Log(line)
	}
	if broke {
		t.Fatal("stress found break points")
	}
}

func runN(n int, fn func(i int) error) (errN, panicN int32, sample string) {
	var wg sync.WaitGroup
	var once sync.Once
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					atomic.AddInt32(&panicN, 1)
					once.Do(func() {
						sample = fmt.Sprintf("panic: %v\n%s", rec, string(debug.Stack()))
					})
				}
			}()
			if err := fn(i); err != nil {
				atomic.AddInt32(&errN, 1)
				once.Do(func() { sample = err.Error() })
			}
		}(i)
	}
	wg.Wait()
	return errN, panicN, sample
}

func stressParseIntent() stressBreak {
	cfg := newConfig("dpu-operator-config", map[string]any{"vendor": "nvidia"})
	errN, panicN, sample := runN(stressUsers, func(int) error {
		in := ParseIntent(cfg)
		if !in.Known || in.Vendor != VendorNVIDIA {
			return fmt.Errorf("intent=%+v", in)
		}
		return nil
	})
	return stressBreak{
		name:    "ParseIntent x2000 same config",
		ok:      errN == 0 && panicN == 0,
		errN:    errN,
		panicN:  panicN,
		sample:  sample,
		pointer: "pkg/lifecycle/intent.go ParseIntent / version.go SanitizeVendorToken",
	}
}

func stressEnsureSamePin() stressBreak {
	mem := &MemoryHelmClient{}
	_, _ = HelmInstaller{Client: mem}.Ensure(context.Background(), nvidiaIntent())
	mgr := &LifecycleManager{Helm: mem, Probe: ReadyClusterProbe()}
	cfg := newConfig("dpu-operator-config", map[string]any{"vendor": "nvidia"})
	cfg.SetAnnotations(map[string]string{
		VersionAnnotation:       NVIDIAChartVersion,
		AdoptExistingAnnotation: "true",
	})
	errN, panicN, sample := runN(stressUsers, func(int) error {
		out, err := mgr.EnsureInstalled(context.Background(), cfg)
		if err != nil {
			return err
		}
		if out.Install.Action != InstallNoop && out.Install.Action != InstallAdopted {
			return fmt.Errorf("action=%s", out.Install.Action)
		}
		return nil
	})
	return stressBreak{
		name:    "EnsureInstalled x2000 same pin (MemoryHelm)",
		ok:      errN == 0 && panicN == 0 && mem.count() == 1,
		errN:    errN,
		panicN:  panicN,
		sample:  sample,
		pointer: "pkg/lifecycle/ensure.go EnsureInstalled + helm_client.go HelmInstaller.Ensure + ReleaseLocks",
	}
}

func stressEnsureManyConfigs() stressBreak {
	mem := &MemoryHelmClient{}
	_, _ = HelmInstaller{Client: mem}.Ensure(context.Background(), nvidiaIntent())
	mgr := &LifecycleManager{Helm: mem, Probe: ReadyClusterProbe()}
	errN, panicN, sample := runN(stressUsers, func(i int) error {
		cfg := newConfig(fmt.Sprintf("user-%d", i), map[string]any{"vendor": "nvidia"})
		cfg.SetAnnotations(map[string]string{VersionAnnotation: NVIDIAChartVersion})
		_, err := mgr.EnsureInstalled(context.Background(), cfg)
		return err
	})
	return stressBreak{
		name:    "EnsureInstalled x2000 distinct user configs — one shared Helm name",
		ok:      errN == 0 && panicN == 0 && mem.count() == 1,
		errN:    errN,
		panicN:  panicN,
		sample:  sample,
		pointer: "pkg/lifecycle/helm_client.go releaseKey = dpf-operator-system/dpf-operator — thousands of users cannot have their own release",
	}
}

func stressReconcileSameConfig() stressBreak {
	cfg := newConfig("dpu-operator-config", map[string]any{"vendor": "nvidia"})
	cfg.SetAnnotations(map[string]string{VersionAnnotation: NVIDIAChartVersion})
	cl := fake.NewClientBuilder().WithScheme(lcmScheme()).
		WithObjects(cfg).WithStatusSubresource(cfg).Build()
	mem := &MemoryHelmClient{}
	_, _ = HelmInstaller{Client: mem}.Ensure(context.Background(), nvidiaIntent())
	mgr := &LifecycleManager{Client: cl, Scheme: lcmScheme(), Helm: mem, Probe: ReadyClusterProbe()}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "dpu-operator-config"}}
	errN, panicN, sample := runN(stressUsers, func(int) error {
		_, err := mgr.Reconcile(context.Background(), req)
		return err
	})
	return stressBreak{
		name:    "Reconcile x2000 same DpuOperatorConfig (fake client)",
		ok:      errN == 0 && panicN == 0,
		errN:    errN,
		panicN:  panicN,
		sample:  sample,
		pointer: "pkg/lifecycle/lifecycle_controller.go Reconcile — per-object lock + RetryOnConflict on finalizer and status",
	}
}

func stressReconcileManyConfigs() stressBreak {
	cfgs := make([]client.Object, 0, stressUsers)
	for i := 0; i < stressUsers; i++ {
		cfg := newConfig(fmt.Sprintf("user-%d", i), map[string]any{"vendor": "nvidia"})
		cfg.SetAnnotations(map[string]string{VersionAnnotation: NVIDIAChartVersion})
		cfgs = append(cfgs, cfg)
	}
	cl := fake.NewClientBuilder().WithScheme(lcmScheme()).
		WithObjects(cfgs...).WithStatusSubresource(cfgs...).Build()
	mem := &MemoryHelmClient{}
	_, _ = HelmInstaller{Client: mem}.Ensure(context.Background(), nvidiaIntent())
	mgr := &LifecycleManager{Client: cl, Scheme: lcmScheme(), Helm: mem, Probe: ReadyClusterProbe()}
	errN, panicN, sample := runN(stressUsers, func(i int) error {
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: fmt.Sprintf("user-%d", i)}}
		_, err := mgr.Reconcile(context.Background(), req)
		return err
	})
	return stressBreak{
		name:    "Reconcile x2000 distinct DpuOperatorConfigs in parallel",
		ok:      errN == 0 && panicN == 0,
		errN:    errN,
		panicN:  panicN,
		sample:  sample,
		pointer: "pkg/lifecycle/lifecycle_controller.go:375 SetupWithManager — no MaxConcurrentReconciles (default 1). Parallel here is synthetic; live queue is serial.",
	}
}

func stressHelmUpgradeSameRelease() stressBreak {
	mem := memWith(ownedOlderRelease())
	h := HelmInstaller{Client: mem}
	errN, panicN, sample := runN(stressUsers, func(int) error {
		_, err := h.Ensure(context.Background(), nvidiaIntent())
		return err
	})
	ok := errN == 0 && panicN == 0 && mem.count() == 1 && mem.MaxInflight <= 1
	if !ok && sample == "" {
		sample = fmt.Sprintf("max inflight=%d releases=%d upgrades=%d", mem.MaxInflight, mem.count(), mem.Upgrades)
	}
	return stressBreak{
		name:    "Helm Ensure/Upgrade x2000 same owned release",
		ok:      ok,
		errN:    errN,
		panicN:  panicN,
		sample:  sample,
		pointer: "pkg/lifecycle/helm_client.go:503 releaseKey lock + upgradeOrRollback — 1999 users wait on one mutex",
	}
}

func stressClusterHelmSameRelease() stressBreak {
	cl := fake.NewClientBuilder().WithScheme(lcmScheme()).Build()
	helm := NewClusterHelmClient(cl)
	helm.ChartRoot = "testdata/lcm-probe"
	cfg := ProbeHelmConfig("0.1.0")
	var wins int32
	errN, panicN, sample := runN(stressUsers, func(int) error {
		_, err := helm.Install(context.Background(), cfg, nil)
		if err == nil {
			atomic.AddInt32(&wins, 1)
			return nil
		}
		if errors.Is(err, ErrReleaseExists) {
			return nil
		}
		return err
	})
	var secs corev1.SecretList
	_ = cl.List(context.Background(), &secs, client.InNamespace(HelmProbeNamespace))
	rel, getErr := helm.GetRelease(context.Background(), HelmProbeNamespace, HelmProbeReleaseName)
	ok := errN == 0 && panicN == 0 && wins == 1 && getErr == nil && rel.Revision == 1 && len(secs.Items) == 1
	if !ok && sample == "" {
		sample = fmt.Sprintf("wins=%d secrets=%d getErr=%v revision=%d", wins, len(secs.Items), getErr, rel.Revision)
	}
	return stressBreak{
		name:    "ClusterHelmClient.Install x2000 same opi-lcm-probe",
		ok:      ok,
		errN:    errN,
		panicN:  panicN,
		sample:  sample,
		pointer: "pkg/lifecycle/cluster_helm_client.go Install — per-release lock + GetRelease-then-writeRevision",
	}
}

func stressClaimSameRelease(t *testing.T) stressBreak {
	sec := helmStorageSecret(t, NVIDIAChartVersion, map[string]string{
		"name":    NVIDIAReleaseName,
		"owner":   "helm",
		"status":  "deployed",
		"version": "1",
	})
	cl := fake.NewClientBuilder().WithScheme(lcmScheme()).WithObjects(sec).Build()
	helm := NewSecretHelmClient(cl)
	cfg, err := DefaultInstallConfig(VendorNVIDIA)
	if err != nil {
		return stressBreak{name: "ClaimRelease setup", sample: err.Error(), pointer: "DefaultInstallConfig"}
	}
	var wins int32
	errN, panicN, sample := runN(stressUsers, func(int) error {
		rel, err := helm.ClaimRelease(context.Background(), cfg)
		if err != nil {
			return err
		}
		if rel.ManagedByAdapter() {
			atomic.AddInt32(&wins, 1)
		}
		return nil
	})
	ok := errN == 0 && panicN == 0
	if !ok && sample == "" {
		sample = fmt.Sprintf("wins=%d", wins)
	} else if sample == "" {
		sample = fmt.Sprintf("wins=%d (all concurrent Update on one Secret)", wins)
	}
	return stressBreak{
		name:    "ClaimRelease x2000 same unmanaged Helm secret",
		ok:      ok,
		errN:    errN,
		panicN:  panicN,
		sample:  sample,
		pointer: "pkg/lifecycle/secret_helm_client.go ClaimRelease — per-release lock + RetryOnConflict",
	}
}

func stressWorkerOneQueue() stressBreak {
	mem := &MemoryHelmClient{}
	_, _ = HelmInstaller{Client: mem}.Ensure(context.Background(), nvidiaIntent())
	mgr := &LifecycleManager{Helm: mem, Probe: ReadyClusterProbe()}
	start := time.Now()
	var errN int32
	var sample string
	for i := 0; i < stressUsers; i++ {
		cfg := newConfig(fmt.Sprintf("user-%d", i), map[string]any{"vendor": "nvidia"})
		cfg.SetAnnotations(map[string]string{VersionAnnotation: NVIDIAChartVersion})
		if _, err := mgr.EnsureInstalled(context.Background(), cfg); err != nil {
			errN++
			if sample == "" {
				sample = err.Error()
			}
		}
	}
	elapsed := time.Since(start)
	per := elapsed / time.Duration(stressUsers)
	sample = fmt.Sprintf("serial 2000 EnsureInstalled took %s (%.0f µs/user). SetupWithManager has no MaxConcurrentReconciles — default worker=1.",
		elapsed.Round(time.Millisecond), float64(per.Microseconds()))
	return stressBreak{
		name:    "Worker=1 queue: 2000 users serial EnsureInstalled",
		ok:      errN == 0,
		errN:    errN,
		sample:  sample,
		pointer: "pkg/lifecycle/lifecycle_controller.go:375 SetupWithManager — default MaxConcurrentReconciles=1; helm_client.go:503 one lock for all users",
	}
}
