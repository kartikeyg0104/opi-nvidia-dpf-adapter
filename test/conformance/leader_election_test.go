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

package conformance

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = Describe("Leader Election and Singleton Controller Failover", func() {
	It("ensures only one active leader when multiple manager instances run, and fails over when leader stops", func() {
		leaseName := "opi-leader-failover-test"
		leaseNS := "default"

		leaseDuration := 2 * time.Second
		renewDeadline := 1 * time.Second
		retryPeriod := 200 * time.Millisecond

		// Manager 1
		ctx1, cancel1 := context.WithCancel(ctx)
		defer cancel1()

		mgr1, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme,
			Metrics: metricsserver.Options{
				BindAddress: "0",
			},
			LeaderElection:                true,
			LeaderElectionID:              leaseName,
			LeaderElectionNamespace:       leaseNS,
			LeaderElectionReleaseOnCancel: true,
			LeaseDuration:                 &leaseDuration,
			RenewDeadline:                 &renewDeadline,
			RetryPeriod:                   &retryPeriod,
		})
		Expect(err).NotTo(HaveOccurred())

		// Manager 2
		ctx2, cancel2 := context.WithCancel(ctx)
		defer cancel2()

		mgr2, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme,
			Metrics: metricsserver.Options{
				BindAddress: "0",
			},
			LeaderElection:                true,
			LeaderElectionID:              leaseName,
			LeaderElectionNamespace:       leaseNS,
			LeaderElectionReleaseOnCancel: true,
			LeaseDuration:                 &leaseDuration,
			RenewDeadline:                 &renewDeadline,
			RetryPeriod:                   &retryPeriod,
		})
		Expect(err).NotTo(HaveOccurred())

		go func() {
			_ = mgr1.Start(ctx1)
		}()
		go func() {
			_ = mgr2.Start(ctx2)
		}()

		By("waiting for one of the managers to become the active elected leader")
		var firstLeader, standbyLeader ctrl.Manager
		var cancelFirstLeader, cancelStandby context.CancelFunc

		Eventually(func() bool {
			select {
			case <-mgr1.Elected():
				firstLeader = mgr1
				standbyLeader = mgr2
				cancelFirstLeader = cancel1
				cancelStandby = cancel2
				return true
			case <-mgr2.Elected():
				firstLeader = mgr2
				standbyLeader = mgr1
				cancelFirstLeader = cancel2
				cancelStandby = cancel1
				return true
			default:
				return false
			}
		}).WithTimeout(10 * time.Second).WithPolling(100 * time.Millisecond).Should(BeTrue())

		By("verifying the standby manager is NOT elected while the first manager is leader")
		Consistently(standbyLeader.Elected()).
			WithTimeout(1 * time.Second).WithPolling(100 * time.Millisecond).
			ShouldNot(BeClosed())
		By("verifying the Lease object exists and holder is active")
		lease := &coordinationv1.Lease{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: leaseName, Namespace: leaseNS}, lease)).To(Succeed())
		Expect(lease.Spec.HolderIdentity).NotTo(BeNil())
		firstHolder := *lease.Spec.HolderIdentity
		Expect(firstHolder).NotTo(BeEmpty())

		By("stopping the first leader manager and releasing the leader lease")
		cancelFirstLeader()

		By("verifying the standby manager acquires the lease and becomes the active leader (failover)")
		Eventually(standbyLeader.Elected()).
			WithTimeout(10 * time.Second).WithPolling(100 * time.Millisecond).
			Should(BeClosed())
		By("verifying the Lease object holder has switched to the new leader")
		Eventually(func(g Gomega) {
			l := &coordinationv1.Lease{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: leaseName, Namespace: leaseNS}, l)).To(Succeed())
			g.Expect(l.Spec.HolderIdentity).NotTo(BeNil())
			g.Expect(*l.Spec.HolderIdentity).NotTo(Equal(firstHolder))
		}).WithTimeout(10 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())

		By("cleaning up the standby manager")
		cancelStandby()
		_ = firstLeader
	})
})
