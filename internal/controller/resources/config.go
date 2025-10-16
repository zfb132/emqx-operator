package controller

import (
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	"github.com/emqx/emqx-operator/internal/controller/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const BaseConfigFile string = "base.hocon"
const OverridesConfigFile string = "emqx.conf"

const configVolumeName = "bootstrap-config"

type emqxConfigResource struct {
	*appsv2beta1.EMQX
}

func EMQXConfig(instance *appsv2beta1.EMQX) emqxConfigResource {
	return emqxConfigResource{instance}
}

func (from emqxConfigResource) ConfigMap() *corev1.ConfigMap {
	// NOTE
	// Providing empty 'emqx.conf' to make sure no user-defined configuration is ignored or
	// overridden during restarts.
	baseConfigData := from.BaseConfig()
	emqxConfData := ""
	return from.ConfigMapWithData(baseConfigData, emqxConfData)
}

func (from emqxConfigResource) ConfigMapWithData(baseConfigData string, overridesConfigData string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      from.ConfigsNamespacedName().Name,
			Namespace: from.Namespace,
			Labels:    appsv2beta1.CloneAndMergeMap(appsv2beta1.DefaultLabels(from.EMQX), from.Labels),
		},
		Data: map[string]string{
			BaseConfigFile:      baseConfigData,
			OverridesConfigFile: overridesConfigData,
		},
	}
}

func (from emqxConfigResource) BaseConfig() string {
	return config.WithDefaults(from.Spec.Config.Data)
}

func (from emqxConfigResource) DiffersFrom(configMap *corev1.ConfigMap) bool {
	return configMap.Data[BaseConfigFile] != from.BaseConfig() || configMap.Data[OverridesConfigFile] != ""
}

func (emqxConfigResource) VolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      configVolumeName,
			MountPath: "/opt/emqx/etc/" + BaseConfigFile,
			SubPath:   BaseConfigFile,
			ReadOnly:  true,
		},
		{
			Name:      configVolumeName,
			MountPath: "/opt/emqx/etc/" + OverridesConfigFile,
			SubPath:   OverridesConfigFile,
			ReadOnly:  true,
		},
	}
}

func (from emqxConfigResource) Volume() corev1.Volume {
	return corev1.Volume{
		Name: configVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: from.ConfigsNamespacedName().Name,
				},
			},
		},
	}
}
