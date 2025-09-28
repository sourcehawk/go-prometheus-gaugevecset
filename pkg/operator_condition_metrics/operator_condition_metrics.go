package operator_condition_metrics

import (
	metrics "github.com/sourcehawk/go-prometheus-gaugevecset/pkg/gauge_vec_set"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

/*
Inspired by kube-state-metrics enum-style patterns:

kube-state-metrics models enum-like states (e.g., Pod phase) by exporting one time series per variant,
and marking exactly one as active (1) while the others are inactive (0). Example:

  kube_pod_status_phase{namespace="default", pod="nginx", phase="Running"} 1
  kube_pod_status_phase{namespace="default", pod="nginx", phase="Pending"} 0
  kube_pod_status_phase{namespace="default", pod="nginx", phase="Failed"}  0

We adopt the same pattern for controller Conditions but we export one time series per (status, reason) variant
and enforce **exclusivity per condition**.

For any given (controller, kind, name, namespace, condition) exactly one (status, reason) series is present at a time.
All other variants are **deleted**. This keeps cardinality under control.

Metric
  <namespace>_controller_condition

Labels (order matches registration)
  - controller: controller name (e.g., "my-operator")
  - kind:       resource kind (e.g., "MyCRD")
  - name:       resource name
  - namespace:  resource namespace ("" for cluster-scoped)
  - condition:  condition type (e.g., "Ready", "Reconciled")
  - status:     "True" | "False" | "Unknown"
  - reason:     short machine-typed reason (often "" when status="True")

Value
  - Always 1 for the single active (status, reason) series in the group.

Examples:

1. Resource becomes Ready (True):

  my_controller_condition{
    controller="my-operator",
    kind="MyCRD",
    name="my-cr-1",
    namespace="prod",
    condition="Ready",
    status="True",
    reason=""
  } 1

(Other status/reason variants for this condition are removed.)

2. Transition: Ready to false

  // Previous series is removed
  // New series becomes active:
  my_controller_condition{
    ...,
    condition="Ready",
    status="False",
    reason="Failed"
  } 1

3. Another condition can be active simultaneously (different group):

  my_controller_condition{
    ...,
    condition="Synchronized",
    status="True",
    reason=""
  } 1

Cleanup
  When the resource is deleted/pruned, all series for its index key
  (controller, kind, name, namespace) are removed via DeleteByIndex().

Implementation
  Backed by a GaugeVecSet with:
    indexLabels = [controller, kind, name, namespace]
    groupLabels = [condition]
    extraLabels = [status, reason]
  Exclusivity is enforced with SetGroup(), which deletes sibling series.

Notes
  - KIND: In controller-runtime, obj.GetObjectKind().GroupVersionKind().Kind can be empty if
    GVK wasn't populated. Ensure your scheme is registered and objects are decoded via the client;
    otherwise, resolve Kind via the scheme (scheme.ObjectKinds(obj)).
  - Cluster-scoped: namespace label is set to the empty string "".
*/

const (
	operatorConditionMetricSubsystem = "controller"
	operatorConditionMetricName      = "condition"
	operatorConditionMetricHelp      = "Condition status for a custom resource; one active (status,reason) time series per (controller,kind,name,namespace,condition)."
)

var (
	indexLabels = []string{"controller", "kind", "name", "namespace"}
	groupLabels = []string{"condition"}
	extraLabels = []string{"status", "reason"}
)

type OperatorConditionsGauge struct {
	*metrics.GaugeVecSet
}

// NewOperatorConditionsGauge creates a new OperatorConditionsGauge for an operator.
// Initialize once (e.g., in your package init or setup)
//
//		var OperatorConditionsGauge *OperatorConditionsGauge = nil
//
//		func init() {
//	     OperatorConditionsGauge = NewOperatorConditionsGauge("my-operator")
//			controllermetrics.Registry.MustRegister(OperatorConditionsGauge)
//		}
func NewOperatorConditionsGauge(metricNamespace string) *OperatorConditionsGauge {
	return &OperatorConditionsGauge{
		metrics.NewGaugeVecSet(
			metricNamespace,
			operatorConditionMetricSubsystem,
			operatorConditionMetricName,
			operatorConditionMetricHelp,
			indexLabels,
			groupLabels,
			extraLabels...,
		),
	}
}

// ConditionMetricRecorder records metrics for Kubernetes style `metav1.Condition`
// objects on custom resources, using a Prometheus gauge.
//
// It is intended for use in controller implementations that expose CRD conditions
// (e.g., Ready, Reconciled, Synchronized).
//
// Usage:
//
// Embed in your custom recorder or reconciler
//
//		type MyRecorder struct {
//			metrics.ConditionMetricRecorder
//		}
//
//		r := MyControllerRecorder{
//			 ConditionMetricRecorder: metrics.ConditionMetricRecorder{
//				 Controller: "my-controller",
//	          OperatorConditionsGauge: OperatorConditionsGauge,
//			 },
//		}
//
//		r.RecordConditionFor(myObj, condition)
//		r.SetStatusCondition(myObj, &myObj.Status.Conditions, condition) // wrapper for meta.SetStatusCondition
//		r.RemoveConditionsFor(myObj)
type ConditionMetricRecorder struct {
	// The name of the controller the condition metrics are for
	Controller string
	// The OperatorConditionsGauge initialized by NewOperatorConditionsGauge
	OperatorConditionsGauge *OperatorConditionsGauge
}

// RecordConditionFor sets a condition metric for a given controller and object.
//
// It enforces exclusivity within the same (controller, name, namespace, condition) group,
// ensuring that only the latest status (True/False/Unknown) is present for a given condition type.
//
// The following label values are set:
//
//   - controller:  the controller name reporting the condition
//   - kind:        object kind
//   - name:        object name
//   - namespace:   object namespace
//   - condition:   condition type (e.g., "Ready", "Reconciled")
//   - status:      condition status ("True", "False", "Unknown")
//   - reason:      short reason string
//
// Example:
//
//	r.RecordConditionFor(obj, metav1.Condition{
//	    Type:   "Ready",
//	    Status: metav1.ConditionFalse,
//	    Reason: "KeyAuthorizationError",
//	})
func (r *ConditionMetricRecorder) RecordConditionFor(
	object client.Object, condition metav1.Condition,
) {
	kind := object.GetObjectKind().GroupVersionKind().Kind
	indexValues := []string{r.Controller, kind, object.GetName(), object.GetNamespace()}
	groupValues := []string{condition.Type}
	extraValues := []string{string(condition.Status), condition.Reason}

	r.OperatorConditionsGauge.SetGroup(1, indexValues, groupValues, extraValues...)
}

// SetStatusCondition sets a condition on the object's status using the Kubernetes
// helper `meta.SetStatusCondition`, and records the corresponding Prometheus metric
// if the condition was changed.
//
// It is a convenience wrapper that ensures metrics and status conditions stay in sync.
//
// Parameters:
//   - object:     the Kubernetes object (used to extract name, namespace, kind).
//     this is the object the condition is tied to in the controller reconciler.
//   - conditions: pointer to the object's `.Status.Conditions` slice
//   - condition:  the metav1.Condition to set
//
// Returns:
//   - changed: true if the condition slice was modified
//
// Example:
//
//	changed := r.SetStatusCondition(
//	    obj,
//	    &obj.Status.Conditions,
//	    metav1.Condition{
//	        Type:   "Ready",
//	        Status: metav1.ConditionTrue,
//	        Reason: "SuccessfullyReconciled",
//	        Message: "The resource is ready.",
//	    },
//	)
func (r *ConditionMetricRecorder) SetStatusCondition(
	object client.Object, conditions *[]metav1.Condition, condition metav1.Condition,
) (changed bool) {
	changed = meta.SetStatusCondition(conditions, condition)
	if changed {
		r.RecordConditionFor(object, condition)
	}
	return changed
}

// RemoveConditionsFor deletes all condition metrics for a given resource.
// This removes all condition types (e.g., Ready, Reconciled) for the resource in one call.
//
// Typically called when the object is deleted or no longer relevant to the controller (Deletion reconcile).
// Returns the number of time series deleted.
func (r *ConditionMetricRecorder) RemoveConditionsFor(object client.Object) (removed int) {
	kind := object.GetObjectKind().GroupVersionKind().Kind
	return r.OperatorConditionsGauge.DeleteByIndex(r.Controller, kind, object.GetName(), object.GetNamespace())
}
