package gauge_vec_set

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ensure Set and Delete work when only using one label as an index
func Test_DynamicGaugeCollector_SetAndDelete_SingleIndex(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"jobs",
		"help text",
		[]string{"index"}, // single index label
		nil,               // no group labels
		"x", "y",          // extra labels
	)

	require.NoError(t, reg.Register(col))

	// no group values -> pass nil (or []string{})
	col.Set(1.23, []string{"A"}, nil, "x1A", "y1A")
	col.Set(4.56, []string{"A"}, nil, "x2A", "y2A")
	col.Set(9.99, []string{"B"}, nil, "x1B", "y1B")

	wantAll := `
# HELP testns_subsys_jobs help text
# TYPE testns_subsys_jobs gauge
testns_subsys_jobs{index="A",x="x1A",y="y1A"} 1.23
testns_subsys_jobs{index="A",x="x2A",y="y2A"} 4.56
testns_subsys_jobs{index="B",x="x1B",y="y1B"} 9.99
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(wantAll), "testns_subsys_jobs"))

	assert.Equal(t, 2, col.DeleteByIndex("A"))

	wantAfter := `
# HELP testns_subsys_jobs help text
# TYPE testns_subsys_jobs gauge
testns_subsys_jobs{index="B",x="x1B",y="y1B"} 9.99
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(wantAfter), "testns_subsys_jobs"))

	// deleting a non-existent index should return 0
	assert.Equal(t, 0, col.DeleteByIndex("NO_SUCH"))
}

// Ensure Set and Delete work when using multiple labels as an index
func Test_DynamicGaugeCollector_SetAndDelete_MultiIndex(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"jobs_multi",
		"help text (multi-index)",
		[]string{"tenant", "cluster"}, // two index labels
		nil,                           // no group labels
		"phase",                       // extra label
	)

	require.NoError(t, reg.Register(col))

	// tenant t1 / cluster c1 has two phases
	col.Set(1, []string{"t1", "c1"}, nil, "running")
	col.Set(0, []string{"t1", "c1"}, nil, "pending")

	// tenant t2 / cluster c2 has one phase
	col.Set(2.5, []string{"t2", "c2"}, nil, "running")

	wantAll := `
# HELP testns_subsys_jobs_multi help text (multi-index)
# TYPE testns_subsys_jobs_multi gauge
testns_subsys_jobs_multi{cluster="c1",phase="running",tenant="t1"} 1
testns_subsys_jobs_multi{cluster="c1",phase="pending",tenant="t1"} 0
testns_subsys_jobs_multi{cluster="c2",phase="running",tenant="t2"} 2.5
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(wantAll), "testns_subsys_jobs_multi"))

	// Delete all series for {tenant=t1, cluster=c1}
	assert.Equal(t, 2, col.DeleteByIndex("t1", "c1"))

	wantAfter := `
# HELP testns_subsys_jobs_multi help text (multi-index)
# TYPE testns_subsys_jobs_multi gauge
testns_subsys_jobs_multi{cluster="c2",phase="running",tenant="t2"} 2.5
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(wantAfter), "testns_subsys_jobs_multi"))

	// Deleting again should return 0
	assert.Equal(t, 0, col.DeleteByIndex("t1", "c1"))
}

// Ensure Set and Delete work when using multiple labels as an index as well as grouping labels
func Test_DynamicGaugeCollector_SetAndDelete_MultiIndexGrouped(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"jobs_multi_grouped",
		"help text (multi-index grouped)",
		[]string{"tenant", "cluster"}, // index labels
		[]string{"condition"},         // group label (provided, but we're using Set, not SetActiveInGroup)
		"phase",                       // extra label
	)

	require.NoError(t, reg.Register(col))

	// tenant t1 / cluster c1 with group "condition=Ready" and two phases
	col.Set(1, []string{"t1", "c1"}, []string{"Ready"}, "running")
	col.Set(0, []string{"t1", "c1"}, []string{"Ready"}, "pending")

	// tenant t2 / cluster c2 with group "condition=Ready"
	col.Set(2.5, []string{"t2", "c2"}, []string{"Ready"}, "running")

	wantAll := `
# HELP testns_subsys_jobs_multi_grouped help text (multi-index grouped)
# TYPE testns_subsys_jobs_multi_grouped gauge
testns_subsys_jobs_multi_grouped{cluster="c1",condition="Ready",phase="running",tenant="t1"} 1
testns_subsys_jobs_multi_grouped{cluster="c1",condition="Ready",phase="pending",tenant="t1"} 0
testns_subsys_jobs_multi_grouped{cluster="c2",condition="Ready",phase="running",tenant="t2"} 2.5
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(wantAll), "testns_subsys_jobs_multi_grouped"))

	// Delete all series for {tenant=t1, cluster=c1}
	assert.Equal(t, 2, col.DeleteByIndex("t1", "c1"))

	wantAfter := `
