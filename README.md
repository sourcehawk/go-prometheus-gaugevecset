# Prometheus GaugeVecSet

A flexible, memory efficient Prometheus `GaugeVec` wrapper for managing **sets** of metrics.

---

## GaugeVecSet

The `GaugeVecSet` is a high-performance wrapper around Prometheus `GaugeVec` that enables bulk operations on series 
by specified index and grouping labels.

It opens the door for categorizing metrics by labels and sub-labels into sets, and modifying them in bulk according to 
that specification, giving us more flexibility when working with dynamic label sets where only higher order labels may 
be known (or matter).

### Initialization

```go
import (
  "github.com/prometheus/client_golang/prometheus"
  gvs "github.com/sourcehawk/go-prometheus-gaugevecset/pkg/gauge_vec_set"
)

// Metric configuration
const (
    namespace = "kube"
    subsystem = "pod_status"
    name = "phase"
    help = "Pod phase (enum-style, one active per Pod)"
)

var PodPhase = gvs.NewGaugeVecSet(
  namespace, subsystem, name, help,
  []string{"namespace"}, // index
  []string{"pod"},       // group
  "phase",               // extra (the enum value)
)

func init() {
  prometheus.MustRegister(PodPhase)
}
```

### GaugeVecSet: Set

Set exactly one series.

```go
PodPhase.Set(1,
    []string{"prod"},       // namespace (index)
    []string{"nginx-6f4c"}, // pod (group)
    "Pending",              // phase (extra)
)
// Result looks like:
// kube_pod_status_phase{namespace="prod", pod="nginx-6f4c", phase="Pending"}  1
```

### GaugeVecSet: SetActiveInGroup

Set one series and zero all other variants in the same group. Only use this method when your cardinality is bounded.

```go
// Flip to Running
PodPhase.SetActiveInGroup(1,
    []string{"prod"},       // namespace (index)
    []string{"nginx-6f4c"}, // pod (group)
    "Pending",              // phase (extra)
)
// Result looks like:
// kube_pod_status_phase{namespace="prod", pod="nginx-6f4c", phase="Pending"}  0 <- zeroed
// kube_pod_status_phase{namespace="prod", pod="nginx-6f4c", phase="Running"}  1
```

### GaugeVecSet: SetGroup

Set one series and delete all other in the same group. This is your best option when cardinality
is high, because it ensures your cardinality is at most `(index variants * group variants)`, rather than 
`(index variants * group variants * extra variants)`.

```go
// Keep only "Failed" for this Pod, remove other phases entirely.
PodPhase.SetGroup(1,
    []string{"prod"},       // namespace (index)
    []string{"nginx-6f4c"}, // pod (group)
    "Failed",               // phase (extra)
)
// Result looks like:
// kube_pod_status_phase{namespace="prod", pod="nginx-6f4c", phase="Failed"}  1
```

### GaugeVecSet: DeleteByIndex

Delete all series that match the given index values. The number of index values this method requires
coincides with the number of index values the gauge was initialized with, meaning you cannot specify a partial
index for deletion.

_Much, much faster than prometheus's `DeletePartialMatch`._

```go
// Removes every kube_pod_status_phase which has namespace = prod
deleted := PodPhase.DeleteByIndex("prod")
```

### GaugeVecSet: DeleteByGroup

Delete all series that match the given (index, group)

```go
deleted := PodPhase.DeleteByGroup(
    []string{"prod"}, // index
    "nginx-6f4c",     // group
)
```

## ConditionMetricsRecorder

The `ConditionMetricsRecorder` is an implementation of `GaugeVecSet` for kubernetes operators. It enables
controllers to record metrics for it's kubernetes `metav1.Conditions` on custom resources.

It is inspired by kube-state-metrics patterns for metrics such as `kube_pod_status_phase`. KSM exports one time series 
per phase for each (namespace, pod), and marks exactly one as active (1) while the others are inactive (0). This metric 
can be thought of as a `GaugeVecSet` with the index label `namespace`, the group `pod` and the `extra` labels 
(i.e. variants per group) as the options for `phase`.

Example:

```
kube_pod_status_phase{namespace="default", pod="nginx", phase="Running"} 1
kube_pod_status_phase{namespace="default", pod="nginx", phase="Pending"} 0
kube_pod_status_phase{namespace="default", pod="nginx", phase="Failed"}  0
```

We adopt the same pattern for controller Conditions, but we export only one time series per (status, reason) variant, 
meaning we delete all other variants in the group when we set the metric, ensuring the cardinality stays under control.

Example:

```
operator_controller_condition{controller="", kind="", name="", namespace="", condition="", status="", reason=""} 1
```

- **Index**: controller, kind, name, namespace
- **Group**: condition
- **Extra**: status, reason

### Initialization

The metric should be initialized and registered once.

You can embed the `ControllerMetricsRecorder` in your controller's recorder.

```go
package my_metrics

import (
    controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
    ocg "github.com/sourcehawk/go-prometheus-gaugevecset/pkg/operator_condition_metrics"
)

// We need this variable later to create the ConditionMetricsRecorder
var OperatorConditionsGauge *ocg.OperatorConditionsGauge

func init() {
    OperatorConditionsGauge = ocg.NewOperatorConditionsGauge("my-operator")
    controllermetrics.Registry.MustRegister(OperatorConditionsGauge)
}

// Embed in existing metrics recorder
type MyControllerRecorder struct {
	ocg.ConditionMetricRecorder
}
```

When constructing your reconciler, initialize the condition metrics recorder with the
operator conditions gauge and a unique name for each controller.

```go
package main

import (
    mymetrics "path/to/pkg/my_metrics"
    ocg "github.com/sourcehawk/go-prometheus-gaugevecset/pkg/operator_condition_metrics"
)

func main() {
    // ...
    recorder := mymetrics.MyControllerRecorder{
        ConditionMetricRecorder: ocg.ConditionMetricRecorder{
            Controller: "my-controller", // unique name per reconciler
            OperatorConditionsGauge: mymetrics.OperatorConditionsGauge,
        },
    }
	
    reconciler := &MyReconciler{
        Recorder: recorder, 
    }
    // ...
}
```

## Usage

The easiest drop-in way to start using the metrics recorder is by creating a `SetStatusCondition` wrapper, which 
comes instead of `meta.SetStatusCondition`.

To delete the metrics for a given custom resource, simply call `RemoveConditionsFor` and pass the object.

```go
const (
	kind = "MyCR"
)

// SetStatusCondition utility function which replaces and wraps meta.SetStatusCondition calls
func (r *MyReconciler) SetStatusCondition(cr *v1.MyCR, condition metav1.Condition) bool {
    changed := meta.SetStatusCondition(&cr.Status.Conditions, condition)
    if changed {
        r.Recorder.RecordConditionFor(kind, cr, condition.Type, string(condition.Status), condition.Reason)
    }
    return changed
}

func (r *MyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // Get the resource we're reconciling
    cr := new(v1.MyCR)
    if err = r.Get(ctx, req.NamespacedName, cr); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
	
    // Remove the metrics when the CR is deleted
    if cr.DeletionTimeStamp != nil {
        r.Recorder.RemoveConditionsFor(kind, cr)
    }
	
    // ...
	
    // Update the status conditions using the recorder (it records the metric if changed)
    if r.SetStatusCondition(cr, condition) {
        if err = r.Status().Update(ctx, cr); err != nil {
            return ctrl.Result{}, err
        }
    }
	
    return ctrl.Result{}, nil
}
```