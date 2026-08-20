//go:build e2e
// +build e2e

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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/test/utils"
)

const (
	// namespace where the project is deployed in
	namespace = "opi-nvidia-dpf-adapter-system"

	// serviceAccountName created for the project
	serviceAccountName = "opi-nvidia-dpf-adapter-controller-manager"

	// metricsServiceName is the name of the metrics service of the project
	metricsServiceName = "opi-nvidia-dpf-adapter-controller-manager-metrics-service"

	// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
	metricsRoleBindingName = "opi-nvidia-dpf-adapter-metrics-binding"

	dpfNamespace = "dpf-operator-system"
	e2eDPUName   = "bf3-e2e"
	e2eSFCName   = "e2e-hbn"
	e2eChartNF   = "hbn"
	e2eImageNF   = "skip-image"
	mockSerial   = "MT25066004A1"
	mockBFBURL   = "https://example.invalid/fw.bfb"
	vspLabel     = "app.kubernetes.io/component=vsp"
	e2eCRDsPath  = "test/e2e/crds"
	e2eSFCPath   = "test/e2e/testdata/servicefunctionchain.yaml"
)

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing OPI source CRDs and NVIDIA DPF CRDs")
		cmd = exec.Command("kubectl", "apply", "-k", e2eCRDsPath)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install OPI/DPF CRDs")
		waitForCRDs()

		By("creating the namespace DPF objects land in for cluster-scoped sources")
		cmd = exec.Command("kubectl", "create", "ns", dpfNamespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create "+dpfNamespace)

		By("deploying the controller-manager and mock VSP")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("deleting translation fixtures")
		cmd = exec.Command("kubectl", "delete", "dataprocessingunit", e2eDPUName, "--ignore-not-found")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "delete", "-f", e2eSFCPath, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager and mock VSP")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling OPI and DPF CRDs")
		cmd = exec.Command("kubectl", "delete", "--ignore-not-found", "-k", e2eCRDsPath)
		_, _ = utils.Run(cmd)

		By("removing DPF and manager namespaces")
		cmd = exec.Command("kubectl", "delete", "ns", dpfNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}

			By("Fetching mock VSP logs")
			cmd = exec.Command("kubectl", "logs", "-l", vspLabel, "-n", namespace, "--tail=200")
			vspLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "VSP logs:\n %s", vspLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get VSP logs: %s", err)
			}

			By("Fetching translated DPF objects")
			cmd = exec.Command("kubectl", "get",
				"dpudevices.provisioning.dpu.nvidia.com,dpuflavors.provisioning.dpu.nvidia.com,bfbs.provisioning.dpu.nvidia.com,dpus.provisioning.dpu.nvidia.com",
				"-n", dpfNamespace, "-o", "wide")
			dpfOut, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "DPF objects:\n%s", dpfOut)
			}
			cmd = exec.Command("kubectl", "get", "dpuservice", "-A", "-o", "wide")
			svcOut, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "DPUService objects:\n%s", svcOut)
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=opi-nvidia-dpf-adapter-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("Translation", func() {
		It("should run the mock VSP and stamp a DataProcessingUnit serial", func() {
			By("waiting for the mock VSP DaemonSet pod")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-l", vspLabel,
					"-o", "jsonpath={.items[0].status.phase}", "-n", namespace)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "VSP pod not found")
				g.Expect(strings.TrimSpace(out)).To(Equal("Running"))
			}).Should(Succeed())

			By("applying a DataProcessingUnit whose nodeName matches the kind node")
			nodeName := clusterNodeName()
			applyDataProcessingUnit(nodeName)

			By("waiting for the VSP to stamp the serial annotation")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "dataprocessingunit", e2eDPUName,
					"-o", `go-template={{index .metadata.annotations "provisioning.dpu.nvidia.com/serial-number"}}`)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal(mockSerial))
			}).Should(Succeed())
		})

		It("should emit DPUDevice, DPUFlavor, BFB, and DPU from the annotated DPU", func() {
			By("waiting for DPUDevice")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "dpudevices.provisioning.dpu.nvidia.com",
					e2eDPUName+"-device", "-n", dpfNamespace,
					"-o", "jsonpath={.spec.serialNumber}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "DPUDevice not created")
				g.Expect(strings.TrimSpace(out)).To(Equal(mockSerial))
			}).Should(Succeed())

			By("waiting for DPUFlavor")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "dpuflavors.provisioning.dpu.nvidia.com",
					"dpf-default-flavor", "-n", dpfNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "DPUFlavor not created")
			}).Should(Succeed())

			By("waiting for BFB")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "bfbs.provisioning.dpu.nvidia.com",
					"bf-bundle", "-n", dpfNamespace, "-o", "jsonpath={.spec.url}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "BFB not created")
				g.Expect(strings.TrimSpace(out)).To(Equal(mockBFBURL))
			}).Should(Succeed())

			By("waiting for DPU")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "dpus.provisioning.dpu.nvidia.com",
					e2eDPUName, "-n", dpfNamespace, "-o", "jsonpath={.spec.serialNumber}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "DPU not created")
				g.Expect(strings.TrimSpace(out)).To(Equal(mockSerial))
			}).Should(Succeed())
		})

		It("should emit a DPUService for the Helm-chart NF and skip the image-only NF", func() {
			By("applying a ServiceFunctionChain with one chart NF and one image NF")
			cmd := exec.Command("kubectl", "apply", "-f", e2eSFCPath)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply "+e2eSFCName)

			By("waiting for DPUService helmChart fields")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "dpuservices.svc.dpu.nvidia.com",
					e2eChartNF, "-n", "default",
					"-o", "jsonpath={.spec.helmChart.source.repoURL}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "DPUService not created")
				g.Expect(strings.TrimSpace(out)).To(Equal("https://helm.ngc.nvidia.com/nvidia/doca"))
			}).Should(Succeed())

			cmd = exec.Command("kubectl", "get", "dpuservices.svc.dpu.nvidia.com",
				e2eChartNF, "-n", "default", "-o", "jsonpath={.spec.helmChart.source.version}")
			version, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(version)).To(Equal("v25.10.1"))

			By("asserting the image-only NF never becomes a DPUService")
			Consistently(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "dpuservices.svc.dpu.nvidia.com",
					e2eImageNF, "-n", "default")
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(Or(ContainSubstring("NotFound"), ContainSubstring("not found")))
			}, 8*time.Second, time.Second).Should(Succeed())
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}

