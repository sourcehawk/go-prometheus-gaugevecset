package gauge_vec_set

import (
	"fmt"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// We hash all label values into a single string separated by this character
	labelHashSeparatorChar = "`"
	// If the label values contain the labelHashSeparator, replace it with this value
	labelHashCollisionReplacementChar = ""
)

// GaugeVecSet wraps a Prometheus GaugeVec and keeps a 3-level index:
//
//	indexKey -> groupKey -> set(fullKey)
//
// Label order in the metric is:
//
//	allLabels = indexLabels + groupLabels + extraLabels
//
// and label values follow the same order for all operations.
//
// Semantics:
//   - "Index" labels identify a higher-level key for bulk operations (e.g., controller,name,namespace).
//   - "Group" labels define a mutually exclusive scope (e.g., condition). Within a given (index,group),
//     you often want exactly one active series (enum-like behavior).
//   - "Extra" labels are additional attributes (e.g., status, reason).
//
// Why this structure?
//   - O(1)-ish lookup of all series in a given (index,group), enabling fast zero/delete when flipping
//     enum-like states (e.g., status transitions) without scanning unrelated series.
//
// Cardinality note:
//
//	This collector maintains an in-memory index of *every* exported series, keyed by index/group.
//	If the set of index values grows without bound, memory usage will grow accordingly. Prefer bounded
//	index/group label spaces and avoid high-cardinality values.
type GaugeVecSet struct {
	metric *prometheus.GaugeVec

	indexLabels []string // labels that define the deletion index (required; order matters)
	groupLabels []string // labels that define a mutually-exclusive group (optional; order matters)
	extraLabels []string // additional dynamic labels not used for grouping (optional; order matters)

	// Nested index: indexKey -> groupKey -> set(fullKey)
	indexes map[string]map[string]map[string]struct{}

	mu sync.RWMutex
}

// NewGaugeVecSet constructs a GaugeVecSet.
//
// Parameters:
//   - namespace, subsystem, name, help: standard Prometheus metadata.
//   - indexLabels: at least one label (e.g., {"controller","name","namespace"}).
//   - groupLabels: labels that define the mutually-exclusive scope (can be empty).
//   - extraLabels: remaining labels (can be empty).
//
// The exported metric's label order is: indexLabels + groupLabels + extraLabels.
//
// Returns an *unregistered* collector; register it with a Prometheus registry yourself.
// Example:
//
//	col := NewGaugeVecSet(ns, sub, "condition", help, []string{"controller","name","namespace"}, []string{"condition"}, []string{"status","reason"})
//	prometheus.MustRegister(col)
func NewGaugeVecSet(
	namespace, subsystem, name, help string,
	indexLabels []string,
	groupLabels []string,
	extraLabels ...string,
) *GaugeVecSet {
	if len(indexLabels) == 0 {
		panic("NewMultiIndexGaugeCollector: at least one index label is required")
	}
	allLabels := buildAllValues(indexLabels, groupLabels, extraLabels)

	// Validate that all labels are unique
	seen := make(map[string]struct{}, len(allLabels))
	for _, label := range allLabels {
		if _, exists := seen[label]; exists {
			panic(
				fmt.Sprintf(
					"GaugeVecSet: duplicate label %q detected across index/group/extra labels", label),
			)
		}
		seen[label] = struct{}{}
	}

	gv := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      name,
		Help:      help,
	}, allLabels)

	return &GaugeVecSet{
		metric:      gv,
		indexLabels: indexLabels,
		groupLabels: groupLabels,
		extraLabels: extraLabels,
		indexes:     make(map[string]map[string]map[string]struct{}),
	}
}

// Describe implements prometheus.Collector.
func (c *GaugeVecSet) Describe(ch chan<- *prometheus.Desc) {
	c.metric.Describe(ch)
}

// Collect implements prometheus.Collector.
func (c *GaugeVecSet) Collect(ch chan<- prometheus.Metric) {
	c.metric.Collect(ch)
}

// containLabelHashSeparator returns true if any of the strings in the given array contains the labelHashSeparatorChar
// In most cases we're not going to encounter labelHashSeparatorChar in the label values.
// So we prevent a new array allocation by checking if the character is present.
func containLabelHashSeparator(values []string) bool {
	for _, v := range values {
		if strings.Contains(v, labelHashSeparatorChar) {
			return true
		}
	}
	return false
}

