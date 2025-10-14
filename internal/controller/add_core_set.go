package controller

import (
	"fmt"
	"reflect"
	"strconv"

	emperror "emperror.dev/errors"
	"github.com/cisco-open/k8s-objectmatcher/patch"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	config "github.com/emqx/emqx-operator/internal/controller/config"
	util "github.com/emqx/emqx-operator/internal/controller/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type addCoreSet struct {
	*EMQXReconciler
}

func (a *addCoreSet) reconcile(r *reconcileRound, instance *appsv2beta1.EMQX) subResult {
	sts := getNewStatefulSet(instance, r.conf)
	stsHash := sts.Labels[appsv2beta1.LabelsPodTemplateHashKey]

	needCreate := false
	updateCoreSet := r.state.updateCoreSet(instance)
	if updateCoreSet == nil {
		r.log.Info("creating new statefulSet",
			"statefulSet", klog.KObj(sts),
			"reason", "no existing statefulSet",
		)
		needCreate = true
	} else {
		patchResult, _ := a.Patcher.Calculate(updateCoreSet, sts, justCheckPodTemplate())
		if !patchResult.IsEmpty() {
			r.log.Info("creating new statefulSet",
				"statefulSet", klog.KObj(sts),
				"reason", "pod template has changed",
				"patch", string(patchResult.Patch),
			)
			needCreate = true
		}
	}

	if needCreate {
		_ = ctrl.SetControllerReference(instance, sts, a.Scheme)
		if err := a.Handler.Create(r.ctx, sts); err != nil {
			if k8sErrors.IsAlreadyExists(emperror.Cause(err)) {
				cond := instance.Status.GetLastTrueCondition()
				if cond != nil && cond.Type != appsv2beta1.Available && cond.Type != appsv2beta1.Ready {
					// Sometimes the updated statefulSet will not be ready, because the EMQX node can not be started.
					// And then we will rollback EMQX CR spec, the EMQX operator controller will create a new statefulSet.
					// But the new statefulSet will be the same as the previous one, so we didn't need to create it, just change the EMQX status.
					if stsHash == instance.Status.CoreNodesStatus.CurrentRevision {
						_ = a.updateEMQXStatus(r, instance, "RevertStatefulSet", stsHash)
						return subResult{}
					}
				}
				if instance.Status.CoreNodesStatus.CollisionCount == nil {
					instance.Status.CoreNodesStatus.CollisionCount = ptr.To(int32(0))
				}
				*instance.Status.CoreNodesStatus.CollisionCount++
				_ = a.Client.Status().Update(r.ctx, instance)
				return subResult{result: ctrl.Result{Requeue: true}}
			}
			return subResult{err: emperror.Wrap(err, "failed to create statefulSet")}
		}
		updateResult := a.updateEMQXStatus(r, instance, "CreateNewStatefulSet", stsHash)
		return subResult{err: updateResult}
	}

	sts.ObjectMeta = updateCoreSet.ObjectMeta
	sts.Spec.Template.ObjectMeta = updateCoreSet.Spec.Template.ObjectMeta
	sts.Spec.Selector = updateCoreSet.Spec.Selector
	patchResult, _ := a.Patcher.Calculate(
		updateCoreSet,
		sts,
		// Ignore Status fields and VolumeClaimTemplate stuff.
		patch.IgnoreStatusFields(),
		patch.IgnoreVolumeClaimTemplateTypeMetaAndStatus(),
		// Ignore if number of replicas has changed.
		// Reconciler `syncCoreSets` will handle scaling up and down of the existing statefulSet.
		ignoreStatefulSetReplicas(),
	)
	if !patchResult.IsEmpty() {
		// Update statefulSet
		r.log.Info("updating statefulSet",
			"statefulSet", klog.KObj(sts),
			"reason", "statefulSet has changed",
			"patch", string(patchResult.Patch),
		)
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			storage := &appsv1.StatefulSet{}
			_ = a.Client.Get(r.ctx, client.ObjectKeyFromObject(sts), storage)
			sts.ResourceVersion = storage.ResourceVersion
			return a.Handler.Update(r.ctx, sts)
		}); err != nil {
			return subResult{err: emperror.Wrap(err, "failed to update statefulSet")}
		}
		updateResult := a.updateEMQXStatus(r, instance, "UpdateStatefulSet", stsHash)
		return subResult{err: updateResult}
	}
	return subResult{}
}

func (a *addCoreSet) updateEMQXStatus(r *reconcileRound, instance *appsv2beta1.EMQX, reason, podTemplateHash string) error {
	instance.Status.ResetConditions(reason)
	instance.Status.CoreNodesStatus.UpdateRevision = podTemplateHash
	return a.Client.Status().Update(r.ctx, instance)
}

