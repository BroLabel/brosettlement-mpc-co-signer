// Package metrics records the deliberately small, non-secret operational
// surface exported by the co-signer. It never accepts arbitrary metric names
// or labels: rejected observations are silently dropped so callers cannot turn
// observability into a secret or high-cardinality data channel.
package metrics

import (
	"fmt"
	"maps"
	"runtime"
	"sort"
	"strings"
	"sync"
)

type Labels map[string]string

type Spec struct {
	Labels map[string][]string
}

type Registry struct {
	mu     sync.RWMutex
	values map[string]map[string]float64
}

var Default = NewRegistry()

func NewRegistry() *Registry {
	return &Registry{values: make(map[string]map[string]float64)}
}

func (r *Registry) Inc(name string, labels Labels) { r.Add(name, labels, 1) }

func (r *Registry) Add(name string, labels Labels, value float64) {
	if r == nil || !validLabels(name, labels) {
		return
	}
	key := labelKey(labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values[name] == nil {
		r.values[name] = make(map[string]float64)
	}
	r.values[name][key] += value
}

func (r *Registry) Set(name string, labels Labels, value float64) {
	if r == nil || !validLabels(name, labels) {
		return
	}
	key := labelKey(labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values[name] == nil {
		r.values[name] = make(map[string]float64)
	}
	r.values[name][key] = value
}

func (r *Registry) Observe(name string, labels Labels, value float64) { r.Set(name, labels, value) }

func (r *Registry) Snapshot() map[string]map[string]float64 {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]map[string]float64, len(r.values))
	for name, entries := range r.values {
		out[name] = maps.Clone(entries)
	}
	return out
}

// Render returns stable, test-friendly lines. A separate HTTP transport can
// consume it without being granted access to any store or decrypted artifact.
func (r *Registry) Render() []string {
	snapshot := r.Snapshot()
	names := make([]string, 0, len(snapshot))
	for name := range snapshot {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0)
	for _, name := range names {
		keys := make([]string, 0, len(snapshot[name]))
		for key := range snapshot[name] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			lines = append(lines, fmt.Sprintf("%s{%s}=%g", name, key, snapshot[name][key]))
		}
	}
	return lines
}

func validLabels(name string, labels Labels) bool {
	spec, ok := Contract()[name]
	if !ok || len(labels) != len(spec.Labels) {
		return false
	}
	for key, allowed := range spec.Labels {
		value, ok := labels[key]
		if !ok || !contains(allowed, value) {
			return false
		}
	}
	return true
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func labelKey(labels Labels) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ",")
}

// ObserveRuntime samples only process-wide runtime values; it does not inspect
// sessions, artifacts, or caller data.
func ObserveRuntime() {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	Default.Set("go_heap_bytes", nil, float64(memory.HeapAlloc))
	Default.Set("go_goroutines", nil, float64(runtime.NumGoroutine()))
}
