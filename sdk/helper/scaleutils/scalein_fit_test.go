// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package scaleutils

import (
	"testing"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad-autoscaler/sdk"
	"github.com/hashicorp/nomad-autoscaler/sdk/helper/scaleutils/noderesources"
	"github.com/hashicorp/nomad/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures are shared with the noderesources package, see the README of
// its capacity cluster. Free CPU and memory of its nodes: n-a 2000/4000, n-b
// 1100/2100 (and 1900/3900 of system job allocations), n-c 1000/2000, n-d
// 2500/5000 and n-empty 4000/8000.
func TestClusterScaleUtils_limitScaleInByFit(t *testing.T) {
	enabled := map[string]string{
		sdk.TargetConfigKeyClass:           "app",
		sdk.TargetConfigKeyScaleInFitCheck: "true",
	}

	testCases := []struct {
		name        string
		cfg         map[string]string
		overrides   map[string]string
		selected    []string
		expectedIDs []string
		expectedErr string
		expectNoOp  bool
	}{
		{
			name: "allocations of the node fit elsewhere, the system job allocation does not have to move",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-none.json",
			},
			selected:    []string{"n-b"},
			expectedIDs: []string{"n-b"},
		},
		{
			name: "selection is reduced to what fits",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-none.json",
			},
			selected:    []string{"n-b", "n-c"},
			expectedIDs: []string{"n-b"},
		},
		{
			name: "no node can be removed",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-none.json",
			},
			selected:   []string{"n-c"},
			expectNoOp: true,
		},
		{
			name: "no node can be removed from the end of a longer selection",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-none.json",
			},
			selected:   []string{"n-c", "n-b"},
			expectNoOp: true,
		},
		{
			// The new node is empty so it has nothing to move, but the
			// blocked allocation can only run on it.
			name: "new empty node is kept while a blocked allocation fits only there",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes.json", "/v1/evaluations": "evaluations-big.json",
			},
			selected:   []string{"n-empty"},
			expectNoOp: true,
		},
		{
			name: "empty node is removed when no allocation is blocked",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes.json", "/v1/evaluations": "evaluations-none.json",
			},
			selected:    []string{"n-empty"},
			expectedIDs: []string{"n-empty"},
		},
		{
			name: "empty node is removed when the blocked allocation fits elsewhere",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes.json", "/v1/evaluations": "evaluations-small.json",
			},
			selected:    []string{"n-empty"},
			expectedIDs: []string{"n-empty"},
		},
		{
			// The allocation fits no node today, so removing a node does not
			// change that and must not prevent the scale-in.
			name: "allocation waiting for a scale-out does not block removing a node",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes-needs-capacity.json", "/v1/evaluations": "evaluations-big.json",
			},
			selected:    []string{"n-d"},
			expectedIDs: []string{"n-d"},
		},
		{
			name: "allocation that fits no node of the pool does not block removing a node",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes-needs-capacity.json", "/v1/evaluations": "evaluations-huge.json",
			},
			selected:    []string{"n-d"},
			expectedIDs: []string{"n-d"},
		},
		{
			// Allocations of an ineligible node still have to move, but the
			// node is not counted as having room.
			name: "ineligible node selected",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-none.json",
			},
			selected:    []string{"n-ineligible"},
			expectedIDs: []string{"n-ineligible"},
		},
		{
			name: "check disabled by false",
			cfg: map[string]string{
				sdk.TargetConfigKeyClass:           "app",
				sdk.TargetConfigKeyScaleInFitCheck: "false",
			},
			overrides: map[string]string{
				"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-none.json",
			},
			selected:    []string{"n-c"},
			expectedIDs: []string{"n-c"},
		},
		{
			name: "invalid option",
			cfg: map[string]string{
				sdk.TargetConfigKeyClass:           "app",
				sdk.TargetConfigKeyScaleInFitCheck: "maybe",
			},
			selected:    []string{"n-c"},
			expectedErr: sdk.TargetConfigKeyScaleInFitCheck,
		},
		{
			name: "blocked evaluations cannot be read",
			cfg:  enabled,
			overrides: map[string]string{
				"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "missing.json",
			},
			selected:    []string{"n-b"},
			expectedErr: "failed to read blocked allocations",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := noderesources.FixtureServer(t, "capacity", tc.overrides)
			client, err := api.NewClient(&api.Config{Address: ts.URL})
			require.NoError(t, err)

			// Use the stubs of the node list, like the selectors are given.
			nodes, _, err := client.Nodes().List(&api.QueryOptions{
				Params: map[string]string{"resources": "true"},
			})
			if tc.overrides != nil {
				require.NoError(t, err)
			}
			byID := make(map[string]*api.NodeListStub)
			for _, n := range nodes {
				byID[n.ID] = n
			}
			var selected []*api.NodeListStub
			for _, id := range tc.selected {
				n, ok := byID[id]
				if !ok {
					n = &api.NodeListStub{ID: id}
				}
				selected = append(selected, n)
			}

			c := &ClusterScaleUtils{log: hclog.NewNullLogger(), client: client}
			got, err := c.limitScaleInByFit(tc.cfg, selected)

			switch {
			case tc.expectedErr != "":
				assert.ErrorContains(t, err, tc.expectedErr)
				assert.Nil(t, got)
			case tc.expectNoOp:
				var noOp *sdk.TargetScalingNoOpError
				assert.ErrorAs(t, err, &noOp)
				assert.Nil(t, got)
			default:
				require.NoError(t, err)
				var gotIDs []string
				for _, n := range got {
					gotIDs = append(gotIDs, n.ID)
				}
				assert.Equal(t, tc.expectedIDs, gotIDs)
			}
		})
	}
}

func TestClusterScaleUtils_limitScaleInByFit_disabledMakesNoRequest(t *testing.T) {
	// Without a client any API call would panic.
	c := &ClusterScaleUtils{log: hclog.NewNullLogger()}
	selected := []*api.NodeListStub{{ID: "n-a"}, {ID: "n-b"}}

	for _, cfg := range []map[string]string{nil, {}, {sdk.TargetConfigKeyScaleInFitCheck: "false"}} {
		got, err := c.limitScaleInByFit(cfg, selected)
		require.NoError(t, err)
		assert.Equal(t, selected, got)
	}
}

func Test_keepPlaced(t *testing.T) {
	capacities := []*noderesources.NodeCapacity{
		{Allocatable: noderesources.Resources{CPU: 2000, MemoryMB: 2000}},
	}
	ask := func(cpu int64) noderesources.Ask {
		return noderesources.Ask{Resources: noderesources.Resources{CPU: cpu, MemoryMB: 100}}
	}

	testCases := []struct {
		name     string
		asks     []noderesources.Ask
		expected []noderesources.Resources
	}{
		{name: "none", expected: nil},
		{
			name:     "all fit together",
			asks:     []noderesources.Ask{ask(500), ask(700)},
			expected: []noderesources.Resources{{CPU: 500, MemoryMB: 100}, {CPU: 700, MemoryMB: 100}},
		},
		{
			// Each fits alone but not together. The ones left out are not
			// placed today whatever node is removed.
			name:     "only those that fit together are kept",
			asks:     []noderesources.Ask{ask(1500), ask(1500), ask(400)},
			expected: []noderesources.Resources{{CPU: 1500, MemoryMB: 100}, {CPU: 400, MemoryMB: 100}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, keepPlaced(tc.asks, capacities), tc.name)
		})
	}
}
