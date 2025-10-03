package e2e

import (
	"fmt"

	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	. "github.com/emqx/emqx-operator/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	emqxCRBasic      = "test/e2e/files/resources/emqx.yaml"
	emqxImage        = "emqx/emqx:5.10.0"
	emqxImageUpgrade = "emqx/emqx:5.10.1"
)

func withCores(numReplicas int) []byte {
	return fmt.Appendf(nil,
		`{"spec": {"coreTemplate": {"spec": {"replicas": %d}}}}`,
		numReplicas,
	)
}

func withReplicants(numReplicas int) []byte {
	return fmt.Appendf(nil,
		`{"spec": {"replicantTemplate": {"spec": {"replicas": %d}}}}`,
		numReplicas,
	)
}

func withImage(image string) []byte {
	return fmt.Appendf(nil, `{"spec": {"image": "%s"}}`, image)
}

func withDS() []byte {
	return FromYAML([]byte(`
spec:
  config:
    data: |
      license { key = "evaluation" }
      durable_sessions { enable = true }
      durable_storage { 
        messages {
          backend = builtin_raft
          n_shards = 8
        }
      }`))
}

var _ = Describe("E2E Test", Label("base"), Ordered, func() {

	BeforeAll(func() {
		By("create manager namespace")
		Expect(Kubectl("create", "ns", namespace)).To(Succeed())

		By("install CRDs")
		Expect(Run("make", "install")).To(Succeed())

		By("deploy emqx-operator")
		Expect(Run("make", "deploy",
			fmt.Sprintf("IMG=%s", projectImage),
			fmt.Sprintf("KUSTOMIZATION_FILE_PATH=%s", "test/e2e/files/manager"),
		)).To(Succeed())
		Expect(Kubectl("wait", "deployment", "emqx-operator-controller-manager",
			"--for", "condition=Available",
			"--namespace", namespace,
			"--timeout", "1m",
		)).To(Succeed(), "Timed out waiting for emqx-operator deployment")
	})

	AfterAll(func() {
		By("undeploy emqx-operator")
		_ = Run("make", "undeploy")

		By("uninstall CRDs")
		_ = Run("make", "uninstall")

		By("delete manager namespace")
		_ = Kubectl("delete", "ns", namespace)
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			PrintDiagnosticReport(namespace)
		}
	})

	Context("EMQX Cluster", func() {
		// Initial number of core replicas:
		var coreReplicas int = 2

		It("deploy cluster", func() {
			By("create EMQX cluster")
			emqxCR := PatchDocument(
				FromYAMLFile(emqxCRBasic),
				withImage(emqxImage),
				withCores(coreReplicas),
			)
			Expect(KubectlStdin(emqxCR, "apply", "-f", "-")).To(Succeed())
			By("wait for EMQX cluster to be ready")
			Eventually(checkEMQXReady).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			checkNoReplicants(Default)
		})

		It("scale cluster up", func() {
			coreReplicas = 3
			scaleupStartedAt := metav1.Now()
			Expect(Kubectl("patch", "emqx", "emqx",
				"--type", "json",
				"--patch", `[{"op": "replace", "path": "/spec/coreTemplate/spec/replicas", "value": 3}]`,
			)).To(Succeed(), "Failed to scale up EMQX cluster")
			Eventually(checkEMQXReady).WithArguments(scaleupStartedAt).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			checkNoReplicants(Default)
		})

		It("change image to trigger blue-green update", func() {
			By("create MQTTX client")
			Expect(Kubectl("apply", "-f", "test/e2e/files/resources/mqttx.yaml")).To(Succeed())
			DeferCleanup(Kubectl, "delete", "-f", "test/e2e/files/resources/mqttx.yaml")
			Expect(Kubectl("wait", "pod",
				"--selector=app=mqttx",
				"--for=condition=Ready",
				"--timeout=1m",
			)).To(Succeed(), "Timed out waiting MQTTX to be ready")

			By("fetch current core StatefulSet")
			var stsList appsv1.StatefulSetList
			coreRev, err := KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.coreNodesStatus.currentRevision}")
			Expect(err).NotTo(HaveOccurred(), "Failed to get EMQX status")
			Expect(KubectlOut("get", "statefulset",
				"--selector", appsv2beta1.LabelsPodTemplateHashKey+"="+coreRev,
				"-o", "json",
			)).To(UnmarshalInto(&stsList), "Failed to list statefulsets")
			Expect(stsList.Items).To(HaveLen(1))

			By("change EMQX image")
			changedAt := metav1.Now()
			Expect(Kubectl("patch", "emqx", "emqx",
				"--type", "json",
				"--patch", `[{"op": "replace", "path": "/spec/image", "value": "`+emqxImageUpgrade+`"}]`)).
				To(Succeed())

			By("check EMQX cluster node evacuations status")
			Eventually(KubectlOut).
				WithArguments("get", "emqx", "emqx", "-o", "jsonpath={.status.nodeEvacuationsStatus}").
				ShouldNot(ContainSubstring("connection_eviction_rate"))

			Eventually(checkEMQXReady).WithArguments(changedAt).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			checkNoReplicants(Default)

			By("check previous core StatefulSet has been scaled down to 0")
			out, err := KubectlOut("get", "statefulset", stsList.Items[0].Name, "-o", "jsonpath={.status.replicas}")
			Expect(err).NotTo(HaveOccurred(), "Failed to get core StatefulSet replicas")
			Expect(out).To(Equal("0"))
		})

		It("delete cluster", func() {
			Expect(Kubectl("delete", "emqx", "emqx")).To(Succeed())
			Expect(Kubectl("get", "emqx", "emqx")).To(HaveOccurred(), "EMQX cluster still exists")
		})
	})

	Context("EMQX Core-Replicant Cluster", func() {
		// Initial number of core and replicant replicas:
		var coreReplicas int = 1
		var replicantReplicas int = 2

		It("deploy cluster", func() {
			By("create EMQX cluster")
			emqxCR := PatchDocument(
				FromYAMLFile(emqxCRBasic),
				withImage(emqxImage),
				withCores(coreReplicas),
				withReplicants(replicantReplicas),
			)
			Expect(KubectlStdin(emqxCR, "apply", "-f", "-")).To(Succeed())
			By("wait for EMQX cluster to be ready")
			Eventually(checkEMQXReady).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			Eventually(checkReplicantStatus).WithArguments(replicantReplicas).Should(Succeed())
		})

		It("scale cluster up", func() {
			coreReplicas = 2
			replicantReplicas = 3
			scaleupStartedAt := metav1.Now()
			By("change number of core replicas")
			Expect(Kubectl("patch", "emqx", "emqx",
				"--type", "json",
				"--patch", `[{"op": "replace", "path": "/spec/coreTemplate/spec/replicas", "value": 2}]`)).
				To(Succeed(), "Failed to scale emqx cluster")
			By("change number of replicant replicas")
			Expect(Kubectl("patch", "emqx", "emqx",
				"--type", "json",
				"--patch", `[{"op": "replace", "path": "/spec/replicantTemplate/spec/replicas", "value": 3}]`)).
				To(Succeed(), "Failed to scale emqx cluster")
			By("wait for EMQX cluster to be ready after scaling")
			Eventually(checkEMQXReady).WithArguments(scaleupStartedAt).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			Eventually(checkReplicantStatus).WithArguments(replicantReplicas).Should(Succeed())
		})

		It("change image for target blue-green update", func() {
			By("create MQTTX client")
			Expect(Kubectl("apply", "-f", "test/e2e/files/resources/mqttx.yaml")).To(Succeed())
			DeferCleanup(Kubectl, "delete", "-f", "test/e2e/files/resources/mqttx.yaml")
			Expect(Kubectl("wait", "pod",
				"--selector=app=mqttx",
				"--for=condition=Ready",
				"--timeout=1m",
			)).To(Succeed(), "Timed out waiting for MQTTX to be ready")

			By("fetch current core StatefulSet")
			var stsList appsv1.StatefulSetList
			coreRev, err := KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.coreNodesStatus.currentRevision}")
			Expect(err).NotTo(HaveOccurred(), "Failed to get EMQX status")
			Expect(KubectlOut("get", "statefulset",
				"--selector", appsv2beta1.LabelsPodTemplateHashKey+"="+coreRev,
				"-o", "json",
			)).To(UnmarshalInto(&stsList), "Failed to list statefulsets")
			Expect(stsList.Items).To(HaveLen(1))

			By("fetch current replicant ReplicaSet")
			var rsList appsv1.ReplicaSetList
			replRev, err := KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.replicantNodesStatus.currentRevision}")
			Expect(err).NotTo(HaveOccurred(), "Failed to get EMQX status")
			Expect(KubectlOut("get", "replicaset",
				"--selector", appsv2beta1.LabelsPodTemplateHashKey+"="+replRev,
				"-o", "json",
			)).To(UnmarshalInto(&rsList), "Failed to list replicasets")
			Expect(rsList.Items).To(HaveLen(1))

			By("change EMQX image")
			changingTime := metav1.Now()
			Expect(Kubectl("patch", "emqx", "emqx",
				"--type", "json",
				"--patch", `[{"op": "replace", "path": "/spec/image", "value": "`+emqxImageUpgrade+`"}]`,
			)).To(Succeed())

			By("check EMQX cluster node evacuations status")
			Eventually(KubectlOut).
				WithArguments("get", "emqx", "emqx", "-o", "jsonpath={.status.nodeEvacuationsStatus}").
				ShouldNot(ContainSubstring("connection_eviction_rate"))

			By("wait for EMQX cluster to be ready again")
			Eventually(checkEMQXReady).WithArguments(changingTime).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			Eventually(checkReplicantStatus).WithArguments(replicantReplicas).Should(Succeed())

			By("check previous coreSet has been scaled down to 0")
			out, err := KubectlOut("get", "statefulset", stsList.Items[0].Name, "-o", "jsonpath={.status.replicas}")
			Expect(err).NotTo(HaveOccurred(), "Failed to get core StatefulSet")
			Expect(out).To(Equal("0"))

			By("check previous replicantSet has been scaled down to 0")
			out, err = KubectlOut("get", "replicaset", rsList.Items[0].Name, "-o", "jsonpath={.status.replicas}")
			Expect(err).NotTo(HaveOccurred(), "Failed to get replicant ReplicaSet")
			Expect(out).To(Equal("0"))
		})

		It("delete cluster", func() {
			Expect(Kubectl("delete", "emqx", "emqx")).To(Succeed())
			Expect(Kubectl("get", "emqx", "emqx")).To(HaveOccurred(), "EMQX cluster still exists")
		})
	})

	Context("EMQX Core-Replicant DS-Enabled Cluster", func() {
		// Initial number of core and replicant replicas:
		var coreReplicas int = 2
		var replicantReplicas int = 2

		It("deploy core-replicant EMQX cluster", func() {
			By("create EMQX cluster")
			emqxCR := PatchDocument(
				FromYAMLFile(emqxCRBasic),
				withImage(emqxImage),
				withCores(coreReplicas),
				withReplicants(replicantReplicas),
				withDS(),
			)
			Expect(KubectlStdin(emqxCR, "apply", "-f", "-")).To(Succeed())

			By("wait for EMQX cluster to be ready")
			Eventually(checkEMQXReady).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			Eventually(checkReplicantStatus).WithArguments(replicantReplicas).Should(Succeed())
			Eventually(checkDSReplicationStatus).WithArguments(coreReplicas).Should(Succeed())
			Eventually(checkDSReplicationHealthy).Should(Succeed())

			By("verify EMQX pods have relevant conditions")
			var pods corev1.PodList
			Expect(KubectlOut("get", "pods",
				"--selector", appsv2beta1.LabelsManagedByKey+"=emqx-operator",
				"-o", "json",
			)).To(UnmarshalInto(&pods), "Failed to list EMQX pods")
			Expect(pods.Items).To(HaveLen(4), "EMQX cluster does not have 4 pods")
			for _, pod := range pods.Items {
				if pod.Labels[appsv2beta1.LabelsDBRoleKey] == "core" {
					Expect(pod.Status.Conditions).To(ContainElement(And(
						HaveField("Type", Equal(appsv2beta1.DSReplicationSite)),
						HaveField("Status", Equal(corev1.ConditionTrue)),
					)))
				}
				if pod.Labels[appsv2beta1.LabelsDBRoleKey] == "replicant" {
					Expect(pod.Status.Conditions).To(ContainElement(And(
						HaveField("Type", Equal(appsv2beta1.DSReplicationSite)),
						HaveField("Status", Equal(corev1.ConditionFalse)),
					)))
				}
			}
		})

		It("scale up core EMQX cluster", func() {
			coreReplicas = 4
			scaleStartedAt := metav1.Now()
			By("change number of core replicas")
			Expect(Kubectl("patch", "emqx", "emqx",
				"--type", "json",
				"--patch", `[{"op": "replace", "path": "/spec/coreTemplate/spec/replicas", "value": 4}]`,
			)).To(Succeed())
			By("wait for EMQX cluster to be ready after scaling")
			Eventually(checkEMQXReady).WithArguments(scaleStartedAt).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			Eventually(checkReplicantStatus).WithArguments(replicantReplicas).Should(Succeed())
			Eventually(checkDSReplicationStatus).WithArguments(coreReplicas).Should(Succeed())
			Eventually(checkDSReplicationHealthy).Should(Succeed())
		})

		It("scale down core EMQX cluster", func() {
			coreReplicas = 2
			scaleStartedAt := metav1.Now()
			By("change number of core replicas")
			Expect(Kubectl("patch", "emqx", "emqx",
				"--type", "json",
				"--patch", `[{"op": "replace", "path": "/spec/coreTemplate/spec/replicas", "value": 2}]`,
			)).To(Succeed())
			By("wait for EMQX cluster to be ready after scaling")
			Eventually(checkEMQXReady).WithArguments(scaleStartedAt).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			Eventually(checkReplicantStatus).WithArguments(replicantReplicas).Should(Succeed())
			Eventually(checkDSReplicationStatus).WithArguments(coreReplicas).Should(Succeed())
			// EMQX 5.10.1: Lost sites are expected to hang around.
			// Eventually(checkDSReplicationHealthy).Should(Succeed())
		})

		It("perform a blue-green update", func() {
			By("fetch current core StatefulSet")
			var stsList appsv1.StatefulSetList
			coreRev, err := KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.coreNodesStatus.currentRevision}")
			Expect(err).NotTo(HaveOccurred(), "Failed to get EMQX status")
			Expect(KubectlOut("get", "statefulset",
				"--selector", appsv2beta1.LabelsPodTemplateHashKey+"="+coreRev,
				"-o", "json",
			)).To(UnmarshalInto(&stsList), "Failed to list statefulSets")

			By("change EMQX image + number of replicas")
			coreReplicas = 2
			changedAt := metav1.Now()
			Expect(Kubectl("patch", "emqx", "emqx",
				"--type", "json",
				"--patch", `[
					{"op": "replace", "path": "/spec/image", "value": "`+emqxImageUpgrade+`"},
					{"op": "replace", "path": "/spec/coreTemplate/spec/replicas", "value": 2}
				]`,
			)).To(Succeed())

			By("check new core StatefulSet is spinning up")
			Eventually(KubectlOut).
				WithArguments("get", "emqx", "emqx", "-o", "jsonpath={.status.coreNodesStatus.updateRevision}").
				ShouldNot(Equal(coreRev), "New StatefulSet has not been spun up")

			By("wait for EMQX cluster to be ready again")
			Eventually(checkEMQXReady).WithArguments(changedAt).Should(Succeed())
			Eventually(checkEMQXStatus).WithArguments(coreReplicas).Should(Succeed())
			Eventually(checkReplicantStatus).WithArguments(replicantReplicas).Should(Succeed())

			By("check previous coreSet has been scaled down to 0")
			Expect(KubectlOut("get", "statefulset", stsList.Items[0].Name, "-o", "jsonpath={.status.replicas}")).
				To(Equal("0"))

			By("wait for DS replication status to be stable")
			Eventually(checkDSReplicationStatus).WithArguments(coreReplicas).Should(Succeed())
			// EMQX 5.10.1: Lost sites are expected to hang around.
			// Eventually(checkDSReplicationHealthy).Should(Succeed())
		})

		It("delete core-replicant EMQX cluster", func() {
			Expect(Kubectl("delete", "emqx", "emqx")).To(Succeed())
			Expect(Kubectl("get", "emqx", "emqx")).To(HaveOccurred(), "EMQX cluster still exists")
		})

	})
})