func waitForCRDs() {
	crds := []string{
		"dataprocessingunits.config.openshift.io",
		"servicefunctionchains.config.openshift.io",
		"dpudevices.provisioning.dpu.nvidia.com",
		"dpuflavors.provisioning.dpu.nvidia.com",
		"bfbs.provisioning.dpu.nvidia.com",
		"dpus.provisioning.dpu.nvidia.com",
		"dpuservices.svc.dpu.nvidia.com",
	}
	for _, crd := range crds {
		By("waiting for CRD " + crd)
		cmd := exec.Command("kubectl", "wait", "--for=condition=Established",
			"crd/"+crd, "--timeout=60s")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "CRD not established: "+crd)
	}
}

func clusterNodeName() string {
	cmd := exec.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[0].metadata.name}")
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to read kind node name")
	Expect(out).NotTo(BeEmpty())
	return strings.TrimSpace(out)
}

func applyDataProcessingUnit(nodeName string) {
	yaml := fmt.Sprintf(`apiVersion: config.openshift.io/v1
kind: DataProcessingUnit
metadata:
  name: %s
spec:
  dpuProductName: BlueField-3
  isDpuSide: false
  nodeName: %s
`, e2eDPUName, nodeName)
	path := filepath.Join(os.TempDir(), "opi-e2e-dataprocessingunit.yaml")
	Expect(os.WriteFile(path, []byte(yaml), os.FileMode(0o644))).To(Succeed())
	cmd := exec.Command("kubectl", "apply", "-f", path)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply DataProcessingUnit")
}
