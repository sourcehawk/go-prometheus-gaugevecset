# Prometheus GaugeVecSet

A flexible, memory efficient Prometheus `GaugeVec` wrapper for managing **sets** of metrics.

The `GaugeVecSet` is a high-performance wrapper around Prometheus `GaugeVec` that enables bulk operations on series 
by specified index and grouping labels.

It opens the door for categorizing metrics by labels and sub-labels into sets, and modifying them in bulk according to 
that specification, giving us more flexibility when working with dynamic label sets where only higher order labels may 
be known (or matter).

### Installation

Install the go package

```go
go get github.com/sourcehawk/go-prometheus-gaugevecset
```

Importing it:

```go
import (
	gvs "github.com/sourcehawk/go-prometheus-gaugevecset/pkg/gauge-vec-set"
)
```

### Initialization

```go
import (
  "github.com/prometheus/client_golang/prometheus"
  gvs "github.com/sourcehawk/go-prometheus-gaugevecset/pkg/gauge-vec-set"
)

// Metric configuration
const (
    namespace = "kube"
    subsystem = "pod_status"
    name = "phase"
    help = "Pod phase (enum-style, one active per Pod)"
)

// Creating the custom collector
var PodPhase = gvs.NewGaugeVecSet(
  namespace, subsystem, name, help,
  []string{"namespace"}, // index
  []string{"pod"},       // group
  "phase",               // extra (the enum value)
)

// Register the collector once
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

Delete all series that match the given (index, group). The number of index and group values this method requires 
coincides with the number of values the gauge was initialized with, meaning you cannot specify partial values for
deletion.

```go
deleted := PodPhase.DeleteByGroup(
    []string{"prod"}, // index
    "nginx-6f4c",     // group
)
```