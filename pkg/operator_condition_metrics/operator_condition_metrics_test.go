package operator_condition_metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// helper: make a minimal client.Object with Kind/Name/Namespace set.
// metav1.PartialObjectMetadata satisfies client.Object and lets us set GVK.
func makeObj(kind, name, namespace string) *metav1.PartialObjectMetadata {
	obj := &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			Kind: kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	// Set Kind explicitly (GetObjectKind().GroupVersionKind().Kind reads this)
	obj.GetObjectKind().SetGroupVersionKind(metav1.SchemeGroupVersion.WithKind(kind))
	return obj
}

func TestConditionMetricRecorder_Record_Transition_And_SecondCondition(t *testing.T) {
	gauge := NewOperatorConditionsGauge("test_record_transition_and_second_condition")
	_ = controllermetrics.Registry.Register(gauge)

	// Arrange
	rec := &ConditionMetricRecorder{
		Controller:              "my-controller",
		OperatorConditionsGauge: gauge,
	}
	kind := "MyCRD"
	name := "cr-1"
	ns := "prod"
	obj := makeObj(kind, name, ns)

	// Record Ready=True
	rec.RecordConditionFor(obj, metav1.Condition{
		Type:   "Ready",
		Status: metav1.ConditionTrue,
		Reason: "",
	})

	// Flip Ready -> False with reason
	rec.RecordConditionFor(obj, metav1.Condition{
		Type:   "Ready",
		Status: metav1.ConditionFalse,
		Reason: "Failed",
	})

	// Another condition Synchronized=True (independent group)
	rec.RecordConditionFor(obj, metav1.Condition{
		Type:   "Synchronized",
		Status: metav1.ConditionTrue,
		Reason: "",
	})

	// Expect: Ready False(reason)=1, Synchronized True=1
	want := `
# HELP test_record_transition_and_second_condition_controller_condition Condition status for a custom resource; one active (status,reason) time series per (controller,kind,name,namespace,condition).
# TYPE test_record_transition_and_second_condition_controller_condition gauge
test_record_transition_and_second_condition_controller_condition{condition="Ready",controller="my-controller",kind="MyCRD",name="cr-1",namespace="prod",reason="Failed",status="False"} 1
test_record_transition_and_second_condition_controller_condition{condition="Synchronized",controller="my-controller",kind="MyCRD",name="cr-1",namespace="prod",reason="",status="True"} 1
`
	require.NoError(t,
		testutil.GatherAndCompare(
			controllermetrics.Registry,
			strings.NewReader(want),
			"test_record_transition_and_second_condition_controller_condition",
		),
	)

	removed := rec.RemoveConditionsFor(obj)
	assert.Equal(t, 2, removed)
}

func TestConditionMetricRecorder_RemoveConditionsFor(t *testing.T) {
	gauge := NewOperatorConditionsGauge("test_remove_conditions_for_condition")
	_ = controllermetrics.Registry.Register(gauge)
	// Arrange
	rec := &ConditionMetricRecorder{
		Controller:              "my-controller",
		OperatorConditionsGauge: gauge,
	}
	kind := "MyCRD"
	name := "cr-2"
	ns := "staging"
	obj := makeObj(kind, name, ns)

	rec.RecordConditionFor(obj, metav1.Condition{
		Type:   "Ready",
		Status: metav1.ConditionTrue,
	})
	rec.RecordConditionFor(obj, metav1.Condition{
		Type:   "Synchronized",
		Status: metav1.ConditionFalse,
		Reason: "SyncPending",
	})

	// Remove all condition series for this object
	removed := rec.RemoveConditionsFor(obj)
	assert.Equal(t, 2, removed)

	// No series remain for this object
	require.NoError(t,
		testutil.GatherAndCompare(
			controllermetrics.Registry,
			strings.NewReader(""),
			"test_remove_conditions_for_condition_controller_condition",
		),
	)
}

func TestConditionMetricRecorder_SetsKindLabelFromObject(t *testing.T) {
	gauge := NewOperatorConditionsGauge("test_sets_kind_label_from_object")
	_ = controllermetrics.Registry.Register(gauge)
	ctrl := "my-controller"
	rec := &ConditionMetricRecorder{
		Controller:              ctrl,
		OperatorConditionsGauge: gauge,
	}
	kind := "FancyKind"
	name := "obj-1"
	ns := "ns-1"
	obj := makeObj(kind, name, ns)

	// Record a condition
	rec.RecordConditionFor(obj, metav1.Condition{
		Type:   "Ready",
		Status: metav1.ConditionTrue,
	})

	// Expect the 'kind' label to reflect the object's Kind
	want := `
# HELP test_sets_kind_label_from_object_controller_condition Condition status for a custom resource; one active (status,reason) time series per (controller,kind,name,namespace,condition).
# TYPE test_sets_kind_label_from_object_controller_condition gauge
test_sets_kind_label_from_object_controller_condition{condition="Ready",controller="my-controller",kind="FancyKind",name="obj-1",namespace="ns-1",reason="",status="True"} 1
`
	require.NoError(t,
		testutil.GatherAndCompare(
			controllermetrics.Registry,
			strings.NewReader(want),
			"test_sets_kind_label_from_object_controller_condition",
		),
	)

	assert.Equal(t, 1, gauge.DeleteByIndex(ctrl, kind, name, ns))
}
