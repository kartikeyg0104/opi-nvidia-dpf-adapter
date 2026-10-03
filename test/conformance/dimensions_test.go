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

// The two conformance dimensions the architecture review names that were not
// previously part of the gate: the detection handshake, and version-skew
// behaviour. Both run against a mock device/CR set, independent of which
// vendor's operator sits behind the adapter.
package conformance

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pb "github.com/openshift/dpu-operator/dpu-api/gen"
	opi "github.com/opiproject/opi-api/v1/gen/go/lifecycle/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/discovery"
	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/lifecycle"
	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/vsp"
)

// -----------------------------------------------------------------------------
// Dimension: detection handshake
// -----------------------------------------------------------------------------

// The per-node gRPC seam is how the dpu-operator daemon discovers that a vendor
// is present at all. It was covered by pkg/vsp unit tests but sat outside the
// conformance gate, so a vendor adapter could pass conformance while failing
// the handshake the daemon actually performs.
//
// Driven per vendor off the same enumerator abstraction the real VSP uses, so
// this asserts the seam is vendor-independent rather than NVIDIA-shaped.
var _ = Describe("Conformance dimension: detection handshake", func() {
	for _, tc := range []struct {
		vendor   string
		vendorID uint16
		serial   string
		pci      string
		product  string
	}{
		{"nvidia", discovery.NVIDIAVendorID, "MTEXAMPLE0001", "0000:03:00.0", "BlueField-3"},
		{"amd", discovery.AMDVendorID, "DSCEXAMPLE0001", "0000:19:00.0", "Pensando DSC2-100 100G 2p QSFP56 DPU"},
		{"marvell", discovery.MarvellVendorID, "MRVEXAMPLE0001", "0000:07:00.0", "OCTEON-10"},
	} {
		tc := tc
		Context(tc.vendor, func() {
			It("answers Init, GetDevices and the network-function calls", func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sock := fmt.Sprintf("%s/conf-vsp-%s-%d.sock", os.TempDir(), tc.vendor, time.Now().UnixNano())
				defer func() { _ = os.Remove(sock) }()

				enum := discovery.MockEnumerator{Devices: []discovery.Device{
					discovery.StaticDevice(tc.vendorID, tc.serial, tc.pci, tc.product),
				}}
				srv := vsp.NewServer(enum, sock)
				errc := make(chan error, 1)
				go func() { errc <- srv.Start(ctx) }()

				conn := dialSocket(sock)
				defer func() { _ = conn.Close() }()

				By("Init returning the address the daemon should dial")
				ipPort, err := pb.NewLifeCycleServiceClient(conn).Init(ctx, &pb.InitRequest{DpuMode: false})
				Expect(err).NotTo(HaveOccurred(), "Init is the daemon's first call; failing it means no detection")
				Expect(ipPort.GetIp()).NotTo(BeEmpty(), "Init returned no IP for the daemon to dial")
				Expect(ipPort.GetPort()).NotTo(BeZero(), "Init returned no port for the daemon to dial")

				By("GetDevices reporting the enumerated card under its PCI address")
				devs, err := pb.NewDeviceServiceClient(conn).GetDevices(ctx, &pb.Empty{})
				Expect(err).NotTo(HaveOccurred())
				dev, ok := devs.GetDevices()[tc.pci]
				Expect(ok).To(BeTrue(), "no device at %s; got %v", tc.pci, devs.GetDevices())
				Expect(dev.GetID()).To(Equal(tc.serial),
					"the device ID must be the serial, since that is what the mapping requires")

				By("the OPI-API surface reporting the same device")
				opiDevs, err := opi.NewDeviceServiceClient(conn).GetDevices(ctx, &emptypb.Empty{})
				Expect(err).NotTo(HaveOccurred(), "the OPI LifeCycle/Device services must answer too")
				Expect(opiDevs.GetDevices()).To(HaveKey(tc.pci),
					"the dpu-api and OPI-API surfaces disagree about which devices exist")

				By("acknowledging the CNI add/delete path")
				nf := &pb.NFRequest{Input: "pf0", Output: "vf0"}
				_, err = pb.NewNetworkFunctionServiceClient(conn).CreateNetworkFunction(ctx, nf)
				Expect(err).NotTo(HaveOccurred())
				_, err = pb.NewNetworkFunctionServiceClient(conn).DeleteNetworkFunction(ctx, nf)
				Expect(err).NotTo(HaveOccurred())

				By("shutting down cleanly when its context is cancelled")
				cancel()
				Eventually(errc, "5s").Should(Receive(), "the VSP did not stop on context cancel")
			})
		})
	}
})

