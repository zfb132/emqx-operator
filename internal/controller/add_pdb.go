package controller

import (
	emperror "emperror.dev/errors"
	semver "github.com/Masterminds/semver/v3"
	crdv2 "github.com/emqx/emqx-operator/api/v2"
	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubeConfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

type addPdb struct {
	*EMQXReconciler
}

func (a *addPdb) reconcile(r *reconcileRound, instance *crdv2.EMQX) subResult {
	discoveryClient, _ := discovery.NewDiscoveryClientForConfig(kubeConfig.GetConfigOrDie())
	kubeVersion, _ := discoveryClient.ServerVersion()
	v, _ := semver.NewVersion(kubeVersion.String())

	pdbList := []client.Object{}
	if v.LessThan(semver.MustParse("1.21")) {
		corePdb, replPdb := generatePodDisruptionBudgetV1beta1(instance)
		pdbList = append(pdbList, corePdb)
		if replPdb != nil {
			pdbList = append(pdbList, replPdb)
		}
	} else {
		corePdb, replPdb := generatePodDisruptionBudget(instance)
		pdbList = append(pdbList, corePdb)
		if replPdb != nil {
			pdbList = append(pdbList, replPdb)
		}
	}

	if err := a.CreateOrUpdateList(r.ctx, a.Scheme, r.log, instance, pdbList); err != nil {
		return subResult{err: emperror.Wrap(err, "failed to create or update PDBs")}
	}
	return subResult{}
}

func generatePodDisruptionBudget(instance *crdv2.EMQX) (*policyv1.PodDisruptionBudget, *policyv1.PodDisruptionBudget) {
	corePdb := &policyv1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "policy/v1",
			Kind:       "PodDisruptionBudget",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: instance.Namespace,
			Name:      instance.CoreNamespacedName().Name,
			Labels:    instance.DefaultLabelsWith(instance.Labels),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: instance.DefaultLabelsWith(crdv2.CoreLabels(), instance.Spec.CoreTemplate.Labels),
			},
			MinAvailable:   instance.Spec.CoreTemplate.Spec.MinAvailable,
			MaxUnavailable: instance.Spec.CoreTemplate.Spec.MaxUnavailable,
		},
	}

	if instance.Spec.HasReplicants() {
		replPdb := corePdb.DeepCopy()
		replPdb.Name = instance.ReplicantNamespacedName().Name
		replPdb.Spec.Selector.MatchLabels = instance.DefaultLabelsWith(
			crdv2.ReplicantLabels(),
			instance.Spec.ReplicantTemplate.Labels,
		)
		replPdb.Spec.MinAvailable = instance.Spec.ReplicantTemplate.Spec.MinAvailable
		replPdb.Spec.MaxUnavailable = instance.Spec.ReplicantTemplate.Spec.MaxUnavailable
		return corePdb, replPdb
	}
	return corePdb, nil
}

func generatePodDisruptionBudgetV1beta1(instance *crdv2.EMQX) (*policyv1beta1.PodDisruptionBudget, *policyv1beta1.PodDisruptionBudget) {
	corePdb := &policyv1beta1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "policy/v1",
			Kind:       "PodDisruptionBudget",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: instance.Namespace,
			Name:      instance.CoreNamespacedName().Name,
			Labels:    instance.DefaultLabelsWith(instance.Labels),
		},
		Spec: policyv1beta1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: instance.DefaultLabelsWith(crdv2.CoreLabels(), instance.Spec.CoreTemplate.Labels),
			},
			MinAvailable:   instance.Spec.CoreTemplate.Spec.MinAvailable,
			MaxUnavailable: instance.Spec.CoreTemplate.Spec.MaxUnavailable,
		},
	}
	if instance.Spec.HasReplicants() {
		replPdb := corePdb.DeepCopy()
		replPdb.Name = instance.ReplicantNamespacedName().Name
		replPdb.Spec.Selector.MatchLabels = instance.DefaultLabelsWith(
			crdv2.ReplicantLabels(),
			instance.Spec.ReplicantTemplate.Labels,
		)
		replPdb.Spec.MinAvailable = instance.Spec.ReplicantTemplate.Spec.MinAvailable
		replPdb.Spec.MaxUnavailable = instance.Spec.ReplicantTemplate.Spec.MaxUnavailable
		return corePdb, replPdb
	}
	return corePdb, nil
}
