package controller

import (
	"time"

	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	. "github.com/emqx/emqx-operator/test/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Reconciler addCoreSet", Ordered, func() {
	var ns *corev1.Namespace
	var instance *appsv2beta1.EMQX
	var a *addCoreSet
	var round *reconcileRound

	BeforeAll(func() {
		// Create namespace:
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "controller-v2beta1-add-emqx-core-test",
				Labels: map[string]string{
					"test": "e2e",
				},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
		// Create instance:
		instance = emqx.DeepCopy()
		instance.Namespace = ns.Name
		Expect(k8sClient.Create(ctx, instance)).Should(Succeed())
	})

	BeforeEach(func() {
		// Mock instance status:
		instance.Status.Conditions = []metav1.Condition{
			{
				Type:               appsv2beta1.Ready,
				Status:             metav1.ConditionTrue,
				Reason:             appsv2beta1.Ready,
				LastTransitionTime: metav1.Time{Time: time.Now().AddDate(0, 0, -1)},
			},
			{
				Type:               appsv2beta1.CoreNodesReady,
				Status:             metav1.ConditionTrue,
				Reason:             appsv2beta1.CoreNodesReady,
				LastTransitionTime: metav1.Time{Time: time.Now().AddDate(0, 0, -1)},
			},
			{
				Type:               appsv2beta1.Initialized,
				Status:             metav1.ConditionTrue,
				Reason:             appsv2beta1.Initialized,
				LastTransitionTime: metav1.Time{Time: time.Now().AddDate(0, 0, -10)},
			},
		}
		// Instantiate reconciler:
		a = &addCoreSet{emqxReconciler}
		round = newReconcileRound()
		round.state = loadReconcileState(ctx, k8sClient, instance)
	})

	It("should create statefulSet", func() {
		Eventually(a.reconcile).WithArguments(round, instance).
			WithTimeout(timeout).
			WithPolling(interval).
			Should(Equal(subResult{}))

		Eventually(func() []appsv1.StatefulSet {
			list := &appsv1.StatefulSetList{}
			_ = k8sClient.List(ctx, list,
				client.InNamespace(instance.Namespace),
				client.MatchingLabels(appsv2beta1.DefaultCoreLabels(instance)),
			)
			return list.Items
		}).Should(ConsistOf(
			HaveField("Spec.Template.Spec.Containers", ConsistOf(HaveField("Image", Equal(instance.Spec.Image)))),
		))
	})

	It("change image creates new statefulSet", func() {
		instance.Spec.Image = "emqx/emqx"
		instance.Spec.UpdateStrategy.InitialDelaySeconds = int32(999999999)
		Eventually(a.reconcile).WithArguments(round, instance).
			WithTimeout(timeout).
			WithPolling(interval).
			Should(Equal(subResult{}))

		Eventually(func() []appsv1.StatefulSet {
			list := &appsv1.StatefulSetList{}
			_ = k8sClient.List(ctx, list,
				client.InNamespace(instance.Namespace),
				client.MatchingLabels(appsv2beta1.DefaultCoreLabels(instance)),
			)
			return list.Items
		}).WithTimeout(timeout).WithPolling(interval).Should(ConsistOf(
			HaveField("Spec.Template.Spec.Containers", ConsistOf(HaveField("Image", Equal("emqx")))),
			HaveField("Spec.Template.Spec.Containers", ConsistOf(HaveField("Image", Equal("emqx/emqx")))),
		))

		Eventually(func() *appsv2beta1.EMQX {
			_ = k8sClient.Get(ctx, client.ObjectKeyFromObject(instance), instance)
			return instance
		}).Should(And(
			WithTransform(
				func(emqx *appsv2beta1.EMQX) *metav1.Condition {
					return emqx.Status.GetLastTrueCondition()
				},
				HaveField("Type", Equal(appsv2beta1.Initialized)),
			),
			HaveCondition(appsv2beta1.Ready, HaveField("Status", Equal(metav1.ConditionFalse))),
			HaveCondition(appsv2beta1.CoreNodesReady, HaveField("Status", Equal(metav1.ConditionFalse))),
		))
	})

	AfterAll(func() {
		Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())
		Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
	})
})
