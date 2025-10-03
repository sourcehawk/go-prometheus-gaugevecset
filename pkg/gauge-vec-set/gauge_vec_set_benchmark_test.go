package gauge_vec_set

import (
	"fmt"
	"math/rand"
	"testing"
)

/*
Run:
	go test -run '^$' -bench . -benchtime=10000x -benchmem ./pkg/gauge_vec_set
*/

var (
	labelVariations = [][3]int{
		// index
		{1, 0, 0},
		{2, 0, 0},
		{4, 0, 0},
		// group
		{1, 1, 0},
		{1, 2, 0},
		{1, 4, 0},
		// extra
		{1, 0, 1},
		{1, 0, 2},
		{1, 0, 4},
		// combo
		{1, 1, 1},
		{2, 1, 2},
		{2, 2, 2},
		{4, 2, 4},
		{4, 4, 4},
	}

	// For Set density.
	prepopulateN = []int{1, 10, 100, 1000}
	siblings     = []int{2, 4, 8, 16}
	groupCount   = []int{2, 4, 8, 16}
)

func makeLabelNames(prefix string, n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("%s%d", prefix, i+1)
	}
	return out
}

func makeIndexValues(i, n int) []string {
	out := make([]string, n)
	for k := 0; k < n; k++ {
		out[k] = fmt.Sprintf("idx_%d_%d", k+1, (i%(k+3))+1)
	}
	return out
}

func makeGroupValues(i, n int) []string {
	out := make([]string, n)
	for k := 0; k < n; k++ {
		out[k] = fmt.Sprintf("grp_%d_%d", k+1, (i%(k+2))+1)
	}
	return out
}

func makeExtraValues(i, n int) []string {
	out := make([]string, n)
	for k := 0; k < n; k++ {
		out[k] = fmt.Sprintf("ext_%d_%d", k+1, (i%(k+4))+1)
	}
	return out
}

func newParamCollector(ns string, idxN, grpN, extN int) *GaugeVecSet {
	return NewGaugeVecSet(
		ns,
		"sub",
		fmt.Sprintf("dg_%d_%d_%d", idxN, grpN, extN),
		"parametric bench",
		makeLabelNames("idx", idxN),
		makeLabelNames("grp", grpN),
		makeLabelNames("ext", extN)...,
	)
}

func labelsCount(idxN, grpN, extN int) int { return idxN + grpN + extN }

func Benchmark_DynamicGaugeCollector_Set(b *testing.B) {
	var tuples [][3]int
	var preN []int

	tuples, preN = labelVariations, prepopulateN

	for _, t := range tuples {
		idxN, grpN, extN := t[0], t[1], t[2]
		L := labelsCount(idxN, grpN, extN)
		for _, n := range preN {
			name := fmt.Sprintf("idx=%d_grp=%d_ext=%d/N=%d", idxN, grpN, extN, n)
			b.Run(name, func(b *testing.B) {
				col := newParamCollector("bench_set", idxN, grpN, extN)

				// Prepopulate (not timed).
				for i := 0; i < n; i++ {
					col.Set(1,
						makeIndexValues(i, idxN),
						makeGroupValues(i, grpN),
						makeExtraValues(i, extN)...,
					)
				}

				r := rand.New(rand.NewSource(42))
				b.ReportAllocs()
				b.ResetTimer()
				// Report contextual metrics.
				b.ReportMetric(float64(n), "series/op")
				b.ReportMetric(float64(L), "labels/op")

				for i := 0; i < b.N; i++ {
					j := r.Intn(max(1, n))
					col.Set(1,
						makeIndexValues(j, idxN),
						makeGroupValues(j, grpN),
						makeExtraValues(j, extN)...,
					)
				}
			})
		}
	}
}