# HELP testns_subsys_jobs_multi_grouped help text (multi-index grouped)
# TYPE testns_subsys_jobs_multi_grouped gauge
testns_subsys_jobs_multi_grouped{cluster="c2",condition="Ready",phase="running",tenant="t2"} 2.5
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(wantAfter), "testns_subsys_jobs_multi_grouped"))

	// Deleting again should return 0
	assert.Equal(t, 0, col.DeleteByIndex("t1", "c1"))
}

// Ensures SetGrouped removes sibling series in the same (index, group).
func Test_DynamicGaugeCollector_SetGrouped(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"conditions",
		"condition enum (grouped by condition name)",
		[]string{"controller", "name", "namespace"}, // index
		[]string{"condition"},                       // group
		"status", "reason",                          // extra
	)

	require.NoError(t, reg.Register(col))

	idx := []string{"ctrl", "obj", "ns"}

	// First: Ready=True (reason empty)
	col.SetGroup(1, idx, []string{"Ready"}, "True", "")
	// Flip: Ready=False with reason (sibling should be zeroed)
	col.SetGroup(1, idx, []string{"Ready"}, "False", "bad_secret")
	// Another condition should be untouched by Ready's exclusivity
	col.SetGroup(1, idx, []string{"Synchronized"}, "True", "")

	want := `
# HELP testns_subsys_conditions condition enum (grouped by condition name)
# TYPE testns_subsys_conditions gauge
testns_subsys_conditions{condition="Ready",controller="ctrl",name="obj",namespace="ns",reason="bad_secret",status="False"} 1
testns_subsys_conditions{condition="Synchronized",controller="ctrl",name="obj",namespace="ns",reason="",status="True"} 1
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want), "testns_subsys_conditions"))
}

// Ensures SetActiveInGroup zeros sibling series in the same (index, group).
func Test_DynamicGaugeCollector_SetActiveInGroup_GroupedExclusivity(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"conditions",
		"condition enum (grouped by condition name)",
		[]string{"controller", "name", "namespace"}, // index
		[]string{"condition"},                       // group
		"status", "reason",                          // extra
	)

	require.NoError(t, reg.Register(col))

	idx := []string{"ctrl", "obj", "ns"}

	// First: Ready=True (reason empty)
	col.SetActiveInGroup(1, idx, []string{"Ready"}, "True", "")
	// Flip: Ready=False with reason (sibling should be zeroed)
	col.SetActiveInGroup(1, idx, []string{"Ready"}, "False", "bad_secret")
	// Another condition should be untouched by Ready's exclusivity
	col.SetActiveInGroup(1, idx, []string{"Synchronized"}, "True", "")

	want := `
# HELP testns_subsys_conditions condition enum (grouped by condition name)
# TYPE testns_subsys_conditions gauge
testns_subsys_conditions{condition="Ready",controller="ctrl",name="obj",namespace="ns",reason="",status="True"} 0
testns_subsys_conditions{condition="Ready",controller="ctrl",name="obj",namespace="ns",reason="bad_secret",status="False"} 1
testns_subsys_conditions{condition="Synchronized",controller="ctrl",name="obj",namespace="ns",reason="",status="True"} 1
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want), "testns_subsys_conditions"))
}