func getNewStatefulSet(instance *appsv2beta1.EMQX, conf *config.Conf) *appsv1.StatefulSet {
	svcPorts := conf.GetDashboardServicePort()
	preSts := generateStatefulSet(instance)
	podTemplateSpecHash := computeHash(preSts.Spec.Template.DeepCopy(), instance.Status.CoreNodesStatus.CollisionCount)
	preSts.Name = preSts.Name + "-" + podTemplateSpecHash
	preSts.Labels = appsv2beta1.CloneAndAddLabel(preSts.Labels, appsv2beta1.LabelsPodTemplateHashKey, podTemplateSpecHash)
	preSts.Spec.Selector = appsv2beta1.CloneSelectorAndAddLabel(preSts.Spec.Selector, appsv2beta1.LabelsPodTemplateHashKey, podTemplateSpecHash)
	preSts.Spec.Template.Labels = appsv2beta1.CloneAndAddLabel(preSts.Spec.Template.Labels, appsv2beta1.LabelsPodTemplateHashKey, podTemplateSpecHash)
	preSts.Spec.Template.Spec.Containers[0].Ports = util.MergeContainerPorts(
		preSts.Spec.Template.Spec.Containers[0].Ports,
		util.MapServicePortsToContainerPorts(svcPorts),
	)
	for _, p := range preSts.Spec.Template.Spec.Containers[0].Ports {
		if p.Name == "dashboard" {
			preSts.Spec.Template.Spec.Containers[0].Env = append([]corev1.EnvVar{
				{Name: "EMQX_DASHBOARD__LISTENERS__HTTP__BIND", Value: strconv.Itoa(int(p.ContainerPort))},
			}, preSts.Spec.Template.Spec.Containers[0].Env...)
		}
		if p.Name == "dashboard-https" {
			preSts.Spec.Template.Spec.Containers[0].Env = append([]corev1.EnvVar{
				{Name: "EMQX_DASHBOARD__LISTENERS__HTTPS__BIND", Value: strconv.Itoa(int(p.ContainerPort))},
			}, preSts.Spec.Template.Spec.Containers[0].Env...)
		}
	}
	return preSts
}

