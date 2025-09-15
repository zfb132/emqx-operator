package controller

import (
	"testing"
	"time"

	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCheckInitialDelaySecondsReady(t *testing.T) {
	assert.False(t, checkInitialDelaySecondsReady(&appsv2beta1.EMQX{}))

	assert.False(t, checkInitialDelaySecondsReady(&appsv2beta1.EMQX{
		Spec: appsv2beta1.EMQXSpec{
			UpdateStrategy: appsv2beta1.UpdateStrategy{
				InitialDelaySeconds: 999999999,
			},
		},
		Status: appsv2beta1.EMQXStatus{
			Conditions: []metav1.Condition{
				{
					Type:               appsv2beta1.Available,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: time.Now()},
				},
			},
		},
	}))

	assert.True(t, checkInitialDelaySecondsReady(&appsv2beta1.EMQX{
		Spec: appsv2beta1.EMQXSpec{
			UpdateStrategy: appsv2beta1.UpdateStrategy{
				InitialDelaySeconds: 0,
			},
		},
		Status: appsv2beta1.EMQXStatus{
			Conditions: []metav1.Condition{
				{
					Type:               appsv2beta1.Available,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: time.Now().AddDate(0, 0, -1)},
				},
			},
		},
	}))
}
