// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package noderesources

import (
	"testing"

	"github.com/hashicorp/nomad/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCluster = "capacity"

// testClient returns a Nomad API client for the test server.
func testClient(t *testing.T, address string) *api.Client {
	t.Helper()

	cfg := api.DefaultConfig()
	cfg.Address = address
	client, err := api.NewClient(cfg)
	require.NoError(t, err)
	return client
}

// listNodes reads the node list of the fixture cluster.
func listNodes(t *testing.T, client *api.Client) []*api.NodeListStub {
	t.Helper()

	nodes, _, err := client.Nodes().List(&api.QueryOptions{Params: map[string]string{"resources": "true"}})
	require.NoError(t, err)
	return nodes
}

func TestSchedulable(t *testing.T) {
	testCases := []struct {
		name           string
		node           *api.NodeListStub
		expectedOutput bool
	}{
		{
			name:           "ready and eligible",
			node:           &api.NodeListStub{Status: "ready", SchedulingEligibility: "eligible"},
			expectedOutput: true,
		},
		{
			name:           "ineligible",
			node:           &api.NodeListStub{Status: "ready", SchedulingEligibility: "ineligible"},
			expectedOutput: false,
		},
		{
			name:           "draining",
			node:           &api.NodeListStub{Status: "ready", SchedulingEligibility: "eligible", Drain: true},
			expectedOutput: false,
		},
		{
			name:           "down",
			node:           &api.NodeListStub{Status: "down", SchedulingEligibility: "eligible"},
			expectedOutput: false,
		},
		{
			name:           "initializing",
			node:           &api.NodeListStub{Status: "initializing", SchedulingEligibility: "eligible"},
			expectedOutput: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedOutput, Schedulable(tc.node), tc.name)
		})
	}
}

func TestLoadCapacity(t *testing.T) {
	ts := FixtureServer(t, testCluster, nil)
	client := testClient(t, ts.URL)

	testCases := []struct {
		name                string
		node                *api.NodeListStub
		expectedAllocatable Resources
		expectedUsed        Resources
		expectedSystemUsed  Resources
		expectedFree        Resources
		expectedFresh       Resources
		expectedMovable     []Resources
	}{
		{
			name: "reserved resources, pending counts, terminal does not",
			node: &api.NodeListStub{
				ID:                "n-a",
				NodeResources:     &api.NodeResources{Cpu: api.NodeCpuResources{CpuShares: 4200}, Memory: api.NodeMemoryResources{MemoryMB: 8512}},
				ReservedResources: &api.NodeReservedResources{Cpu: api.NodeReservedCpuResources{CpuShares: 200}, Memory: api.NodeReservedMemoryResources{MemoryMB: 512}},
			},
			expectedAllocatable: Resources{4000, 8000},
			expectedUsed:        Resources{2000, 4000},
			expectedFree:        Resources{2000, 4000},
			expectedFresh:       Resources{4000, 8000},
			expectedMovable:     []Resources{{1500, 3000}, {500, 1000}},
		},
		{
			name: "system allocations are used but not movable",
			node: &api.NodeListStub{
				ID:            "n-b",
				NodeResources: &api.NodeResources{Cpu: api.NodeCpuResources{CpuShares: 4000}, Memory: api.NodeMemoryResources{MemoryMB: 8000}},
			},
			expectedAllocatable: Resources{4000, 8000},
			expectedUsed:        Resources{2900, 5900},
			expectedSystemUsed:  Resources{1900, 3900},
			expectedFree:        Resources{1100, 2100},
			expectedFresh:       Resources{2100, 4100},
			expectedMovable:     []Resources{{1000, 2000}},
		},
		{
			name:                "resources read from the node endpoint, deprecated allocation resources",
			node:                &api.NodeListStub{ID: "n-c"},
			expectedAllocatable: Resources{4000, 8000},
			expectedUsed:        Resources{3000, 6000},
			expectedFree:        Resources{1000, 2000},
			expectedFresh:       Resources{4000, 8000},
			expectedMovable:     []Resources{{3000, 6000}},
		},
		{
			name: "empty node",
			node: &api.NodeListStub{
				ID:            "n-empty",
				NodeResources: &api.NodeResources{Cpu: api.NodeCpuResources{CpuShares: 4000}, Memory: api.NodeMemoryResources{MemoryMB: 8000}},
			},
			expectedAllocatable: Resources{4000, 8000},
			expectedFree:        Resources{4000, 8000},
			expectedFresh:       Resources{4000, 8000},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoadCapacity(client, tc.node)
			require.NoError(t, err)

			assert.Equal(t, tc.expectedAllocatable, got.Allocatable, "allocatable")
			assert.Equal(t, tc.expectedUsed, got.Used, "used")
			assert.Equal(t, tc.expectedSystemUsed, got.SystemUsed, "system used")
			assert.Equal(t, tc.expectedFree, got.Free(), "free")
			assert.Equal(t, tc.expectedFresh, got.Fresh(), "fresh")
			assert.ElementsMatch(t, tc.expectedMovable, got.MovableAllocs(), "movable")
		})
	}
}

func TestLoadCapacity_errors(t *testing.T) {
	ts := FixtureServer(t, testCluster, nil)
	client := testClient(t, ts.URL)

	testCases := []struct {
		name string
		node *api.NodeListStub
	}{
		{name: "node info not found", node: &api.NodeListStub{ID: "n-unknown"}},
		{
			name: "allocations not found",
			node: &api.NodeListStub{
				ID:            "n-down",
				NodeResources: &api.NodeResources{Cpu: api.NodeCpuResources{CpuShares: 1}},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoadCapacity(client, tc.node)
			assert.Error(t, err, tc.name)
			assert.Nil(t, got, tc.name)
		})
	}
}

func TestLoadPoolCapacity(t *testing.T) {
	testCases := []struct {
		name        string
		nodesFile   string
		expectedIDs []string
	}{
		{
			// The allocations of the ineligible, draining and down nodes are
			// loaded for none of them. The draining and down ones have no
			// fixture, so reading them would fail the call.
			name:        "ineligible, draining and down nodes are skipped",
			nodesFile:   "nodes.json",
			expectedIDs: []string{"n-a", "n-b", "n-c", "n-empty"},
		},
		{
			name:        "initializing node is skipped",
			nodesFile:   "nodes-init.json",
			expectedIDs: []string{"n-a", "n-b", "n-c"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := FixtureServer(t, testCluster, map[string]string{"/v1/nodes": tc.nodesFile})
			client := testClient(t, ts.URL)

			got, err := LoadPoolCapacity(client, listNodes(t, client))
			require.NoError(t, err)

			var gotIDs []string
			for _, c := range got {
				gotIDs = append(gotIDs, c.Node.ID)
			}
			assert.ElementsMatch(t, tc.expectedIDs, gotIDs, tc.name)
		})
	}
}

func TestHasInitializing(t *testing.T) {
	testCases := []struct {
		name           string
		nodesFile      string
		expectedOutput bool
	}{
		{name: "no initializing node", nodesFile: "nodes.json", expectedOutput: false},
		{name: "one initializing node", nodesFile: "nodes-init.json", expectedOutput: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := FixtureServer(t, testCluster, map[string]string{"/v1/nodes": tc.nodesFile})
			client := testClient(t, ts.URL)

			assert.Equal(t, tc.expectedOutput, HasInitializing(listNodes(t, client)), tc.name)
		})
	}
}
