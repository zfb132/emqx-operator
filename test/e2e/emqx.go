package e2e

import (
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	. "github.com/emqx/emqx-operator/test/util"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func checkEMQXReady(g Gomega, afterTime ...metav1.Time) {
	var cond metav1.Condition
	g.Expect(KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")]}")).
		To(UnmarshalInto(&cond), "Failed to get emqx status")
	g.Expect(cond.Status).To(
		Equal(metav1.ConditionTrue),
		"EMQX cluster has not become ready",
	)
	if len(afterTime) > 0 {
		g.Expect(cond.LastTransitionTime.After(afterTime[0].Time)).To(
			BeTrue(),
			"EMQX cluster has not become ready after specified time",
		)
	}
}

func checkEMQXStatus(g Gomega, coreReplicas int) {
	var podList corev1.PodList
	var status appsv2beta1.EMQXNodesStatus
	g.Expect(KubectlOut("get", "pod",
		"--selector", "apps.emqx.io/instance=emqx,apps.emqx.io/managed-by=emqx-operator",
		"-o", "json",
	)).To(UnmarshalInto(&podList), "Failed to list EMQX pods")
	g.Expect(podList.Items).To(
		HaveEach(
			HaveField("Status.Conditions", ContainElement(And(
				HaveField("Type", Equal(corev1.PodReady)),
				HaveField("Status", Equal(corev1.ConditionTrue)),
			))),
		),
		"Not all EMQX pods are ready",
	)
	g.Expect(KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.coreNodesStatus}")).
		To(UnmarshalInto(&status), "Failed to get EMQX status")
	g.Expect(status).To(
		And(
			HaveField("Replicas", BeEquivalentTo(coreReplicas)),
			HaveField("ReadyReplicas", BeEquivalentTo(coreReplicas)),
			HaveField("CurrentReplicas", BeEquivalentTo(coreReplicas)),
			HaveField("UpdateReplicas", BeEquivalentTo(coreReplicas)),
		),
		"EMQX status does not have expected number of core nodes",
	)
	checkNodesStatusRevision(g, status, "core", coreReplicas)
}

func checkNoReplicants(g Gomega) {
	g.Expect(KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.replicantNodesStatus}")).
		To(Equal("{}"), "EMQX cluster status has replicant nodes status")
	g.Expect(KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.replicantNodes}")).
		To(BeEmpty(), "EMQX cluster status lists replicant nodes")
}

func checkReplicantStatus(g Gomega, replicantReplicas int) {
	var status appsv2beta1.EMQXNodesStatus
	g.Expect(KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.replicantNodesStatus}")).
		To(UnmarshalInto(&status), "Failed to get EMQX replicant nodes status")
	g.Expect(status).To(
		And(
			HaveField("Replicas", BeEquivalentTo(replicantReplicas)),
			HaveField("ReadyReplicas", BeEquivalentTo(replicantReplicas)),
			HaveField("CurrentReplicas", BeEquivalentTo(replicantReplicas)),
			HaveField("UpdateReplicas", BeEquivalentTo(replicantReplicas)),
		),
		"EMQX status does not have expected number of replicant nodes",
	)
	checkNodesStatusRevision(g, status, "replicant", replicantReplicas)
}

func checkNodesStatusRevision(g Gomega, status appsv2beta1.EMQXNodesStatus, role string, replicas int) {
	var podList corev1.PodList
	g.Expect(status).To(
		And(
			HaveField("CurrentRevision", Not(BeEmpty())),
			HaveField("UpdateRevision", Not(BeEmpty())),
		),
		"EMQX %s nodes status does not have expected revision", role,
	)
	g.Expect(status.CurrentRevision).To(
		Equal(status.UpdateRevision),
		"EMQX %s nodes current and update revisions are different", role,
	)
	g.Expect(KubectlOut("get", "pods",
		"--selector", appsv2beta1.LabelsPodTemplateHashKey+"="+status.CurrentRevision,
		"--field-selector", "status.phase==Running",
		"-o", "json",
	)).To(UnmarshalInto(&podList), "Failed to list %s pods", role)
	g.Expect(podList.Items).To(
		HaveLen(replicas),
		"EMQX cluster does not have %d current revision %s pods", replicas, role,
	)
}

func checkDSReplicationStatus(g Gomega, coreReplicas int) {
	status := &appsv2beta1.DSReplicationStatus{}
	g.Expect(KubectlOut("get", "emqx", "emqx", "-o", "jsonpath={.status.dsReplication}")).
		To(UnmarshalInto(&status), "Failed to get emqx status")
	g.Expect(status.DBs).NotTo(BeEmpty(), "No DS database is present")
	g.Expect(status.DBs).To(
		HaveEach(And(
			HaveField("Name", Equal("messages")),
			HaveField("NumShards", BeEquivalentTo(8)),
			HaveField("NumShardReplicas", BeEquivalentTo(8*min(3, coreReplicas))),
			HaveField("LostShardReplicas", BeEquivalentTo(0)),
			HaveField("NumTransitions", BeEquivalentTo(0)),
			HaveField("MinReplicas", BeEquivalentTo(min(3, coreReplicas))),
			HaveField("MaxReplicas", BeEquivalentTo(min(3, coreReplicas))),
		)),
		"EMQX DS databases are not replicated correctly across %d core nodes", coreReplicas,
	)
}

func checkDSReplicationHealthy(g Gomega) {
	g.Expect(KubectlOut("exec", "service/emqx-listeners", "--", "emqx", "ctl", "ds", "info")).
		NotTo(
			ContainSubstring("(!)"),
			"EMQX DS replication is not healthy",
		)
}
