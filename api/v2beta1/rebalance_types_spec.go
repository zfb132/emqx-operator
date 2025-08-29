/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v2beta1

type RebalanceSpec struct {
	// InstanceKind is used to distinguish between EMQX and EMQXEnterprise.
	// When it is set to "EMQX", it means that the EMQX CR is v2beta1,
	// and when it is set to "EmqxEnterprise", it means that the EmqxEnterprise CR is v1beta4.
	// +kubebuilder:default:="EMQX"
	InstanceKind string `json:"instanceKind"`
	// InstanceName represents the name of EMQX CR, just work for EMQX Enterprise
	// +kubebuilder:validation:Required
	InstanceName string `json:"instanceName"`
	// RebalanceStrategy represents the strategy of EMQX rebalancing
	// More info: https://docs.emqx.com/en/emqx/v5.10/deploy/cluster/rebalancing.html#rebalancing
	// +kubebuilder:validation:Required
	RebalanceStrategy RebalanceStrategy `json:"rebalanceStrategy"`
}

type RebalanceStrategy struct {
	// Represents the source node client disconnect rate per second.
	// Same as `conn-evict-rate` in [EMQX Rebalancing](https://docs.emqx.com/en/emqx/v5.10/deploy/cluster/rebalancing.html#rebalancing).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	ConnEvictRate int32 `json:"connEvictRate"`

	// Represents the source node session evacuation rate per second.
	// Same as `sess-evict-rate` in [EMQX Rebalancing](https://docs.emqx.com/en/emqx/v5.10/deploy/cluster/rebalancing.html#rebalancing).
	// +kubebuilder:default:=500
	// +kubebuilder:validation:Minimum=1
	SessEvictRate int32 `json:"sessEvictRate,omitempty"`

	// Represents the time in seconds to wait for a client to
	// reconnect to take over the session after all connections are disconnected.
	// Same as `wait-takeover` in [EMQX Rebalancing](https://docs.emqx.com/en/emqx/v5.10/deploy/cluster/rebalancing.html#rebalancing).
	// +kubebuilder:default:=60
	// +kubebuilder:validation:Minimum=0
	WaitTakeover int32 `json:"waitTakeover,omitempty"`

	// Represents the time (in seconds) to wait for the LB to remove the source node from the list of active backend nodes.
	// After the specified waiting time is exceeded, the rebalancing task will start.
	// Same as `wait-health-check` in [EMQX Rebalancing](https://docs.emqx.com/en/emqx/v5.10/deploy/cluster/rebalancing.html#rebalancing).
	// +kubebuilder:default:=60
	// +kubebuilder:validation:Minimum=0
	WaitHealthCheck int32 `json:"waitHealthCheck,omitempty"`

	// Represents the absolute threshold for checking connection balance.
	// Same as `abs-conn-threshold` in [EMQX Rebalancing](https://docs.emqx.com/en/emqx/v5.10/deploy/cluster/rebalancing.html#rebalancing).
	// +kubebuilder:default:=1000
	// +kubebuilder:validation:Minimum=1
	AbsConnThreshold int32 `json:"absConnThreshold,omitempty"`

	// Represents the relative threshold for checkin connection balance.
	// Same as `rel-conn-threshold` in [EMQX Rebalancing](https://docs.emqx.com/en/emqx/v5.10/deploy/cluster/rebalancing.html#rebalancing).
	// Using floats is highly [discouraged](https://github.com/kubernetes-sigs/controller-tools/issues/245), defined as a _string_ instead.
	// Must be greater than 1.0.
	// +kubebuilder:default:="1.1"
	// +kubebuilder:validation:Pattern=`^[1-9]{1,8}\.[0-9]{1,8}$`
	RelConnThreshold string `json:"relConnThreshold,omitempty"`

	// Represents the absolute threshold for checking session connection balance.
	// Same as `abs-sess-threshold` in [EMQX Rebalancing](https://docs.emqx.com/en/emqx/v5.10/deploy/cluster/rebalancing.html#rebalancing)
	// +kubebuilder:default:=1000
	// +kubebuilder:validation:Minimum=1
	AbsSessThreshold int32 `json:"absSessThreshold,omitempty"`

	// Represents the relative threshold for checking session connection balance.
	// Same as `rel-sess-threshold` in [EMQX Rebalancing](https://docs.emqx.com/en/emqx/v5.10/deploy/cluster/rebalancing.html#rebalancing).
	// Using floats is highly [discouraged](https://github.com/kubernetes-sigs/controller-tools/issues/245), defined as a _string_ instead.
	// Must be greater than 1.0.
	// +kubebuilder:default:="1.1"
	// +kubebuilder:validation:Pattern=`^[1-9]{1,8}\.[0-9]{1,8}$`
	RelSessThreshold string `json:"relSessThreshold,omitempty"`
}
