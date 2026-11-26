// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package noderesources

import (
	"encoding/json"
	"testing"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlockedAsks(t *testing.T) {
	testCases := []struct {
		name          string
		evalsFile     string
		poolClass     string
		expectedAsks  []Ask
		expectedError bool
	}{
		{
			// The file has a request exhausted on the pool and also one
			// constraint filtered, one for another node class, a system job,
			// an evaluation that is not blocked, one for another node pool and
			// one whose job has been purged. Those are all left out.
			name:      "only requests limited by the resources of the pool",
			evalsFile: "evaluations.json",
			poolClass: "app",
			expectedAsks: []Ask{
				{EvalID: "e1", Namespace: "default", JobID: "big", TaskGroup: "web", Resources: Resources{3000, 6000}},
				{EvalID: "e6", Namespace: "default", JobID: "huge", TaskGroup: "web", Resources: Resources{9000, 16000}},
				{EvalID: "e7", Namespace: "default", JobID: "small", TaskGroup: "web", Resources: Resources{500, 512}},
			},
		},
		{
			name:         "constraint filtered only",
			evalsFile:    "evaluations-constraint.json",
			poolClass:    "app",
			expectedAsks: nil,
		},
		{
			name:         "no blocked evaluation",
			evalsFile:    "evaluations-none.json",
			poolClass:    "app",
			expectedAsks: nil,
		},
		{
			name:         "exhausted on another node class",
			evalsFile:    "evaluations-big.json",
			poolClass:    "gpu",
			expectedAsks: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := FixtureServer(t, testCluster, map[string]string{"/v1/evaluations": tc.evalsFile})
			client := testClient(t, ts.URL)

			// The pool is the nodes of the requested class, relabelled so the
			// same fixtures can be used for a pool the evaluations do not
			// concern.
			var pool []*api.NodeListStub
			for _, n := range listNodes(t, client) {
				n.NodeClass = tc.poolClass
				pool = append(pool, n)
			}

			got, err := BlockedAsks(client, pool, hclog.NewNullLogger())
			require.NoError(t, err)
			assert.Equal(t, tc.expectedAsks, got, tc.name)
		})
	}
}

func TestBlockedAsks_apiError(t *testing.T) {
	// Pointing at a server which has no evaluations file returns a 404.
	ts := FixtureServer(t, testCluster, map[string]string{"/v1/evaluations": "missing.json"})
	client := testClient(t, ts.URL)

	got, err := BlockedAsks(client, nil, hclog.NewNullLogger())
	assert.Error(t, err)
	assert.Nil(t, got)
}

