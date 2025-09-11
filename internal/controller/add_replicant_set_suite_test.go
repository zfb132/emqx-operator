package controller

import (
	"time"

	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/emqx/emqx-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func actualInstance(instance *appsv2beta1.EMQX) *appsv2beta1.EMQX {
	_ = k8sClient.Get(ctx, client.ObjectKeyFromObject(instance), instance)
	return instance
}

func replicantSets(instance *appsv2beta1.EMQX) []appsv1.ReplicaSet {
	list := &appsv1.ReplicaSetList{}
	_ = k8sClient.List(ctx, list,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(appsv2beta1.DefaultReplicantLabels(instance)),
	)
	return list.Items
}

func adoptReplicantSet(instance *appsv2beta1.EMQX) *appsv1.ReplicaSet {
	list := replicantSets(instance)
	if len(list) == 0 {
		return nil
	}
	rs := list[0].DeepCopy()
	rsHash := rs.Labels[appsv2beta1.LabelsPodTemplateHashKey]
	instance.Status.ReplicantNodesStatus.UpdateRevision = rsHash
	return rs
}

func replicantSetsReconcileRound(instance *appsv2beta1.EMQX) *reconcileRound {
	round := newReconcileRound()
	round.state = loadReconcileState(ctx, k8sClient, instance)
	return round
}

var _ = Describe("Reconciler addReplicantSet", Ordered, func() {
	var a *addReplicantSet
	var instance *appsv2beta1.EMQX = new(appsv2beta1.EMQX)
	var ns *corev1.Namespace = &corev1.Namespace{}

	BeforeEach(func() {
		a = &addReplicantSet{emqxReconciler}

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "controller-v2beta1-add-emqx-repl-test",
				Labels: map[string]string{
					"test": "e2e",
				},
			},
		}

		instance = emqx.DeepCopy()
		instance.Namespace = ns.Name
		instance.Spec.ReplicantTemplate = &appsv2beta1.EMQXReplicantTemplate{
			Spec: appsv2beta1.EMQXReplicantTemplateSpec{
				Replicas: ptr.To(int32(3)),
			},
		}
		instance.Status = appsv2beta1.EMQXStatus{
			ReplicantNodesStatus: appsv2beta1.EMQXNodesStatus{
				Replicas: 3,
			},
			Conditions: []metav1.Condition{
				{
					Type:               appsv2beta1.Ready,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: time.Now().AddDate(0, 0, -1)},
				},
				{
					Type:               appsv2beta1.CoreNodesReady,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: time.Now().AddDate(0, 0, -1)},
				},
			},
		}
	})

	It("create namespace", func() {
		Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
	})

	Context("replicant template is nil", func() {
		It("should do nothing", func() {
			// Clear replicant template:
			instance.Spec.ReplicantTemplate = nil
			// Reconciliation step should do nothing and succeed:
			round := replicantSetsReconcileRound(instance)
			Eventually(a.reconcile).WithArguments(round, instance).
				WithTimeout(timeout).
				WithPolling(interval).
				Should(Equal(subResult{}))
			Eventually(replicantSets).WithArguments(instance).
				Should(BeEmpty())
		})
	})

	Context("core nodes is not ready", func() {
		It("should do nothing", func() {
			// Remove core nodes ready condition:
			instance.Status.RemoveCondition(appsv2beta1.CoreNodesReady)
			// Reconciliation step should succeed:
			round := replicantSetsReconcileRound(instance)
			Eventually(a.reconcile).WithArguments(round, instance).
				WithTimeout(timeout).
				WithPolling(interval).
				Should(Equal(subResult{}))
			Eventually(replicantSets).WithArguments(instance).
				Should(BeEmpty())
		})
	})

	Context("replicant template is not nil, and core code is ready", func() {

		It("should create replicaSet", func() {
			round := replicantSetsReconcileRound(instance)
			Eventually(a.reconcile).WithArguments(round, instance).
				WithTimeout(timeout).
				WithPolling(interval).
				Should(Equal(subResult{}))
			Eventually(replicantSets).WithArguments(instance).
				Should(ConsistOf(
					HaveField("Spec.Template.Spec.Containers", ConsistOf(
						HaveField("Image", Equal(instance.Spec.Image)),
					)),
				))
		})

	})

	Context("scale down replicas count", func() {
		BeforeAll(func() {
			Eventually(adoptReplicantSet).WithArguments(instance).Should(Not(BeNil()))
		})

		JustBeforeEach(func() {
			rs := adoptReplicantSet(instance)
			rs.Status.Replicas = 3
			Expect(k8sClient.Status().Update(ctx, rs)).Should(Succeed())
			Eventually(func() *appsv1.ReplicaSet {
				_ = k8sClient.Get(ctx, client.ObjectKeyFromObject(rs), rs)
				return rs
			}).WithTimeout(timeout).WithPolling(interval).Should(
				HaveField("Status.Replicas", BeEquivalentTo(3)),
			)
		})

		It("should update replicaSet", func() {
			// Set replicas count to 0:
			instance.Spec.ReplicantTemplate.Spec.Replicas = ptr.To(int32(0))
			// Reconciliation step should succeed:
			round := replicantSetsReconcileRound(instance)
			Eventually(a.reconcile).WithArguments(round, instance).
				WithTimeout(timeout).
				WithPolling(interval).
				Should(Equal(subResult{}))
			// ReplicaSet should be updated in place:
			Eventually(replicantSets).WithArguments(instance).
				Should(ConsistOf(
					HaveField("Spec.Replicas", HaveValue(BeEquivalentTo(0))),
				))
		})

		AfterAll(func() {
			Eventually(adoptReplicantSet).WithArguments(instance).Should(Not(BeNil()))
		})

	})

	Context("scale up replicas count", func() {
		BeforeAll(func() {
			Eventually(adoptReplicantSet).WithArguments(instance).Should(Not(BeNil()))
		})

		It("should update replicaSet", func() {
			// Set replicas count to 4:
			instance.Spec.ReplicantTemplate.Spec.Replicas = ptr.To(int32(4))
			// Reconciliation step should succeed:
			round := replicantSetsReconcileRound(instance)
			Eventually(a.reconcile).WithArguments(round, instance).
				WithTimeout(timeout).
				WithPolling(interval).
				Should(Equal(subResult{}))
			// ReplicaSet should be updated:
			Eventually(replicantSets).WithArguments(instance).
				Should(ConsistOf(
					HaveField("Spec.Replicas", HaveValue(BeEquivalentTo(4))),
				))
			// Status conditions should reset to `ReplicantNodesProgressing`:
			Eventually(actualInstance).WithArguments(instance).
				Should(And(
					WithTransform(func(emqx *appsv2beta1.EMQX) string { return emqx.Status.GetLastTrueCondition().Type }, Equal(appsv2beta1.ReplicantNodesProgressing)),
					HaveCondition(appsv2beta1.Ready, BeNil()),
					HaveCondition(appsv2beta1.Available, BeNil()),
					HaveCondition(appsv2beta1.ReplicantNodesReady, BeNil()),
				))
		})
	})

	Context("change image", func() {
		BeforeAll(func() {
			Eventually(adoptReplicantSet).WithArguments(instance).Should(Not(BeNil()))
		})

		It("should create new replicaSet", func() {
			// Introduce changes that require creating a new replicaSet:
			instance.Spec.Image = "emqx/emqx"
			instance.Spec.UpdateStrategy.InitialDelaySeconds = int32(999999999)
			// Reconciliation step should succeed:
			round := replicantSetsReconcileRound(instance)
			Eventually(a.reconcile).WithArguments(round, instance).
				WithTimeout(timeout).
				WithPolling(interval).
				Should(Equal(subResult{}))
			// There should be two replicaSets soon:
			Eventually(replicantSets).WithArguments(instance).
				Should(ConsistOf(
					HaveField("Spec.Template.Spec.Containers", ConsistOf(HaveField("Image", Equal(emqx.Spec.Image)))),
					HaveField("Spec.Template.Spec.Containers", ConsistOf(HaveField("Image", Equal(instance.Spec.Image)))),
				))
			// Status conditions should reset to `ReplicantNodesProgressing`:
			Eventually(actualInstance).WithArguments(instance).
				Should(And(
					WithTransform(func(emqx *appsv2beta1.EMQX) string { return emqx.Status.GetLastTrueCondition().Type }, Equal(appsv2beta1.ReplicantNodesProgressing)),
					HaveCondition(appsv2beta1.Ready, BeNil()),
					HaveCondition(appsv2beta1.Available, BeNil()),
					HaveCondition(appsv2beta1.ReplicantNodesReady, BeNil()),
				))
		})
	})

	It("delete namespace", func() {
		Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
	})
})
