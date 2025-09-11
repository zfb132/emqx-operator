package api

import (
	"encoding/json"
	"fmt"

	emperror "emperror.dev/errors"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	req "github.com/emqx/emqx-operator/internal/requester"
)

func NodeInfo(req req.RequesterInterface, nodeName string) (*appsv2beta1.EMQXNode, error) {
	body, err := get(req, fmt.Sprintf("api/v5/nodes/%s", nodeName))
	if emperror.Is(err, ErrorNotFound) {
		return &appsv2beta1.EMQXNode{
			Node:       nodeName,
			NodeStatus: "stopped",
		}, nil
	}
	if err != nil {
		return nil, err
	}

	nodeInfo := &appsv2beta1.EMQXNode{}
	if err := json.Unmarshal(body, &nodeInfo); err != nil {
		return nil, emperror.Wrap(err, "unexpected node info format")
	}
	return nodeInfo, nil
}

func Nodes(req req.RequesterInterface) ([]appsv2beta1.EMQXNode, error) {
	body, err := get(req, "api/v5/nodes")
	if err != nil {
		return nil, err
	}

	nodeInfos := []appsv2beta1.EMQXNode{}
	if err := json.Unmarshal(body, &nodeInfos); err != nil {
		return nil, emperror.Wrap(err, "unexpected node info format")
	}
	return nodeInfos, nil
}
