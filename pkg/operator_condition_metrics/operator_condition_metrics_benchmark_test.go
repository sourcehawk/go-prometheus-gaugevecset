package operator_condition_metrics

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/prometheus/common/expfmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	controllermetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

/*
Run:
	go test -run '^$' -bench . -benchtime=10000x -benchmem ./pkg/operator_condition_metrics
*/

// Let's benchmark against a somewhat realistic high usage scenario
const (
	controllerCount         = 10
	resourcesPerController  = 200
	conditionsPerController = 3
	variantsPerCondition    = 10
	// Maximum total time series variants: 10 * 200 * 3 * 10 = 60k
	// In our configuration however, we expect only one variant per condition to be exported.
	// Maximum total exported time series: 10 * 200 * 3 * 1 = 6k
	maxCardinality = controllerCount * resourcesPerController * conditionsPerController * variantsPerCondition
)

func generatedName(prefix string, i int) string {
	return fmt.Sprintf("%s%d", prefix, i)
}

func createBenchmarkScenario(tb testing.TB) *ConditionMetricRecorder {
	tb.Helper()

	ns := "bench_ns_" + generatedName("", tb.(*testing.B).N)
	gauge := NewOperatorConditionsGauge(ns)
	_ = controllermetrics.Registry.Register(gauge)
	tb.Cleanup(func() {
		controllermetrics.Registry.Unregister(gauge)
	})

	rec := &ConditionMetricRecorder{
		Controller:              "my-controller",
		OperatorConditionsGauge: gauge,
	}

	obj := &metav1.PartialObjectMetadata{}
	gvk := schema.GroupVersionKind{
		Group:   "benchmark.io",
		Version: "v1",
		Kind:    "Benchmark",
	}
	condition := metav1.Condition{
		Status: metav1.ConditionTrue, // doesn't matter, cardinality controlled by reason
	}

	for i := 0; i < controllerCount; i++ {
		gvk.Kind = generatedName("Controller", i)
		obj.SetGroupVersionKind(gvk)

		for j := 0; j < resourcesPerController; j++ {
			obj.SetName(generatedName("Resource", j))
			obj.SetNamespace(generatedName("namespace", j))

			for k := 0; k < conditionsPerController; k++ {
				condition.Type = generatedName("condition", k)

				for v := 0; v < variantsPerCondition; v++ {
					condition.Reason = generatedName("variant", v)
					rec.RecordConditionFor(obj, condition)
				}
			}
		}
	}

	return rec
}

// Benchmark the average time per call on a pre-populated scenario:
//   - RecordConditionFor
//   - RemoveConditionsFor
//
// Reports: ns/op for each sub-benchmark.
func Benchmark_ConditionMetricsRecorder_TimePerCall(b *testing.B) {
	rec := createBenchmarkScenario(b)

	// Use a stable object that exists in the populated dataset.
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "benchmark.io",
		Version: "v1",
		Kind:    "Controller0",
	})
	obj.SetName("Resource0")
	obj.SetNamespace("namespace0")

	// Two variants in the same (controller,kind,name,namespace,condition) group.
	condTrue := metav1.Condition{Type: "condition0", Status: metav1.ConditionTrue, Reason: "variant0"}
	condFalse := metav1.Condition{Type: "condition0", Status: metav1.ConditionFalse, Reason: "variant1"}

	b.Run("RecordConditionFor", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.ReportMetric(float64(maxCardinality), "series/op")
		for i := 0; i < b.N; i++ {
			// Flip between two variants
			if (i & 1) == 0 {
				rec.RecordConditionFor(obj, condTrue)
			} else {
				rec.RecordConditionFor(obj, condFalse)
			}
		}
	})

	b.Run("RemoveConditionsFor", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.ReportMetric(float64(maxCardinality), "series/op")
		for i := 0; i < b.N; i++ {
			// Ensure there is something to remove, but do not count the set time.
			b.StopTimer()
			rec.RecordConditionFor(obj, condTrue)
			b.StartTimer()

			rec.RemoveConditionsFor(obj)
		}
	})
}

// Benchmark the size of the Prometheus gather output on a pre-populated scenario.
//
// Reports: Metric size in KB retrieved from the registry.
func Benchmark_ConditionMetricsRecorder_PrometheusMemorySize(b *testing.B) {
	_ = createBenchmarkScenario(b)

	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(maxCardinality), "series/op")

	mfs, err := controllermetrics.Registry.Gather()
	if err != nil {
		b.Fatalf("gather: %v", err)
	}
	var buf bytes.Buffer
	for _, mf := range mfs {
		_, _ = expfmt.MetricFamilyToText(&buf, mf)
	}
	sizeKB := float64(buf.Len()) / 1024.0

	b.ReportMetric(sizeKB, "KB")
}