// removeLabelHashSeparator returns a new slice with any labelHashSeparatorChar replaced by labelHashCollisionReplacementChar
func removeLabelHashSeparator(values []string) []string {
	clean := make([]string, len(values))
	for i, v := range values {
		clean[i] = strings.ReplaceAll(v, labelHashSeparatorChar, labelHashCollisionReplacementChar)
	}
	return clean
}

// buildAllValues concatenates values in the canonical order: index + group + extra.
func buildAllValues(indexValues, groupValues, extraValues []string) []string {
	allVals := make([]string, 0, len(indexValues)+len(groupValues)+len(extraValues))
	allVals = append(allVals, indexValues...)
	allVals = append(allVals, groupValues...)
	allVals = append(allVals, extraValues...)

	if !containLabelHashSeparator(allVals) {
		return allVals
	}
	return removeLabelHashSeparator(allVals)
}

// serialize joins label values with the separator labelHashSeparatorChar.
func serialize(labelValues []string) string {
	if !containLabelHashSeparator(labelValues) {
		return strings.Join(labelValues, labelHashSeparatorChar)
	}

	return strings.Join(removeLabelHashSeparator(labelValues), labelHashSeparatorChar)
}

// deserialize the hash into label values
func deserialize(s string) []string {
	return strings.Split(s, labelHashSeparatorChar)
}

// listHashesForIndex returns a flat slice of all hashes under indexKey.
// Safe for concurrent use, holds RLock briefly.
func (c *GaugeVecSet) listHashesForIndex(indexKey string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	groupMap, ok := c.indexes[indexKey]
	if !ok {
		return nil
	}

	var hashes []string
	for _, group := range groupMap {
		for hash := range group {
			hashes = append(hashes, hash)
		}
	}

	return hashes
}

// listHashesForGroup returns all hashes under (indexKey, groupKey).
// Safe for concurrent use, holds RLock briefly.
func (c *GaugeVecSet) listHashesForGroup(indexKey, groupKey string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	groupMap, ok := c.indexes[indexKey]
	if !ok {
		return nil
	}
	group, ok := groupMap[groupKey]
	if !ok {
		return nil
	}
	hashes := make([]string, 0, len(group))
	for hash := range group {
		hashes = append(hashes, hash)
	}
	return hashes
}

// validateIndexValues ensures the arity of indexValues matches the configured indexLabels.
func (c *GaugeVecSet) validateIndexValues(indexValues []string) {
	if len(indexValues) != len(c.indexLabels) {
		panic(fmt.Sprintf("expected %d indexValues for labels %v, got %d",
			len(c.indexLabels), c.indexLabels, len(indexValues)))
	}
}

// validateGroupValues ensures the arity of groupValues matches the configured groupLabels.
func (c *GaugeVecSet) validateGroupValues(groupValues []string) {
	if len(groupValues) != len(c.groupLabels) {
		panic(
			fmt.Sprintf("expected %d groupValues for labels %v, got %d",
				len(c.groupLabels), c.groupLabels, len(groupValues)))
	}
}

// validateExtraValues ensures the arity of extraValues matches the configured extraLabels.
func (c *GaugeVecSet) validateExtraValues(extraValues []string) {
	if len(extraValues) != len(c.extraLabels) {
		panic(fmt.Sprintf("expected %d extraValues for labels %v, got %d",
			len(c.extraLabels), c.extraLabels, len(extraValues)))
	}
}

// pruneIndex removes the entire indexKey bucket from the cache.
// Holds a write lock momentarily while removing the index.
func (c *GaugeVecSet) pruneIndex(indexKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.indexes, indexKey)
}

// pruneGroup removes the (indexKey, groupKey) bucket from the cache and prunes the index if empty.
// Holds a write lock momentarily while removing the group.
func (c *GaugeVecSet) pruneGroup(indexKey, groupKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if groupMap, ok := c.indexes[indexKey]; ok {
		delete(groupMap, groupKey)
		if len(groupMap) == 0 {
			delete(c.indexes, indexKey)
		}
	}
}

// cache records the full label tuple under (indexKey, groupKey).
func (c *GaugeVecSet) cache(indexValues, groupValues, allValues []string) {
	indexKey := serialize(indexValues)
	groupKey := serialize(groupValues)
	fullKey := serialize(allValues)
	c.cacheWithKeys(indexKey, groupKey, fullKey)
}

