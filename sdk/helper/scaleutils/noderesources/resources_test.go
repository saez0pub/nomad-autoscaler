// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package noderesources

import (
	"testing"

	"github.com/hashicorp/nomad-autoscaler/sdk/helper/ptr"
	"github.com/hashicorp/nomad/api"
	"github.com/stretchr/testify/assert"
)

func TestResources_Fits(t *testing.T) {
	testCases := []struct {
		name           string
		free           Resources
		ask            Resources
		expectedOutput bool
	}{
		{name: "both fit", free: Resources{1000, 1000}, ask: Resources{500, 500}, expectedOutput: true},
		{name: "exactly equal", free: Resources{1000, 1000}, ask: Resources{1000, 1000}, expectedOutput: true},
		{name: "cpu too big", free: Resources{1000, 1000}, ask: Resources{1001, 10}, expectedOutput: false},
		{name: "memory too big", free: Resources{1000, 1000}, ask: Resources{10, 1001}, expectedOutput: false},
		{name: "overcommitted node", free: Resources{-100, 1000}, ask: Resources{10, 10}, expectedOutput: false},
		{name: "empty ask", free: Resources{0, 0}, ask: Resources{}, expectedOutput: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedOutput, tc.free.Fits(tc.ask), tc.name)
		})
	}
}

func TestAllocatable(t *testing.T) {
	testCases := []struct {
		name           string
		total          *api.NodeResources
		reserved       *api.NodeReservedResources
		expectedOutput Resources
	}{
		{
			name: "reserved resources are subtracted",
			total: &api.NodeResources{
				Cpu:    api.NodeCpuResources{CpuShares: 4200},
				Memory: api.NodeMemoryResources{MemoryMB: 8512},
			},
			reserved: &api.NodeReservedResources{
				Cpu:    api.NodeReservedCpuResources{CpuShares: 200},
				Memory: api.NodeReservedMemoryResources{MemoryMB: 512},
			},
			expectedOutput: Resources{CPU: 4000, MemoryMB: 8000},
		},
		{
			name: "nil reserved resources",
			total: &api.NodeResources{
				Cpu:    api.NodeCpuResources{CpuShares: 4000},
				Memory: api.NodeMemoryResources{MemoryMB: 8000},
			},
			expectedOutput: Resources{CPU: 4000, MemoryMB: 8000},
		},
		{
			name:           "nil node resources",
			expectedOutput: Resources{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedOutput, Allocatable(tc.total, tc.reserved), tc.name)
		})
	}
}

func TestAllocResources(t *testing.T) {
	testCases := []struct {
		name           string
		alloc          *api.Allocation
		expectedOutput Resources
	}{
		{
			name: "allocated resources of all tasks are summed",
			alloc: &api.Allocation{
				AllocatedResources: &api.AllocatedResources{
					Tasks: map[string]*api.AllocatedTaskResources{
						"web":     {Cpu: api.AllocatedCpuResources{CpuShares: 100}, Memory: api.AllocatedMemoryResources{MemoryMB: 50}},
						"sidecar": {Cpu: api.AllocatedCpuResources{CpuShares: 20}, Memory: api.AllocatedMemoryResources{MemoryMB: 10}},
						"nil":     nil,
					},
				},
			},
			expectedOutput: Resources{CPU: 120, MemoryMB: 60},
		},
		{
			name: "allocated resources win over the deprecated field",
			alloc: &api.Allocation{
				AllocatedResources: &api.AllocatedResources{
					Tasks: map[string]*api.AllocatedTaskResources{
						"web": {Cpu: api.AllocatedCpuResources{CpuShares: 100}, Memory: api.AllocatedMemoryResources{MemoryMB: 50}},
					},
				},
				Resources: &api.Resources{CPU: ptr.Of(999), MemoryMB: ptr.Of(999)},
			},
			expectedOutput: Resources{CPU: 100, MemoryMB: 50},
		},
		{
			name:           "deprecated field fallback",
			alloc:          &api.Allocation{Resources: &api.Resources{CPU: ptr.Of(100), MemoryMB: ptr.Of(50)}},
			expectedOutput: Resources{CPU: 100, MemoryMB: 50},
		},
		{
			name:           "partial deprecated field",
			alloc:          &api.Allocation{Resources: &api.Resources{CPU: ptr.Of(100)}},
			expectedOutput: Resources{CPU: 100},
		},
		{
			name:           "no resources reported",
			alloc:          &api.Allocation{},
			expectedOutput: Resources{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedOutput, AllocResources(tc.alloc), tc.name)
		})
	}
}

