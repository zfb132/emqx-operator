package api

import (
	"encoding/json"
	"fmt"
	"strings"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	req "github.com/emqx/emqx-operator/internal/requester"
	corev1 "k8s.io/api/core/v1"
)

type nodeEvacuationStatus struct {
	Evacuations []appsv2beta1.NodeEvacuationStatus `json:"evacuations"`
}

func NodeEvacuationStatus(req req.RequesterInterface) ([]appsv2beta1.NodeEvacuationStatus, error) {
	body, err := get(req, "api/v5/load_rebalance/global_status")
	if err != nil {
		return nil, err
	}

	nodeEvacuationStatus := nodeEvacuationStatus{}
	if err := json.Unmarshal(body, &nodeEvacuationStatus); err != nil {
		return nil, emperror.Wrap(err, "unexpected node evacuation status format")
	}
	return nodeEvacuationStatus.Evacuations, nil
}

func StartEvacuation(
	r req.RequesterInterface,
	strategy appsv2beta1.EvacuationStrategy,
	migrateTo []string,
	nodeName string,
) error {
	path := fmt.Sprintf("api/v5/load_rebalance/%s/evacuation/start", nodeName)
	request := map[string]interface{}{
		"conn_evict_rate": strategy.ConnEvictRate,
		"sess_evict_rate": strategy.SessEvictRate,
		"migrate_to":      migrateTo,
	}
	if strategy.WaitTakeover > 0 {
		request["wait_takeover"] = strategy.WaitTakeover
	}
	// Specify `wait_health_check` if different from the default.
	if strategy.WaitHealthCheck != 60 {
		request["wait_health_check"] = fmt.Sprintf("%ds", strategy.WaitHealthCheck)
	}

	body, _ := json.Marshal(request)
	_, err := post(r, path, body)

	// TODO:
	// the api/v5/load_rebalance/global_status have some bugs, so we need to ignore the 400 error
	var apiErr apiError
	if ok := emperror.As(err, &apiErr); ok {
		if apiErr.StatusCode == 400 && strings.Contains(apiErr.Message, "already_started") {
			return nil
		}
	}
	if err != nil {
		return err
	}
	return nil
}

func AvailabilityCheck(req req.RequesterInterface) corev1.ConditionStatus {
	_, err := get(req, "api/v5/load_rebalance/availability_check")
	if err != nil && emperror.Is(err, ErrorServiceUnavailable) {
		return corev1.ConditionFalse
	}
	if err != nil {
		return corev1.ConditionUnknown
	}
	return corev1.ConditionTrue
}
