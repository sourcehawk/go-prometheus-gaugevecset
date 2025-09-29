package operator_condition_metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeObj(name, namespace string) *FakeObject {
	return &FakeObject{
		Name:      name,
		Namespace: namespace,
	}
}

func TestConditionMetricRecorder_Record_Transition_And_SecondCondition(t *testing.T) {
	gauge := NewOperatorConditionsGauge("test_record_transition_and_second_condition")
	reg := prometheus.NewRegistry()
	_ = reg.Register(gauge)

	// Arrange
	rec := &ConditionMetricRecorder{
		Controller:              "my-controller",
		OperatorConditionsGauge: gauge,
	}
	kind := "MyCRD"
	name := "cr-1"
	ns := "prod"
	transitionTime := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

	obj := makeObj(name, ns)

	// Record Ready=True
	rec.RecordConditionFor(kind, obj, "Ready", "True", "", transitionTime)

	// Flip Ready -> False with reason
	rec.RecordConditionFor(kind, obj, "Ready", "False", "Failed", transitionTime)

	// Another condition Synchronized=True (independent group)
	rec.RecordConditionFor(kind, obj, "Synchronized", "True", "", transitionTime)

	// Expect: Ready False(reason)=1, Synchronized True=1
	want := `
# HELP test_record_transition_and_second_condition_controller_condition Condition status for a custom resource; one active (status,reason) time series per (controller,kind,name,namespace,condition).
# TYPE test_record_transition_and_second_condition_controller_condition gauge
test_record_transition_and_second_condition_controller_condition{condition="Ready",controller="my-controller",reason="Failed",resource_kind="MyCRD",resource_name="cr-1",resource_namespace="prod",status="False"} 1735689600
test_record_transition_and_second_condition_controller_condition{condition="Synchronized",controller="my-controller",reason="",resource_kind="MyCRD",resource_name="cr-1",resource_namespace="prod",status="True",} 1735689600
`
	require.NoError(t,
		testutil.GatherAndCompare(
			reg,
			strings.NewReader(want),
			"test_record_transition_and_second_condition_controller_condition",
		),
	)

	removed := rec.RemoveConditionsFor(kind, obj)
	assert.Equal(t, 2, removed)
}

func TestConditionMetricRecorder_RemoveConditionsFor(t *testing.T) {
	gauge := NewOperatorConditionsGauge("test_remove_conditions_for_condition")
	reg := prometheus.NewRegistry()
	_ = reg.Register(gauge)
	// Arrange
	rec := &ConditionMetricRecorder{
		Controller:              "my-controller",
		OperatorConditionsGauge: gauge,
	}
	kind := "MyCRD"
	name := "cr-2"
	ns := "staging"
	transitionTime := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	obj := makeObj(name, ns)

	rec.RecordConditionFor(kind, obj, "Ready", "True", "", transitionTime)
	rec.RecordConditionFor(kind, obj, "Synchronized", "False", "SyncPending", transitionTime)

	// Remove all condition series for this object
	removed := rec.RemoveConditionsFor(kind, obj)
	assert.Equal(t, 2, removed)

	// No series remain for this object
	require.NoError(t,
		testutil.GatherAndCompare(
			reg,
			strings.NewReader(""),
			"test_remove_conditions_for_condition_controller_condition",
		),
	)
}

func TestConditionMetricRecorder_SetsKindLabelFromObject(t *testing.T) {
	gauge := NewOperatorConditionsGauge("test_sets_kind_label_from_object")
	reg := prometheus.NewRegistry()
	_ = reg.Register(gauge)
	ctrl := "my-controller"
	rec := &ConditionMetricRecorder{
		Controller:              ctrl,
		OperatorConditionsGauge: gauge,
	}
	kind := "FancyKind"
	name := "obj-1"
	ns := "ns-1"
	transitionTime := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	obj := makeObj(name, ns)

	// Record a condition
	rec.RecordConditionFor(kind, obj, "Ready", "True", "", transitionTime)

	// Expect the 'kind' label to reflect the object's Kind
	want := `
# HELP test_sets_kind_label_from_object_controller_condition Condition status for a custom resource; one active (status,reason) time series per (controller,kind,name,namespace,condition).
# TYPE test_sets_kind_label_from_object_controller_condition gauge
test_sets_kind_label_from_object_controller_condition{condition="Ready",controller="my-controller",reason="",resource_kind="FancyKind",resource_name="obj-1",resource_namespace="ns-1",status="True"} 1735689600
`
	require.NoError(t,
		testutil.GatherAndCompare(
			reg,
			strings.NewReader(want),
			"test_sets_kind_label_from_object_controller_condition",
		),
	)

	assert.Equal(t, 1, gauge.DeleteByIndex(ctrl, kind, name, ns))
}