func TestIsNonTerminal(t *testing.T) {
	testCases := []struct {
		name           string
		desired        string
		client         string
		expectedOutput bool
	}{
		{name: "running", desired: "run", client: "running", expectedOutput: true},
		{name: "pending is already placed", desired: "run", client: "pending", expectedOutput: true},
		{name: "complete", desired: "run", client: "complete", expectedOutput: false},
		{name: "failed", desired: "run", client: "failed", expectedOutput: false},
		{name: "lost", desired: "run", client: "lost", expectedOutput: false},
		{name: "stopped by the server", desired: "stop", client: "running", expectedOutput: false},
		{name: "evicted by the server", desired: "evict", client: "running", expectedOutput: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			alloc := &api.Allocation{DesiredStatus: tc.desired, ClientStatus: tc.client}
			assert.Equal(t, tc.expectedOutput, IsNonTerminal(alloc), tc.name)
		})
	}
}

func TestIsSystemAlloc(t *testing.T) {
	testCases := []struct {
		name           string
		job            *api.Job
		expectedOutput bool
	}{
		{name: "system", job: &api.Job{Type: ptr.Of("system")}, expectedOutput: true},
		{name: "sysbatch", job: &api.Job{Type: ptr.Of("sysbatch")}, expectedOutput: true},
		{name: "service", job: &api.Job{Type: ptr.Of("service")}, expectedOutput: false},
		{name: "batch", job: &api.Job{Type: ptr.Of("batch")}, expectedOutput: false},
		{name: "job without type", job: &api.Job{}, expectedOutput: false},
		{name: "no job", job: nil, expectedOutput: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedOutput, IsSystemAlloc(&api.Allocation{Job: tc.job}), tc.name)
		})
	}
}

func TestPack(t *testing.T) {
	testCases := []struct {
		name           string
		asks           []Resources
		free           []Resources
		expectedOutput bool
	}{
		{
			name:           "nothing to place",
			free:           []Resources{{1000, 1000}},
			expectedOutput: true,
		},
		{
			name:           "nothing to place on no nodes",
			expectedOutput: true,
		},
		{
			name:           "no nodes",
			asks:           []Resources{{10, 10}},
			expectedOutput: false,
		},
		{
			name:           "each ask on its own node",
			asks:           []Resources{{1500, 1500}, {1500, 1500}},
			free:           []Resources{{2000, 2000}, {2000, 2000}},
			expectedOutput: true,
		},
		{
			name:           "several asks share a node",
			asks:           []Resources{{500, 500}, {500, 500}, {500, 500}},
			free:           []Resources{{1500, 1500}},
			expectedOutput: true,
		},
		{
			name:           "cluster total is enough but no single node fits",
			asks:           []Resources{{3000, 3000}},
			free:           []Resources{{2000, 2000}, {2000, 2000}},
			expectedOutput: false,
		},
		{
			name:           "each ask fits alone but not all together",
			asks:           []Resources{{1500, 100}, {1500, 100}},
			free:           []Resources{{2000, 1000}},
			expectedOutput: false,
		},
		{
			name:           "memory is the limit",
			asks:           []Resources{{100, 3000}},
			free:           []Resources{{4000, 2000}, {4000, 2500}},
			expectedOutput: false,
		},
		{
			name:           "larger ask first leaves room for the small ones",
			asks:           []Resources{{300, 300}, {300, 300}, {1300, 1300}},
			free:           []Resources{{1600, 1600}, {300, 300}},
			expectedOutput: true,
		},
		{
			name:           "overcommitted node is not used",
			asks:           []Resources{{100, 100}},
			free:           []Resources{{-500, 1000}, {50, 50}},
			expectedOutput: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			asks := append([]Resources(nil), tc.asks...)
			free := append([]Resources(nil), tc.free...)

			assert.Equal(t, tc.expectedOutput, Pack(tc.asks, tc.free), tc.name)

			// The inputs must be left as they were.
			assert.Equal(t, asks, append([]Resources(nil), tc.asks...))
			assert.Equal(t, free, append([]Resources(nil), tc.free...))
		})
	}
}
