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

// NVIDIA hardware-discovery VSP. On a real worker this will scan PCI for
// BlueField (vendor 0x15b3) and read the serial. Locally it takes
// --serial-number and stamps the OPI DataProcessingUnit annotation the
// translation engine already consumes.
package main

import (
	"flag"
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/discovery"
	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/vsp"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("vsp")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var (
		probeAddr    string
		metricsAddr  string
		nodeName     string
		serialNumber string
		pciAddress   string
		productName  string
		bfbURL       string
		flavor       string
		bfbName      string
		usePCI       bool
		grpcSocket   string
		grpcOnly     bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "metrics listen address; 0 disables")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8082", "health probe listen address")
	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"),
		"only stamp DataProcessingUnits whose spec.nodeName matches")
	flag.StringVar(&serialNumber, "serial-number", os.Getenv("DPU_SERIAL_NUMBER"),
		"mock BlueField serial (required unless --pci)")
	flag.StringVar(&pciAddress, "pci-address", "0000:03:00.0", "mock PCI address recorded in logs")
	flag.StringVar(&productName, "product-name", "BlueField-3", "mock DPU product name")
	flag.StringVar(&bfbURL, "bfb-url", os.Getenv("DPU_BFB_URL"), "optional BFB URL annotation")
	flag.StringVar(&flavor, "flavor", "", "optional dpu.nvidia.com/flavor annotation")
	flag.StringVar(&bfbName, "bfb-name", "", "optional dpu.nvidia.com/bfb annotation")
	flag.BoolVar(&usePCI, "pci", false, "scan sysfs for vendor 0x15b3 instead of using --serial-number")
	flag.StringVar(&grpcSocket, "grpc-socket", vsp.DefaultSocket,
		"unix socket the dpu-operator daemon dials")
	flag.BoolVar(&grpcOnly, "grpc-only", false,
		"serve the vendor plugin unix socket without a Kubernetes client")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	var enumerator discovery.Enumerator
	if usePCI {
		enumerator = discovery.PCIEnumerator{}
	} else {
		if serialNumber == "" {
			setupLog.Info("either --serial-number or --pci is required")
			os.Exit(1)
		}
		enumerator = discovery.MockEnumerator{
			Devices: []discovery.Device{discovery.StaticDevice(serialNumber, pciAddress, productName)},
		}
	}

	if grpcOnly {
		setupLog.Info("starting NVIDIA VSP gRPC only", "socket", grpcSocket, "pci-mode", usePCI)
		if err := vsp.NewServer(enumerator, grpcSocket).Start(ctrl.SetupSignalHandler()); err != nil {
			setupLog.Error(err, "vendor plugin gRPC server exited")
			os.Exit(1)
		}
		return
	}

	registerUnstructured(scheme, discovery.DataProcessingUnitGVK)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         false,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	ann := &discovery.Annotator{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Enumerator: enumerator,
		NodeName:   nodeName,
		BFBURL:     bfbURL,
		Flavor:     flavor,
		BFBName:    bfbName,
	}
	if err := ann.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create hardware-discovery controller")
		os.Exit(1)
	}

	if err := mgr.Add(vsp.NewServer(enumerator, grpcSocket)); err != nil {
		setupLog.Error(err, "unable to add vendor plugin gRPC server")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting NVIDIA hardware-discovery VSP", "node", nodeName, "pci-mode", usePCI)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited")
		os.Exit(1)
	}
}

func registerUnstructured(s *runtime.Scheme, gvk schema.GroupVersionKind) {
	s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	metav1.AddToGroupVersion(s, gvk.GroupVersion())
}