// cacheWithKeys records a fullKey under the nested (indexKey, groupKey) maps.
func (c *GaugeVecSet) cacheWithKeys(indexKey, groupKey, fullKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	indexSet, ok := c.indexes[indexKey]
	if !ok {
		indexSet = make(map[string]map[string]struct{})
		c.indexes[indexKey] = indexSet
	}
	groupSet, ok := indexSet[groupKey]
	if !ok {
		groupSet = make(map[string]struct{})
		indexSet[groupKey] = groupSet
	}

	groupSet[fullKey] = struct{}{}
}

// Set assigns the Gauge value for the series identified by (index, group)
// This does not modify sibling series. Use SetExclusiveInGroup or SetActiveInGroup to enforce enum-like exclusivity.
func (c *GaugeVecSet) Set(
	value float64,
	indexValues []string,
	groupValues []string,
	extraValues ...string,
) {
	c.validateIndexValues(indexValues)
	c.validateGroupValues(groupValues)
	c.validateExtraValues(extraValues)

	allVals := buildAllValues(indexValues, groupValues, extraValues)
	c.metric.WithLabelValues(allVals...).Set(value)
	c.cache(indexValues, groupValues, allVals)
}

// SetActiveInGroup sets the target series to `value` and zeroes **all other series**
// in the same (index, group) bucket. If no groupLabels were configured, this behaves like Set.
//
// Use this for enum-like series where exactly one variant should be active within a group and inactive metrics
// should be present. Prefer SetExclusiveInGroup if your metrics have high cardinality.
func (c *GaugeVecSet) SetActiveInGroup(
	value float64,
	indexValues []string,
	groupValues []string,
	extraValues ...string,
) {
	if len(c.groupLabels) == 0 {
		c.Set(value, indexValues, groupValues, extraValues...)
		return
	}
	c.validateIndexValues(indexValues)
	c.validateGroupValues(groupValues)
	c.validateExtraValues(extraValues)

	allValues := buildAllValues(indexValues, groupValues, extraValues)
	fullKey := serialize(allValues)
	indexKey := serialize(indexValues)
	groupKey := serialize(groupValues)

	// Snapshot hashes (no locks held during Prometheus calls).
	hashes := c.listHashesForGroup(indexKey, groupKey)
	for _, hash := range hashes {
		if hash == fullKey {
			continue
		}
		c.metric.WithLabelValues(deserialize(hash)...).Set(0)
	}

	// Set target and cache.
	c.metric.WithLabelValues(allValues...).Set(value)
	c.cacheWithKeys(indexKey, groupKey, fullKey)
}

// SetGroup deletes all other series for (index, group) and then sets the given one to the passed in value.
// Prefer this method over SetActiveInGroup when your labels have high cardinality.
//
// Example: If the cardinality of your (index, group) is ~10'000 and the cardinality of your extra labels is ~10:
//   - Using SetActiveInGroup causes the total cardinality of your time series to become 10'000 * 10 = 100k.
//   - Using SetGroup causes the total cardinality of your time series to stay 10'000 (10k * 1 = 10k).
func (c *GaugeVecSet) SetGroup(
	value float64, indexValues []string, groupValues []string, extraValues ...string,
) {
	_ = c.DeleteByGroup(indexValues, groupValues...)
	c.Set(value, indexValues, groupValues, extraValues...)
}

// DeleteByIndex removes all series whose index label-values tuple equals indexValues.
// Returns the number of deleted series.
func (c *GaugeVecSet) DeleteByIndex(indexValues ...string) (deleted int) {
	c.validateIndexValues(indexValues)

	indexKey := serialize(indexValues)
	hashes := c.listHashesForIndex(indexKey)

	for _, hash := range hashes {
		if c.metric.DeleteLabelValues(deserialize(hash)...) {
			deleted++
		}
	}
	c.pruneIndex(indexKey)

	return deleted
}

// DeleteByGroup removes all series for the given (indexValues, groupValues) pair.
// Returns the number of deleted series.
func (c *GaugeVecSet) DeleteByGroup(indexValues []string, groupValues ...string) (deleted int) {
	if len(c.groupLabels) == 0 {
		return 0
	}
	c.validateIndexValues(indexValues)
	c.validateGroupValues(groupValues)

	indexKey := serialize(indexValues)
	groupKey := serialize(groupValues)
	hashes := c.listHashesForGroup(indexKey, groupKey)

	for _, hash := range hashes {
		if c.metric.DeleteLabelValues(deserialize(hash)...) {
			deleted++
		}
	}

	c.pruneGroup(indexKey, groupKey)

	return deleted
}