func Test_poolAttributes_exhaustedBy(t *testing.T) {
	pool := func(class, nodePool, dc string) []*api.NodeListStub {
		return []*api.NodeListStub{{NodeClass: class, NodePool: nodePool, Datacenter: dc}}
	}
	cpu := map[string]int{"cpu": 1}

	testCases := []struct {
		name           string
		pool           []*api.NodeListStub
		metric         *api.AllocationMetric
		expectedOutput bool
	}{
		{
			name:           "nil metric",
			pool:           pool("app", "default", "dc1"),
			expectedOutput: false,
		},
		{
			name:           "cpu exhausted on the class",
			pool:           pool("app", "default", "dc1"),
			metric:         &api.AllocationMetric{DimensionExhausted: cpu, ClassExhausted: map[string]int{"app": 1}},
			expectedOutput: true,
		},
		{
			name:           "memory exhausted on the class",
			pool:           pool("app", "default", "dc1"),
			metric:         &api.AllocationMetric{DimensionExhausted: map[string]int{"memory": 2}, ClassExhausted: map[string]int{"app": 1}},
			expectedOutput: true,
		},
		{
			name:           "no exhausted dimension, nodes were filtered",
			pool:           pool("app", "default", "dc1"),
			metric:         &api.AllocationMetric{ClassFiltered: map[string]int{"app": 1}, ClassExhausted: map[string]int{"app": 1}},
			expectedOutput: false,
		},
		{
			name:           "dimension that is not cpu or memory",
			pool:           pool("app", "default", "dc1"),
			metric:         &api.AllocationMetric{DimensionExhausted: map[string]int{"disk": 1}, ClassExhausted: map[string]int{"app": 1}},
			expectedOutput: false,
		},
		{
			name:           "exhausted on another class",
			pool:           pool("app", "default", "dc1"),
			metric:         &api.AllocationMetric{DimensionExhausted: cpu, ClassExhausted: map[string]int{"gpu": 1}},
			expectedOutput: false,
		},
		{
			name:           "pool without class cannot be excluded by class",
			pool:           pool("", "default", "dc1"),
			metric:         &api.AllocationMetric{DimensionExhausted: cpu, ClassExhausted: map[string]int{"gpu": 1}},
			expectedOutput: true,
		},
		{
			name:           "other node pool",
			pool:           pool("app", "default", "dc1"),
			metric:         &api.AllocationMetric{DimensionExhausted: cpu, ClassExhausted: map[string]int{"app": 1}, NodePool: "gpu"},
			expectedOutput: false,
		},
		{
			name:           "node pool unknown on the nodes, as before Nomad node pools",
			pool:           pool("app", "", "dc1"),
			metric:         &api.AllocationMetric{DimensionExhausted: cpu, ClassExhausted: map[string]int{"app": 1}, NodePool: "default"},
			expectedOutput: true,
		},
		{
			name:           "other datacenter",
			pool:           pool("app", "default", "dc1"),
			metric:         &api.AllocationMetric{DimensionExhausted: cpu, ClassExhausted: map[string]int{"app": 1}, NodesAvailable: map[string]int{"dc2": 3}},
			expectedOutput: false,
		},
		{
			name:           "one of several datacenters",
			pool:           pool("app", "default", "dc1"),
			metric:         &api.AllocationMetric{DimensionExhausted: cpu, ClassExhausted: map[string]int{"app": 1}, NodesAvailable: map[string]int{"dc1": 1, "dc2": 3}},
			expectedOutput: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedOutput, newPoolAttributes(tc.pool).exhaustedBy(tc.metric), tc.name)
		})
	}
}

func TestClassify(t *testing.T) {
	// Free capacity of the nodes is 2000/4000 and 1100/2100. A new node
	// offers 4000/8000, or 2100/4100 where the system job is large.
	nodes := []*NodeCapacity{
		{Allocatable: Resources{4000, 8000}, Used: Resources{2000, 4000}},
		{Allocatable: Resources{4000, 8000}, Used: Resources{2900, 5900}, SystemUsed: Resources{1900, 3900}},
	}
	ask := func(name string, cpu, mem int64) Ask {
		return Ask{EvalID: name, Resources: Resources{cpu, mem}}
	}

	testCases := []struct {
		name     string
		asks     []Ask
		nodes    []*NodeCapacity
		expected Classification
	}{
		{
			name:  "no asks",
			nodes: nodes,
		},
		{
			name:     "fits the free capacity of a single node",
			asks:     []Ask{ask("a", 2000, 4000)},
			nodes:    nodes,
			expected: Classification{Placeable: []Ask{ask("a", 2000, 4000)}},
		},
		{
			// 3100 MHz are free in total, but on no single node.
			name:     "fits only a new node",
			asks:     []Ask{ask("a", 3000, 6000)},
			nodes:    nodes,
			expected: Classification{NeedsCapacity: []Ask{ask("a", 3000, 6000)}},
		},
		{
			name:     "does not fit a new node either",
			asks:     []Ask{ask("a", 4001, 100)},
			nodes:    nodes,
			expected: Classification{Unsatisfiable: []Ask{ask("a", 4001, 100)}},
		},
		{
			name:  "mixed",
			asks:  []Ask{ask("a", 100, 100), ask("b", 3000, 6000), ask("c", 9000, 9000)},
			nodes: nodes,
			expected: Classification{
				Placeable:     []Ask{ask("a", 100, 100)},
				NeedsCapacity: []Ask{ask("b", 3000, 6000)},
				Unsatisfiable: []Ask{ask("c", 9000, 9000)},
			},
		},
		{
			name:     "no nodes to size against",
			asks:     []Ask{ask("a", 1, 1)},
			expected: Classification{Unsatisfiable: []Ask{ask("a", 1, 1)}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, Classify(tc.asks, tc.nodes), tc.name)
		})
	}
}