// SetGroup: delete all series in (index,group) then set the chosen one.
// Two modes:
//   - cold:   each op starts with `sib` siblings present (worst case).
//   - steady: each op starts with 1 series present (typical steady state).
func Benchmark_DynamicGaugeCollector_SetGroup(b *testing.B) {
	var tuples [][3]int
	var sibs []int
	tuples, sibs = labelVariations, siblings

	for _, t := range tuples {
		idxN, grpN, extN := t[0], t[1], t[2]
		if grpN == 0 {
			continue
		}
		L := labelsCount(idxN, grpN, extN)

		for _, sib := range sibs {
			// cold: measure first-switch cost with many siblings present
			{
				// With extN==0, all variants collapse to the same series.
				coldSeries := sib
				if extN == 0 {
					coldSeries = 1
				}

				name := fmt.Sprintf("idx=%d_grp=%d_ext=%d/siblings=%d/cold", idxN, grpN, extN, sib)
				b.Run(name, func(b *testing.B) {
					col := newParamCollector("bench_set_group_cold", idxN, grpN, extN)

					idxVals := makeIndexValues(0, idxN)
					grpVals := makeGroupValues(0, grpN)

					// Pre-create siblings (not timed).
					for j := 0; j < sib; j++ {
						col.Set(0, idxVals, grpVals, makeExtraValues(j, extN)...)
					}

					r := rand.New(rand.NewSource(2027))
					b.ReportAllocs()
					b.ResetTimer()
					b.ReportMetric(float64(coldSeries), "series/op")
					b.ReportMetric(float64(L), "labels/op")

					for i := 0; i < b.N; i++ {
						j := r.Intn(sib)
						col.SetGroup(1, idxVals, grpVals, makeExtraValues(j, extN)...)

						// Restore "cold" state (untimed): re-materialize all siblings.
						b.StopTimer()
						for jj := 0; jj < sib; jj++ {
							// It’s fine to also touch the active one with 0; we just need the series present.
							col.Set(0, idxVals, grpVals, makeExtraValues(jj, extN)...)
						}
						b.StartTimer()
					}
				})
			}
			// steady: measure typical cost after SetGroup has reduced the group to one series
			{
				const steadySeries = 1
				name := fmt.Sprintf("idx=%d_grp=%d_ext=%d/siblings=%d/steady",
					idxN, grpN, extN, sib)
				b.Run(name, func(b *testing.B) {
					col := newParamCollector("bench_set_group_steady", idxN, grpN, extN)

					idxVals := makeIndexValues(1, idxN)
					grpVals := makeGroupValues(1, grpN)

					// Start in steady state: exactly one series present.
					col.Set(1, idxVals, grpVals, makeExtraValues(0, extN)...)

					r := rand.New(rand.NewSource(2028))
					b.ReportAllocs()
					b.ResetTimer()
					b.ReportMetric(float64(steadySeries), "series/op")
					b.ReportMetric(float64(L), "labels/op")

					for i := 0; i < b.N; i++ {
						j := r.Intn(max(1, sib)) // works even if extN==0
						col.SetGroup(1, idxVals, grpVals, makeExtraValues(j, extN)...)
					}
				})
			}
		}
	}
}

// Flipping the active sibling within a single (index,group) bucket.
func Benchmark_DynamicGaugeCollector_SetActiveInGroup(b *testing.B) {
	var tuples [][3]int
	var sibs []int
	tuples, sibs = labelVariations, siblings

	for _, t := range tuples {
		idxN, grpN, extN := t[0], t[1], t[2]
		L := labelsCount(idxN, grpN, extN)
		for _, sib := range sibs {
			name := fmt.Sprintf("idx=%d_grp=%d_ext=%d/siblings=%d", idxN, grpN, extN, sib)
			b.Run(name, func(b *testing.B) {
				col := newParamCollector("bench_set_excl", idxN, grpN, extN)

				idxVals := makeIndexValues(0, idxN)
				grpVals := makeGroupValues(0, grpN)

				// Pre-create siblings (not timed).
				for j := 0; j < sib; j++ {
					col.Set(0, idxVals, grpVals, makeExtraValues(j, extN)...)
				}

				r := rand.New(rand.NewSource(1337))
				b.ReportAllocs()
				b.ResetTimer()
				// Report contextual metrics.
				b.ReportMetric(float64(sib), "series/op")
				b.ReportMetric(float64(L), "labels/op")

				for i := 0; i < b.N; i++ {
					j := r.Intn(max(1, sib))
					col.SetActiveInGroup(1, idxVals, grpVals, makeExtraValues(j, extN)...)
				}
			})
		}
	}
}

