package controller

import (
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type cleanupOutdatedSets struct {
	*EMQXReconciler
}

func (s *cleanupOutdatedSets) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	// Postpone cleanups until the instance is ready:
	if !instance.Status.IsConditionTrue(appsv2beta1.Ready) {
		return subResult{}
	}

	// List outdated replicantSets, preserving order by creation timestamp:
	currentRs := r.state.currentReplicantSet(instance)
	updateRs := r.state.updateReplicantSet(instance)
	prevRsList := []*appsv1.ReplicaSet{}
	for _, rs := range r.state.replicantSets {
		if rs.DeletionTimestamp == nil && rs != currentRs && rs != updateRs {
			prevRsList = append(prevRsList, rs)
		}
	}

	rsOutdated := len(prevRsList) - int(instance.Spec.RevisionHistoryLimit)
	for i := 0; i < rsOutdated; i++ {
		rs := prevRsList[i]
		// Avoid delete replica set with non-zero replica counts
		if rs.Status.Replicas != 0 || *(rs.Spec.Replicas) != 0 || rs.Generation > rs.Status.ObservedGeneration {
			continue
		}
		r.log.Info("removing outdated replicantSet", "replicaSet", klog.KObj(rs))
		if err := s.Client.Delete(r.ctx, rs); err != nil && !k8sErrors.IsNotFound(err) {
			return subResult{err: err}
		}
	}

	// List outdated coreSets, preserving order by creation timestamp:
	currentSts := r.state.currentCoreSet(instance)
	updateSts := r.state.updateCoreSet(instance)
	prevStsList := []*appsv1.StatefulSet{}
	for _, sts := range r.state.coreSets {
		if sts.DeletionTimestamp == nil && sts != currentSts && sts != updateSts {
			prevStsList = append(prevStsList, sts)
		}
	}

	stsOutdated := len(prevStsList) - int(instance.Spec.RevisionHistoryLimit)
	for i := 0; i < stsOutdated; i++ {
		sts := prevStsList[i]
		// Avoid delete stateful set with non-zero replica counts
		if sts.Status.Replicas != 0 || *(sts.Spec.Replicas) != 0 || sts.Generation > sts.Status.ObservedGeneration {
			continue
		}

		// Delete PVCs
		pvcList := &corev1.PersistentVolumeClaimList{}
		_ = s.Client.List(r.ctx, pvcList,
			client.InNamespace(instance.Namespace),
			client.MatchingLabels(sts.Spec.Selector.MatchLabels),
		)
		for _, p := range pvcList.Items {
			pvc := p.DeepCopy()
			if pvc.DeletionTimestamp != nil {
				continue
			}
			r.log.Info("removing persistentVolumeClaim of outdated coreSet",
				"persistentVolumeClaim", klog.KObj(pvc),
				"coreSet", klog.KObj(sts),
			)
			if err := s.Client.Delete(r.ctx, pvc); err != nil && !k8sErrors.IsNotFound(err) {
				return subResult{err: err}
			}
		}

		r.log.Info("removing outdated coreSet", "statefulSet", klog.KObj(sts))
		if err := s.Client.Delete(r.ctx, sts); err != nil && !k8sErrors.IsNotFound(err) {
			return subResult{err: err}
		}

	}

	return subResult{}
}