// clusterTestEvalJSON is a blocked evaluation read from a real cluster. Identifiers
// are replaced with placeholders, the resource values are kept.
const clusterTestEvalJSON = `{
  "ID": "00000000-0000-0000-0000-000000000001",
  "Namespace": "default",
  "JobID": "job-blocked",
  "Status": "blocked",
  "TriggeredBy": "queued-allocs",
  "FailedTGAllocs": {
    "group-blocked": {
      "AllocationTime": 103267,
      "ClassExhausted": {"default": 2},
      "ClassFiltered": null,
      "CoalescedFailures": 1,
      "ConstraintFiltered": null,
      "DimensionExhausted": {"memory": 1, "cpu": 1},
      "NodePool": "default",
      "NodesAvailable": {"dc1": 2},
      "NodesEvaluated": 2,
      "NodesExhausted": 2,
      "NodesFiltered": 0,
      "NodesInPool": 2,
      "ResourcesExhausted": {
        "task-blocked": {"CPU": 1000, "MemoryMB": 3000}
      }
    }
  }
}`

func TestClusterTestBlockedEval_decodesAndIsPoolLimited(t *testing.T) {
	var eval api.Evaluation
	require.NoError(t, json.Unmarshal([]byte(clusterTestEvalJSON), &eval))
	require.Equal(t, api.EvalStatusBlocked, eval.Status)

	metric := eval.FailedTGAllocs["group-blocked"]
	require.NotNil(t, metric)
	assert.Equal(t, 1, metric.DimensionExhausted["cpu"])
	assert.Equal(t, 1, metric.DimensionExhausted["memory"])

	testCases := []struct {
		name           string
		pool           []*api.NodeListStub
		expectedOutput bool
	}{
		{
			name:           "nodes of the cluster test pool with class default",
			pool:           []*api.NodeListStub{{NodeClass: "default", NodePool: "default", Datacenter: "dc1"}},
			expectedOutput: true,
		},
		{
			name:           "nodes of the cluster test pool without class",
			pool:           []*api.NodeListStub{{NodeClass: "", NodePool: "default", Datacenter: "dc1"}},
			expectedOutput: true,
		},
		{
			name:           "same nodes but another datacenter",
			pool:           []*api.NodeListStub{{NodeClass: "default", NodePool: "default", Datacenter: "dc2"}},
			expectedOutput: false,
		},
		{
			name:           "same nodes but another node pool",
			pool:           []*api.NodeListStub{{NodeClass: "default", NodePool: "gpu", Datacenter: "dc1"}},
			expectedOutput: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedOutput, newPoolAttributes(tc.pool).exhaustedBy(metric), tc.name)
		})
	}
}

// TestClusterTestPool_classifiesBlockedAsk uses the capacity of the two nodes of
// the pool, read from the API, with the blocked task-blocked ask.
// Both nodes have 10000 MHz and 15782 MB with 500 MHz and 512 MB reserved. The
// used values are the sums of the running and pending allocations.
func TestClusterTestPool_classifiesBlockedAsk(t *testing.T) {
	nodes := []*NodeCapacity{
		{ // node-a: 21 allocations, 1 system job
			Allocatable: Resources{CPU: 9500, MemoryMB: 15270},
			Used:        Resources{CPU: 6560, MemoryMB: 14614},
			SystemUsed:  Resources{CPU: 200, MemoryMB: 128},
		},
		{ // node-b: 4 allocations, 1 system job
			Allocatable: Resources{CPU: 9500, MemoryMB: 15270},
			Used:        Resources{CPU: 9200, MemoryMB: 11868},
			SystemUsed:  Resources{CPU: 200, MemoryMB: 128},
		},
	}

	testCases := []struct {
		name     string
		ask      Resources
		expected Classification
	}{
		{
			name:     "job spec ask of 500 MHz and 1500 MB",
			ask:      Resources{CPU: 500, MemoryMB: 1500},
			expected: Classification{NeedsCapacity: []Ask{{Resources: Resources{CPU: 500, MemoryMB: 1500}}}},
		},
		{
			name:     "ask of 1000 MHz and 3000 MB, as in ResourcesExhausted",
			ask:      Resources{CPU: 1000, MemoryMB: 3000},
			expected: Classification{NeedsCapacity: []Ask{{Resources: Resources{CPU: 1000, MemoryMB: 3000}}}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify([]Ask{{Resources: tc.ask}}, nodes)
			assert.Equal(t, tc.expected, got, tc.name)
		})
	}
}
