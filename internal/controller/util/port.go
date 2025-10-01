package controller

import (
	corev1 "k8s.io/api/core/v1"
)

func MergeServicePorts(ports1, ports2 []corev1.ServicePort) []corev1.ServicePort {
	ports := append(ports1, ports2...)

	result := make([]corev1.ServicePort, 0, len(ports))
	tempName := map[string]struct{}{}
	tempPort := map[int32]struct{}{}

	for _, item := range ports {
		_, nameOK := tempName[item.Name]
		_, portOK := tempPort[item.Port]

		if !nameOK && !portOK {
			tempName[item.Name] = struct{}{}
			tempPort[item.Port] = struct{}{}
			result = append(result, item)
		}
	}

	return result
}

func MergeContainerPorts(ports1, ports2 []corev1.ContainerPort) []corev1.ContainerPort {
	ports := append(ports1, ports2...)

	result := make([]corev1.ContainerPort, 0, len(ports))
	tempName := map[string]struct{}{}
	tempPort := map[int32]struct{}{}

	for _, item := range ports {
		_, nameOK := tempName[item.Name]
		_, portOK := tempPort[item.ContainerPort]

		if !nameOK && !portOK {
			tempName[item.Name] = struct{}{}
			tempPort[item.ContainerPort] = struct{}{}
			result = append(result, item)
		}
	}

	return result
}

func MapServicePortsToContainerPorts(ports []corev1.ServicePort) []corev1.ContainerPort {
	result := make([]corev1.ContainerPort, 0, len(ports))
	for _, item := range ports {
		result = append(result, corev1.ContainerPort{
			Name:          item.Name,
			ContainerPort: item.Port,
			Protocol:      item.Protocol,
		})
	}
	return result
}
