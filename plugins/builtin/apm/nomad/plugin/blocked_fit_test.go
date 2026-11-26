// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package plugin

import (
	"testing"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad-autoscaler/sdk"
	"github.com/hashicorp/nomad-autoscaler/sdk/helper/scaleutils/noderesources"
	"github.com/hashicorp/nomad/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures are shared with the noderesources package, see the README of
// its capacity cluster for the layout of the nodes. With nodes-fragmented.json
// the free capacity of the pool is 4100 MHz and 8100 MB in total, yet no node
// has more than 2000 MHz and 4000 MB free.
func TestAPMPlugin_queryBlockedFit(t *testing.T) {
	const query = "node_fit-deficit_blocked/node_class=app"

	testCases := []struct {
		name          string
		query         string
		overrides     map[string]string
		expectedValue float64
		expectedErr   string
	}{
		{
			name:          "allocation fits no single node but a new one: scale out",
			overrides:     map[string]string{"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-big.json"},
			expectedValue: 1,
		},
		{
			name:          "the same allocation fits an existing node",
			overrides:     map[string]string{"/v1/nodes": "nodes.json", "/v1/evaluations": "evaluations-big.json"},
			expectedValue: 0,
		},
		{
			name:          "no blocked evaluation",
			overrides:     map[string]string{"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-none.json"},
			expectedValue: 0,
		},
		{
			name:          "small allocation blocked for another reason",
			overrides:     map[string]string{"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-small.json"},
			expectedValue: 0,
		},
		{
			name:          "constraint filtered, more nodes do not help",
			overrides:     map[string]string{"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-constraint.json"},
			expectedValue: 0,
		},
		{
			name:          "allocation too big for any node of the pool, more nodes do not help",
			overrides:     map[string]string{"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-huge.json"},
			expectedValue: 0,
		},
		{
			name:          "initializing node may fit the allocation",
			overrides:     map[string]string{"/v1/nodes": "nodes-init.json", "/v1/evaluations": "evaluations-big.json"},
			expectedValue: 0,
		},
		{
			name:          "one allocation needing a node among others that do not",
			overrides:     map[string]string{"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations.json"},
			expectedValue: 1,
		},
		{
			name:        "no node in the pool",
			query:       "node_fit-deficit_blocked/node_class=unknown",
			overrides:   map[string]string{"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "evaluations-big.json"},
			expectedErr: "no nodes identified within pool",
		},
		{
			name:        "no schedulable node to size the allocation",
			overrides:   map[string]string{"/v1/nodes": "nodes-unschedulable.json", "/v1/evaluations": "evaluations-big.json"},
			expectedErr: "no schedulable nodes found within pool",
		},
		{
			name:        "evaluations cannot be read",
			overrides:   map[string]string{"/v1/nodes": "nodes-fragmented.json", "/v1/evaluations": "missing.json"},
			expectedErr: "failed to list blocked evaluations",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := noderesources.FixtureServer(t, "capacity", tc.overrides)
			client, err := api.NewClient(&api.Config{Address: ts.URL})
			require.NoError(t, err)

			q := tc.query
			if q == "" {
				q = query
			}

			p := &APMPlugin{client: client, logger: hclog.NewNullLogger()}
			now := time.Now()
			got, err := p.Query(q, sdk.TimeRange{From: now.Add(-time.Minute), To: now})

			if tc.expectedErr != "" {
				assert.ErrorContains(t, err, tc.expectedErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.expectedValue, got[0].Value)
		})
	}
}

func TestAPMPlugin_queryBlockedFit_noClient(t *testing.T) {
	p := &APMPlugin{logger: hclog.NewNullLogger()}
	now := time.Now()

	_, err := p.Query("node_fit-deficit_blocked/node_class=app", sdk.TimeRange{From: now.Add(-time.Minute), To: now})
	assert.ErrorContains(t, err, "nomad client not configured")
}