// dialSocket waits for the VSP's unix socket to appear before building a
// client. grpc.NewClient is lazy and succeeds whether or not anything is
// listening, so waiting on the socket file is what actually synchronises with
// the server; the first RPC would otherwise race it and get ECONNREFUSED.
func dialSocket(sock string) *grpc.ClientConn {
	GinkgoHelper()
	Eventually(func() error {
		_, err := os.Stat(sock)
		return err
	}, "5s", "20ms").Should(Succeed(), "the VSP never created its socket at %s", sock)

	conn, err := grpc.NewClient("passthrough:///"+sock,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}),
	)
	Expect(err).NotTo(HaveOccurred(), "dial %s", sock)
	return conn
}

// -----------------------------------------------------------------------------
// Dimension: version-skew behaviour
// -----------------------------------------------------------------------------

// The adapter writes the vendor operator's CRs, so that operator's API is a
// hard dependency: a release that moves a field turns translation into objects
// that never go ready, with no other symptom. The review therefore names
// version skew as a conformance dimension.
//
// Asserted against the real apiserver rather than a fake client, because the
// point is that the condition survives the CRD's status schema and is visible
// to whoever runs kubectl.
var _ = Describe("Conformance dimension: version-skew behaviour", func() {
	for _, tc := range []struct {
		name, vendor, installed string
		wantStatus, wantReason  string
	}{
		{"inside the supported window", "nvidia", "v25.4.0", "True", string(lifecycle.SkewSupported)},
		{"older than supported", "nvidia", "v24.1.0", "False", string(lifecycle.SkewTooOld)},
		{"newer than supported", "nvidia", "v26.1.0", "False", string(lifecycle.SkewTooNew)},
		{"version not determinable", "nvidia", "", "Unknown", string(lifecycle.SkewUnknown)},
		{"vendor with no pinned window", "amd", "v1.46.0", "Unknown", string(lifecycle.SkewUnpinned)},
	} {
		tc := tc
		Context(tc.name, func() {
			It("publishes a vendor-scoped condition and leaves Ready to the daemon", func() {
				name := fmt.Sprintf("skew-%s-%d", tc.vendor, time.Now().UnixNano())

				cfg := &unstructured.Unstructured{}
				cfg.SetGroupVersionKind(lifecycle.DpuOperatorConfigGVK)
				cfg.SetName(name)
				// Via annotations, not spec.vendor: the upstream CRD has no
				// such field, so a real apiserver prunes it. That is the whole
				// point of asserting this against envtest.
				ann := map[string]string{lifecycle.VendorAnnotation: tc.vendor}
				if tc.installed != "" {
					ann[lifecycle.VersionAnnotation] = tc.installed
				}
				cfg.SetAnnotations(ann)
				Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, cfg) })

				lcm := &lifecycle.LifecycleManager{Client: k8sClient, Scheme: scheme}
				_, err := lcm.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: name},
				})
				Expect(err).NotTo(HaveOccurred())

				got := getObject(lifecycle.DpuOperatorConfigGVK, types.NamespacedName{Name: name})

				cond := conditionByType(got, lifecycle.SupportedConditionType)
				Expect(cond).NotTo(BeNil(),
					"no %s condition; version skew is invisible to the operator",
					lifecycle.SupportedConditionType)
				Expect(cond["status"]).To(Equal(tc.wantStatus))
				Expect(cond["reason"]).To(Equal(tc.wantReason))
				Expect(cond["message"]).NotTo(BeEmpty(),
					"the condition must say what to do about the skew")

				By("not touching the daemon-owned Ready condition")
				// Ready is DpuOperatorConfig's printer column and the daemon
				// owns it; a second writer would misreport the whole cluster.
				Expect(conditionByType(got, "Ready")).To(BeNil(),
					"the lifecycle manager wrote Ready on DpuOperatorConfig")
			})
		})
	}

	It("keeps the condition stable across repeated reconciles", func() {
		name := fmt.Sprintf("skew-stable-%d", time.Now().UnixNano())
		cfg := &unstructured.Unstructured{}
		cfg.SetGroupVersionKind(lifecycle.DpuOperatorConfigGVK)
		cfg.SetName(name)
		cfg.SetAnnotations(map[string]string{
			lifecycle.VendorAnnotation:  "nvidia",
			lifecycle.VersionAnnotation: "v25.4.0",
		})
		Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, cfg) })

		nn := types.NamespacedName{Name: name}
		lcm := &lifecycle.LifecycleManager{Client: k8sClient, Scheme: scheme}
		for i := 0; i < 3; i++ {
			_, err := lcm.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred(), "reconcile %d", i)
		}

		got := getObject(lifecycle.DpuOperatorConfigGVK, nn)
		conds, _, err := unstructured.NestedSlice(got.Object, "status", "conditions")
		Expect(err).NotTo(HaveOccurred())
		n := 0
		for _, raw := range conds {
			if c, ok := raw.(map[string]any); ok && c["type"] == lifecycle.SupportedConditionType {
				n++
			}
		}
		Expect(n).To(Equal(1), "conditions are upserted by type, so repeated reconciles must not accumulate")
	})
})