// Ensures DeleteByGroup deletes only the targeted (index, group) bucket.
func Test_DynamicGaugeCollector_DeleteByGroup(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"conditions",
		"condition enum (grouped by condition name)",
		[]string{"controller", "name", "namespace"}, // index
		[]string{"condition"},                       // group
		"status", "reason",                          // extra
	)

	require.NoError(t, reg.Register(col))

	idx := []string{"ctrl", "obj", "ns"}

	// Populate two different groups (conditions)
	col.SetActiveInGroup(1, idx, []string{"Ready"}, "True", "")
	col.SetActiveInGroup(1, idx, []string{"Synchronized"}, "False", "sync_pending")

	// Delete only the Ready group
	assert.Equal(t, 1, col.DeleteByGroup(idx, "Ready"))

	// Ready should be gone; Synchronized should remain
	want := `
# HELP testns_subsys_conditions condition enum (grouped by condition name)
# TYPE testns_subsys_conditions gauge
testns_subsys_conditions{condition="Synchronized",controller="ctrl",name="obj",namespace="ns",reason="sync_pending",status="False"} 1
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want), "testns_subsys_conditions"))

	// Deleting Ready again should return false
	assert.Equal(t, 0, col.DeleteByGroup(idx, "Ready"))
}

func Test_DynamicGaugeCollector_DeleteByGroup_NoGroupsConfigured(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"jobs",
		"help text",
		[]string{"index"}, // index labels
		nil,               // NO group labels
		"x", "y",          // extra
	)
	require.NoError(t, reg.Register(col))

	col.Set(1.0, []string{"A"}, nil, "x1", "y1")

	// Calling DeleteByGroup when no groups configured should be a no-op and return 0.
	assert.Equal(t, 0, col.DeleteByGroup([]string{"A"}, "ignored"))

	want := `
# HELP testns_subsys_jobs help text
# TYPE testns_subsys_jobs gauge
testns_subsys_jobs{index="A",x="x1",y="y1"} 1
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want), "testns_subsys_jobs"))
}

func Test_DynamicGaugeCollector_DeleteByGroup_NonExistent(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"conditions",
		"help text",
		[]string{"tenant", "cluster"}, // index
		[]string{"condition"},         // group
		"status",                      // extra
	)
	require.NoError(t, reg.Register(col))

	col.Set(1, []string{"t1", "c1"}, []string{"Ready"}, "True")

	// Try to delete a group that doesn't exist
	assert.Equal(t, 0, col.DeleteByGroup([]string{"t1", "c1"}, "Synchronized"))

	// Original series remains
	want := `
# HELP testns_subsys_conditions help text
# TYPE testns_subsys_conditions gauge
testns_subsys_conditions{cluster="c1",condition="Ready",status="True",tenant="t1"} 1
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want), "testns_subsys_conditions"))
}

func Test_DynamicGaugeCollector_ArityValidationPanics(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"arity",
		"help text",
		[]string{"a", "b"}, // index has 2 labels
		[]string{"grp"},    // 1 group
		"x", "y", "z",      // 3 extra
	)
	require.NoError(t, reg.Register(col))

	// Wrong index arity for Set
	assert.Panics(t, func() {
		col.Set(1, []string{"onlyA"}, []string{"G"}, "xv", "yv", "zv")
	})

	// Wrong group arity for Set
	assert.Panics(t, func() {
		col.Set(1, []string{"A", "B"}, []string{"G", "H"}, "xv", "yv", "zv")
	})

	// Wrong extra arity for Set
	assert.Panics(t, func() {
		col.Set(1, []string{"A", "B"}, []string{"G"}, "xv", "yv") // missing z
	})

	// Wrong index arity for DeleteByIndex
	assert.Panics(t, func() {
		col.DeleteByIndex("A") // expecting 2 index values
	})

	// Wrong group arity for DeleteByGroup
	assert.Panics(t, func() {
		col.DeleteByGroup([]string{"A", "B"}, "G", "H")
	})
}

func Test_DynamicGaugeCollector_MetricNamePanics(t *testing.T) {
	cases := []struct {
		name       string
		namespace  string
		subsystem  string
		metricName string
	}{
		{
			name:       "invalid namespace contains special characters",
			namespace:  "n-s",
			subsystem:  "subsystem",
			metricName: "name",
		},
		{
			name:       "invalid subsystem contains special characters",
			namespace:  "namespace",
			subsystem:  "sub-system",
			metricName: "name",
		},
		{
			name:       "invalid metric contains special characters",
			namespace:  "namespace",
			subsystem:  "subsystem",
			metricName: "na-me",
		},
		{
			name:       "invalid namespace ends with underscore",
			namespace:  "namespace_",
			subsystem:  "subsystem",
			metricName: "name",
		},
		{
			name:       "invalid subsystem ends with underscore",
			namespace:  "namespace",
			subsystem:  "subsystem_",
			metricName: "name",
		},
		{
			name:       "invalid metric ends with underscore",
			namespace:  "namespace",
			subsystem:  "subsystem",
			metricName: "name_",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Panics(t, func() {
				NewGaugeVecSet(c.namespace, c.subsystem, c.metricName, "", []string{"index"}, []string{"group"})
			})
		})
	}
}

func Test_DynamicGaugeCollector_LabelsWithHashCharacters(t *testing.T) {
	reg := prometheus.NewRegistry()

	col := NewGaugeVecSet(
		"testns",
		"subsys",
		"hashy",
		"help text",
		[]string{"tenant", "cluster"}, // index
		[]string{"condition"},         // group
		"phase",                       // extra
	)
	require.NoError(t, reg.Register(col))

	// Values containing '#' should be stripped
	idx := []string{"t`1", "c`1"}
	col.Set(1, idx, []string{"Re`ady"}, "run`ning")

	want := `