func generateStatefulSet(instance *appsv2beta1.EMQX) *appsv1.StatefulSet {
	labels := appsv2beta1.CloneAndMergeMap(
		appsv2beta1.DefaultCoreLabels(instance),
		instance.Spec.CoreTemplate.Labels,
	)

	// Add a PreStop hook to leave the cluster when the pod is asked to stop.
	// This is especially important when DS Raft is enabled, otherwise there will be a
	// lot of leftover records in the DS cluster metadata.
	lifecycle := instance.Spec.CoreTemplate.Spec.Lifecycle
	if lifecycle == nil {
		lifecycle = &corev1.Lifecycle{}
	} else {
		lifecycle = lifecycle.DeepCopy()
	}
	lifecycle.PreStop = &corev1.LifecycleHandler{
		Exec: &corev1.ExecAction{
			Command: []string{"/bin/sh", "-c", "emqx ctl cluster leave"},
		},
	}

	sts := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   instance.Namespace,
			Name:        instance.CoreNamespacedName().Name,
			Annotations: instance.Spec.CoreTemplate.DeepCopy().Annotations,
			Labels:      labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: instance.HeadlessServiceNamespacedName().Name,
			Replicas:    instance.Spec.CoreTemplate.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: instance.Spec.CoreTemplate.DeepCopy().Annotations,
					Labels:      labels,
				},
				Spec: corev1.PodSpec{
					ReadinessGates: []corev1.PodReadinessGate{
						{
							ConditionType: appsv2beta1.PodOnServing,
						},
					},
					ImagePullSecrets:          instance.Spec.ImagePullSecrets,
					ServiceAccountName:        instance.Spec.ServiceAccountName,
					SecurityContext:           instance.Spec.CoreTemplate.Spec.PodSecurityContext,
					Affinity:                  instance.Spec.CoreTemplate.Spec.Affinity,
					Tolerations:               instance.Spec.CoreTemplate.Spec.Tolerations,
					TopologySpreadConstraints: instance.Spec.CoreTemplate.Spec.TopologySpreadConstraints,
					NodeName:                  instance.Spec.CoreTemplate.Spec.NodeName,
					NodeSelector:              instance.Spec.CoreTemplate.Spec.NodeSelector,
					InitContainers:            instance.Spec.CoreTemplate.Spec.InitContainers,
					Containers: append([]corev1.Container{
						{
							Name:            appsv2beta1.DefaultContainerName,
							Image:           instance.Spec.Image,
							ImagePullPolicy: instance.Spec.ImagePullPolicy,
							Command:         instance.Spec.CoreTemplate.Spec.Command,
							Args:            instance.Spec.CoreTemplate.Spec.Args,
							Ports:           instance.Spec.CoreTemplate.Spec.Ports,
							Env: append([]corev1.EnvVar{
								{
									Name: "POD_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.name",
										},
									},
								},
								{
									Name:  "EMQX_CLUSTER__DISCOVERY_STRATEGY",
									Value: "dns",
								},
								{
									Name:  "EMQX_CLUSTER__DNS__RECORD_TYPE",
									Value: "srv",
								},
								{
									Name:  "EMQX_CLUSTER__DNS__NAME",
									Value: fmt.Sprintf("%s.%s.svc.%s", instance.HeadlessServiceNamespacedName().Name, instance.Namespace, instance.Spec.ClusterDomain),
								},
								{
									Name:  "EMQX_HOST",
									Value: "$(POD_NAME).$(EMQX_CLUSTER__DNS__NAME)",
								},
								{
									Name:  "EMQX_NODE__DATA_DIR",
									Value: "data",
								},
								{
									Name:  "EMQX_NODE__ROLE",
									Value: "core",
								},
								{
									Name: "EMQX_NODE__COOKIE",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: instance.NodeCookieNamespacedName().Name,
											},
											Key: "node_cookie",
										},
									},
								},
								{
									Name:  "EMQX_API_KEY__BOOTSTRAP_FILE",
									Value: `"/opt/emqx/data/bootstrap_api_key"`,
								},
							}, instance.Spec.CoreTemplate.Spec.Env...),
							EnvFrom:         instance.Spec.CoreTemplate.Spec.EnvFrom,
							Resources:       instance.Spec.CoreTemplate.Spec.Resources,
							SecurityContext: instance.Spec.CoreTemplate.Spec.ContainerSecurityContext,
							LivenessProbe:   instance.Spec.CoreTemplate.Spec.LivenessProbe,
							ReadinessProbe:  instance.Spec.CoreTemplate.Spec.ReadinessProbe,
							StartupProbe:    instance.Spec.CoreTemplate.Spec.StartupProbe,
							Lifecycle:       lifecycle,
							VolumeMounts: append([]corev1.VolumeMount{
								{
									Name:      "bootstrap-api-key",
									MountPath: "/opt/emqx/data/bootstrap_api_key",
									SubPath:   "bootstrap_api_key",
									ReadOnly:  true,
								},
								{
									Name:      "bootstrap-config",
									MountPath: "/opt/emqx/etc/" + appsv2beta1.BaseConfigFile,
									SubPath:   appsv2beta1.BaseConfigFile,
									ReadOnly:  true,
								},
								{
									Name:      instance.CoreNamespacedName().Name + "-log",
									MountPath: "/opt/emqx/log",
								},
								{
									Name:      instance.CoreNamespacedName().Name + "-data",
									MountPath: "/opt/emqx/data",
								},
							}, instance.Spec.CoreTemplate.Spec.ExtraVolumeMounts...),
						},
					}, instance.Spec.CoreTemplate.Spec.ExtraContainers...),
					Volumes: append([]corev1.Volume{
						{
							Name: "bootstrap-api-key",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: instance.BootstrapAPIKeyNamespacedName().Name,
								},
							},
						},
						{
							Name: "bootstrap-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: instance.ConfigsNamespacedName().Name,
									},
								},
							},
						},
						{
							Name: instance.CoreNamespacedName().Name + "-log",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					}, instance.Spec.CoreTemplate.Spec.ExtraVolumes...),
				},
			},
		},
	}

	if !reflect.ValueOf(instance.Spec.CoreTemplate.Spec.VolumeClaimTemplates).IsZero() {
		volumeClaimTemplates := instance.Spec.CoreTemplate.Spec.VolumeClaimTemplates.DeepCopy()
		if volumeClaimTemplates.VolumeMode == nil {
			// Wait https://github.com/cisco-open/k8s-objectmatcher/issues/51 fixed
			fs := corev1.PersistentVolumeFilesystem
			volumeClaimTemplates.VolumeMode = &fs
		}
		sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instance.CoreNamespacedName().Name + "-data",
					Namespace: instance.Namespace,
					Labels:    labels,
				},
				Spec: *volumeClaimTemplates,
			},
		}
	} else {
		sts.Spec.Template.Spec.Volumes = append([]corev1.Volume{
			{
				Name: instance.CoreNamespacedName().Name + "-data",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		}, sts.Spec.Template.Spec.Volumes...)
	}

	return sts
}
