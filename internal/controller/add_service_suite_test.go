package controller

import (
	crdv2 "github.com/emqx/emqx-operator/api/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Reconciler addService", Ordered, func() {
	var a *addService
	var instance *crdv2.EMQX = &crdv2.EMQX{}
	var ns *corev1.Namespace = &corev1.Namespace{}

	BeforeEach(func() {
		a = &addService{emqxReconciler}

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "controller-v2beta1-add-headless-svc-test",
				Labels: map[string]string{
					"test": "e2e",
				},
			},
		}

		instance = emqx.DeepCopy()
		instance.Namespace = ns.Name
		instance.Spec.CoreTemplate = crdv2.EMQXCoreTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"test": "label"},
			},
		}
	})

	It("create namespace", func() {
		Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
	})

	It("generate svc", func() {
		Eventually(a.reconcile).WithArguments(newReconcileRound(), instance).
			WithTimeout(timeout).
			WithPolling(interval).
			Should(Equal(subResult{}))

		Eventually(func() *corev1.Service {
			svc := &corev1.Service{}
			_ = k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "emqx-dashboard"}, svc)
			return svc
		}).Should(Not(BeNil()))

		Eventually(func() *corev1.Service {
			svc := &corev1.Service{}
			_ = k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "emqx-listeners"}, svc)
			return svc
		}).Should(Not(BeNil()))
	})
})