// DeleteByGroup: populate one (index,group) bucket with S siblings, then time deletion.
// We populate once, then loop: delete (timed) -> repopulate (untimed).
func Benchmark_DynamicGaugeCollector_DeleteByGroup(b *testing.B) {
	var tuples [][3]int
	var sibs []int

	tuples, sibs = labelVariations, siblings

	for _, t := range tuples {
		idxN, grpN, extN := t[0], t[1], t[2]
		if grpN == 0 {
			continue
		}
		L := labelsCount(idxN, grpN, extN)
		for _, sib := range sibs {
			name := fmt.Sprintf("idx=%d_grp=%d_ext=%d/siblings=%d", idxN, grpN, extN, sib)
			b.Run(name, func(b *testing.B) {
				col := newParamCollector("bench_del_group", idxN, grpN, extN)
				idxVals := makeIndexValues(1, idxN)
				grpVals := makeGroupValues(1, grpN)

				// Initial population (not timed).
				for j := 0; j < sib; j++ {
					col.Set(1, idxVals, grpVals, makeExtraValues(j, extN)...)
				}

				b.ReportAllocs()
				b.ResetTimer()
				// Report contextual metrics.
				b.ReportMetric(float64(sib), "series/op")
				b.ReportMetric(float64(L), "labels/op")

				for i := 0; i < b.N; i++ {
					_ = col.DeleteByGroup(idxVals, grpVals...)
					b.StopTimer()
					// Repopulate same siblings (not timed).
					for j := 0; j < sib; j++ {
						col.Set(1, idxVals, grpVals, makeExtraValues(j, extN)...)
					}
					b.StartTimer()
				}
			})
		}
	}
}

// DeleteByIndex: populate a single index with G groups and S siblings per group, then time deletion.
// Populate once, then loop: delete (timed) -> repopulate (untimed).
func Benchmark_DynamicGaugeCollector_DeleteByIndex(b *testing.B) {
	var tuples [][3]int
	var groupsList, sibs []int
	tuples, groupsList, sibs = labelVariations, groupCount, siblings

	for _, t := range tuples {
		idxN, grpN, extN := t[0], t[1], t[2]
		L := labelsCount(idxN, grpN, extN)

		for _, groups := range groupsList {
			for _, sib := range sibs {
				name := fmt.Sprintf("idx=%d_grp=%d_ext=%d/groups=%d_siblings=%d", idxN, grpN, extN, groups, sib)
				b.Run(name, func(b *testing.B) {
					col := newParamCollector("bench_del_index", idxN, grpN, extN)
					idxVals := makeIndexValues(2, idxN)

					// Initial population (not timed).
					for g := 0; g < groups; g++ {
						grpVals := makeGroupValues(g, grpN)
						for s := 0; s < sib; s++ {
							col.Set(1, idxVals, grpVals, makeExtraValues(s, extN)...)
						}
					}

					b.ReportAllocs()
					b.ResetTimer()
					// Report contextual metrics.
					b.ReportMetric(float64(sib), "series/op")
					b.ReportMetric(float64(L), "labels/op")

					for i := 0; i < b.N; i++ {
						_ = col.DeleteByIndex(idxVals...)
						b.StopTimer()
						// Repopulate same set (not timed).
						for g := 0; g < groups; g++ {
							grpVals := makeGroupValues(g, grpN)
							for s := 0; s < sib; s++ {
								col.Set(1, idxVals, grpVals, makeExtraValues(s, extN)...)
							}
						}
						b.StartTimer()
					}
				})
			}
		}
	}
}