# HELP testns_subsys_hashy help text
# TYPE testns_subsys_hashy gauge
testns_subsys_hashy{cluster="c1",condition="Ready",phase="running",tenant="t1"} 1
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want), "testns_subsys_hashy"))

	// Ensure DeleteByIndex works with input that still contains '#'
	assert.Equal(t, 1, col.DeleteByIndex("t`1", "c`1"))

	// Metric should now be gone
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(""), "testns_subsys_hashy"))
}

// Run the test 50 times:
// go test -race ./pkg/metrics -run 'TestDynamicGaugeCollector_ConcurrentSetDelete_NoRace' -count=50
func Test_DynamicGaugeCollector_ConcurrentSetDelete_NoRace(t *testing.T) {
	col := NewGaugeVecSet(
		"concur", "controller", "condition",
		"concurrency stress test",
		[]string{"controller", "kind", "name", "namespace"}, // index
		[]string{"condition"},                               // group
		"status", "reason",                                  // extra
	)

	controllers := []string{"c1", "c2"}
	kinds := []string{"KindA", "KindB"}
	names := []string{"a", "b", "c", "d"}
	namespaces := []string{"ns1", "ns2"}
	conditions := []string{"Ready", "Synchronized"}
	statuses := []string{"True", "False", "Unknown"}
	reasons := []string{"", "KeyAuthorizationError", "SyncPending", "Other"}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup

	// Helper used inside each goroutine with its own rng
	pick := func(r *rand.Rand, ss []string) string { return ss[r.Intn(len(ss))] }

	// Writers
	startWriters := 8
	for i := 0; i < startWriters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)*7919))
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				idx := []string{pick(r, controllers), pick(r, kinds), pick(r, names), pick(r, namespaces)}
				grp := []string{pick(r, conditions)}
				st := pick(r, statuses)
				rs := pick(r, reasons)
				if st == "True" {
					rs = ""
				}
				col.SetActiveInGroup(1, idx, grp, st, rs)
			}
		}(i)
	}

	// Group deleters
	startGroupDeleters := 4
	for i := 0; i < startGroupDeleters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)*104729))
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				idx := []string{pick(r, controllers), pick(r, kinds), pick(r, names), pick(r, namespaces)}
				grp := []string{pick(r, conditions)}
				_ = col.DeleteByGroup(idx, grp...)
			}
		}(i)
	}

	// Index deleters
	startIndexDeleters := 2
	for i := 0; i < startIndexDeleters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)*15485863))
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				idx := []string{pick(r, controllers), pick(r, kinds), pick(r, names), pick(r, namespaces)}
				_ = col.DeleteByIndex(idx...)
			}
		}(i)
	}

	wg.Wait()

	// ensure collect path works
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(col))
	_, err := reg.Gather()
	require.NoError(t, err)
}
