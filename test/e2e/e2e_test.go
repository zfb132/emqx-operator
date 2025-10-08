/*
Copyright 2025.

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

	. "github.com/emqx/emqx-operator/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

// namespace where the project is deployed in
const namespace = "emqx-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "emqx-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "emqx-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "emqx-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// installing CRDs, and deploying the controller.
	BeforeAll(func() {
		By("create manager namespace")
		Expect(Kubectl("create", "ns", namespace)).To(Succeed())

		By("install CRDs")
		Expect(Run("make", "install")).To(Succeed())

		By("deploy emqx-operator")
		Expect(Run("make", "deploy",
			fmt.Sprintf("OPERATOR_IMAGE=%s", projectImage),
			fmt.Sprintf("KUSTOMIZATION_FILE_PATH=%s", "test/e2e/files/manager"),
		)).To(Succeed())
		Expect(Kubectl("wait", "deployment", "emqx-operator-controller-manager",
			"--for", "condition=Available",
			"--namespace", namespace,
			"--timeout", "1m",
		)).To(Succeed(), "Timed out waiting for emqx-operator deployment")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("undeploy emqx-operator")
		_ = Run("make", "undeploy")

		By("uninstall CRDs")
		_ = Run("make", "uninstall")

		By("delete manager namespace")
		_ = Kubectl("delete", "ns", namespace)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			PrintDiagnosticReport(namespace)
		}
	})

	It("emqx-operator pod should run successfully", func() {
		Eventually(func(g Gomega) {
			// Get the name of the controller-manager pod
			var podList corev1.PodList
			g.Expect(KubectlOut("get", "pods",
				"--namespace", namespace,
				"--selector", "control-plane=controller-manager",
				"-o", "json",
			)).To(UnmarshalInto(&podList), "Failed to list controller-manager pods")
			g.Expect(podList.Items).To(HaveLen(1), "expected 1 controller pod running")
			g.Expect(podList.Items[0]).To(And(
				HaveField("Name", ContainSubstring("controller-manager")),
				HaveField("Status.Phase", Equal(corev1.PodRunning)),
			))
			controllerPodName = podList.Items[0].Name
		}).Should(Succeed())
	})

	It("metrics endpoint should be serving metrics", func() {
		By("create ClusterRoleBinding for service account to allow access to metrics")
		Expect(Kubectl("create", "clusterrolebinding", metricsRoleBindingName,
			"--clusterrole=emqx-operator-metrics-reader",
			fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
		)).To(Succeed())

		By("verify metrics service is available")
		Expect(Kubectl("get", "service", metricsServiceName, "--namespace", namespace)).To(Succeed())

		By("verify Prometheus ServiceMonitor is deployed in the namespace")
		Expect(Kubectl("get", "ServiceMonitor", "--namespace", namespace)).To(Succeed())

		By("fetch service account token")
		token, err := serviceAccountToken()
		Expect(err).NotTo(HaveOccurred())
		Expect(token).NotTo(BeEmpty())

		By("wait for metrics endpoint to be ready")
		Eventually(KubectlOut).
			WithArguments("get", "endpoints", metricsServiceName, "--namespace", namespace).
			Should(ContainSubstring("8443"))

		By("verify emqx-operator is serving metrics")
		Eventually(KubectlOut).
			WithArguments("logs", controllerPodName, "--namespace", namespace).
			Should(ContainSubstring("controller-runtime.metrics\tServing metrics server"))

		By("create curl-metrics pod to access the metrics endpoint")
		Expect(Kubectl("run", "curl-metrics", "--restart=Never",
			"--namespace", namespace,
			"--image=curlimages/curl:7.78.0",
			"--", "/bin/sh", "-c", fmt.Sprintf(
				"curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics",
				token, metricsServiceName, namespace),
		)).To(Succeed())
		Eventually(KubectlOut).
			WithArguments("get", "pods", "curl-metrics", "--namespace", namespace,
				"-o", "jsonpath={.status.phase}",
			).
			Should(Equal("Succeeded"), "curl-metrics pod in wrong status")

		By("lookup expected metrics in curl-metrics logs")
		Expect(KubectlOut("logs", "curl-metrics", "--namespace", namespace)).
			To(ContainSubstring("controller_runtime_reconcile_total"))
	})

	// +kubebuilder:scaffold:e2e-webhooks-checks
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
