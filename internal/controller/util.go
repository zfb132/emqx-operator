package controller

import (
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"hash/fnv"
	"slices"
	"time"

	emperror "emperror.dev/errors"
	"github.com/cisco-open/k8s-objectmatcher/patch"
	"github.com/davecgh/go-spew/spew"
	appsv2beta1 "github.com/emqx/emqx-operator/api/v2beta1"
	"github.com/tidwall/gjson"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func getEventList(ctx context.Context, clientSet *kubernetes.Clientset, obj client.Object) []*corev1.Event {
	// https://github.com/kubernetes-sigs/kubebuilder/issues/547#issuecomment-450772300
	eventList, _ := clientSet.CoreV1().Events(obj.GetNamespace()).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + obj.GetName(),
	})
	return handlerEventList(eventList)
}

func handlerEventList(list *corev1.EventList) []*corev1.Event {
	listDeletes := []*corev1.Event{}
	for _, e := range list.Items {
		if e.Reason == "SuccessfulDelete" {
			listDeletes = append(listDeletes, e.DeepCopy())
		}
	}
	sortByLastTimestamp(listDeletes)
	return listDeletes
}

func checkInitialDelaySecondsReady(instance *appsv2beta1.EMQX) bool {
	_, condition := instance.Status.GetCondition(appsv2beta1.Available)
	if condition == nil || condition.Type != appsv2beta1.Available {
		return false
	}
	delay := time.Since(condition.LastTransitionTime.Time).Seconds()
	return int32(delay) > instance.Spec.UpdateStrategy.InitialDelaySeconds
}

func checkWaitTakeoverReady(instance *appsv2beta1.EMQX, eList []*corev1.Event) bool {
	if len(eList) == 0 {
		return true
	}

	lastEvent := eList[len(eList)-1]
	delay := time.Since(lastEvent.LastTimestamp.Time).Seconds()
	return int32(delay) > instance.Spec.UpdateStrategy.EvacuationStrategy.WaitTakeover
}

// JustCheckPodTemplate will check only the differences between the podTemplate of the two statefulSets
func justCheckPodTemplate() patch.CalculateOption {
	getPodTemplate := func(obj []byte) ([]byte, error) {
		podTemplateSpecJson := gjson.GetBytes(obj, "spec.template")
		podTemplateSpec := &corev1.PodTemplateSpec{}
		_ = json.Unmarshal([]byte(podTemplateSpecJson.String()), podTemplateSpec)

		// Remove the podTemplateHashLabelKey from the podTemplateSpec
		if _, ok := podTemplateSpec.Labels[appsv2beta1.LabelsPodTemplateHashKey]; ok {
			podTemplateSpec.Labels = appsv2beta1.CloneAndRemoveLabel(podTemplateSpec.Labels, appsv2beta1.LabelsPodTemplateHashKey)
		}

		emptyRs := &appsv1.ReplicaSet{}
		emptyRs.Spec.Template = *podTemplateSpec
		return json.Marshal(emptyRs)
	}

	return func(current, modified []byte) ([]byte, []byte, error) {
		current, err := getPodTemplate(current)
		if err != nil {
			return []byte{}, []byte{}, emperror.Wrap(err, "could not get pod template field from current byte sequence")
		}

		modified, err = getPodTemplate(modified)
		if err != nil {
			return []byte{}, []byte{}, emperror.Wrap(err, "could not get pod template field from modified byte sequence")
		}

		return current, modified, nil
	}
}

// IgnoreStatefulSetReplicas will ignore the `Replicas` field of the statefulSet
func ignoreStatefulSetReplicas() patch.CalculateOption {
	return func(current, modified []byte) ([]byte, []byte, error) {
		current, err := filterStatefulSetReplicasField(current)
		if err != nil {
			return []byte{}, []byte{}, emperror.Wrap(err, "could not filter replicas field from current byte sequence")
		}

		modified, err = filterStatefulSetReplicasField(modified)
		if err != nil {
			return []byte{}, []byte{}, emperror.Wrap(err, "could not filter replicas field from modified byte sequence")
		}

		return current, modified, nil
	}
}

func filterStatefulSetReplicasField(obj []byte) ([]byte, error) {
	sts := appsv1.StatefulSet{}
	err := json.Unmarshal(obj, &sts)
	if err != nil {
		return []byte{}, emperror.Wrap(err, "could not unmarshal byte sequence")
	}
	sts.Spec.Replicas = ptr.To(int32(1))
	obj, err = json.Marshal(sts)
	if err != nil {
		return []byte{}, emperror.Wrap(err, "could not marshal byte sequence")
	}

	return obj, nil
}

func compareCreationTimestamp(a, b client.Object) int {
	atime := a.GetCreationTimestamp()
	btime := b.GetCreationTimestamp()
	cmpTime := atime.Time.Compare(btime.Time)
	// Use name as a tie breaker:
	if cmpTime == 0 {
		return cmp.Compare(a.GetName(), b.GetName())
	}
	return cmpTime
}

func compareName(a, b client.Object) int {
	cmpName := cmp.Compare(a.GetName(), b.GetName())
	// Use creation timestamp as a tie breaker:
	if cmpName == 0 {
		atime := a.GetCreationTimestamp()
		btime := b.GetCreationTimestamp()
		return atime.Time.Compare(btime.Time)
	}
	return cmpName
}

func sortByCreationTimestamp[T client.Object](list []T) {
	slices.SortFunc(list, func(a, b T) int {
		return compareCreationTimestamp(a, b)
	})
}

func sortByName[T client.Object](list []T) {
	slices.SortFunc(list, func(a, b T) int {
		return compareName(a, b)
	})
}

func compareLastTimestamp(a, b *corev1.Event) int {
	cmpLastTime := a.LastTimestamp.Time.Compare(b.LastTimestamp.Time)
	if cmpLastTime == 0 {
		return compareCreationTimestamp(a, b)
	}
	return cmpLastTime
}

func sortByLastTimestamp(list []*corev1.Event) {
	slices.SortFunc(list, compareLastTimestamp)
}

// ComputeHash returns a hash value calculated from pod template and
// a collisionCount to avoid hash collision. The hash will be safe encoded to
// avoid bad words.
func computeHash(template *corev1.PodTemplateSpec, collisionCount *int32) string {
	templateSpecHasher := fnv.New32a()
	deepHashObject(templateSpecHasher, *template)

	// Add collisionCount in the hash if it exists.
	if collisionCount != nil {
		collisionCountBytes := make([]byte, 8)
		binary.LittleEndian.PutUint32(collisionCountBytes, uint32(*collisionCount))
		templateSpecHasher.Write(collisionCountBytes)
	}

	return rand.SafeEncodeString(fmt.Sprint(templateSpecHasher.Sum32()))
}

// DeepHashObject writes specified object to hash using the spew library
// which follows pointers and prints actual values of the nested objects
// ensuring the hash does not change when a pointer changes.
func deepHashObject(hasher hash.Hash, objectToWrite interface{}) {
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	_, _ = printer.Fprintf(hasher, "%#v", objectToWrite)
}
