// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package plugin

import (
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/nomad-autoscaler/sdk"
	"github.com/hashicorp/nomad-autoscaler/sdk/helper/scaleutils/noderesources"
	"github.com/hashicorp/nomad/api"
)

// queryBlockedFit answers a fit deficit query. The result is 1 when an
// allocation waits in a blocked evaluation because of exhausted CPU or memory
// and no single node of the pool can host it, but a new node would. In every
// other case it is 0. It is meant to be consumed with the threshold strategy
// (lower_bound = 1, delta = 1), so the scale-out is relative to the current
// count of the target.
//
// Cluster totals can look fine while no node has room for an allocation,
// which is why the capacity is computed per node.
func (a *APMPlugin) queryBlockedFit(q string, query *nodePoolQuery) (sdk.TimestampedMetrics, error) {
	client, _ := a.getClientAndConfigSnapshot()
	if client == nil {
		return nil, errors.New("nomad client not configured")
	}

	nodes, _, err := client.Nodes().List(&api.QueryOptions{Params: map[string]string{"resources": "true"}})
	if err != nil {
		return nil, fmt.Errorf("failed to list Nomad nodes: %v", err)
	}

	var poolNodes []*api.NodeListStub
	for _, node := range nodes {
		if query.poolIDs.IsPoolMember(node) {
			poolNodes = append(poolNodes, node)
		}
	}
	if len(poolNodes) == 0 {
		return nil, errors.New("no nodes identified within pool")
	}

	deficit, err := a.blockedFitDeficit(client, q, poolNodes)
	if err != nil {
		return nil, err
	}

	return sdk.TimestampedMetrics{{Timestamp: time.Now(), Value: float64(deficit)}}, nil
}

// blockedFitDeficit returns the number of nodes to add, 0 or 1.
func (a *APMPlugin) blockedFitDeficit(client *api.Client, q string, poolNodes []*api.NodeListStub) (int, error) {
	asks, err := noderesources.BlockedAsks(client, poolNodes, a.logger)
	if err != nil {
		return 0, err
	}

	// The node capacity needs a call per node, so only get it when needed.
	if len(asks) == 0 {
		a.logger.Debug("no blocked allocation limited by pool resources", "query", q)
		return 0, nil
	}

	// A node that is still coming up may fit the allocation. Waiting avoids
	// adding a node every evaluation while it boots.
	if noderesources.HasInitializing(poolNodes) {
		a.logger.Debug("pool has an initializing node, not requesting more capacity", "query", q)
		return 0, nil
	}

	capacities, err := noderesources.LoadPoolCapacity(client, poolNodes)
	if err != nil {
		return 0, err
	}
	if len(capacities) == 0 {
		return 0, errors.New("no schedulable nodes found within pool to size blocked allocations")
	}

	c := noderesources.Classify(asks, capacities)

	for _, ask := range c.Unsatisfiable {
		a.logger.Warn("blocked allocation does not fit a new node of the pool, scaling out would not help",
			"query", q, "eval_id", ask.EvalID, "allocation", ask.String(),
			"cpu", ask.Resources.CPU, "memory_mb", ask.Resources.MemoryMB)
	}
	a.logger.Debug("classified blocked allocations", "query", q,
		"placeable", len(c.Placeable), "needs_capacity", len(c.NeedsCapacity),
		"unsatisfiable", len(c.Unsatisfiable))

	if len(c.NeedsCapacity) > 0 {
		return 1, nil
	}
	return 0, nil
}
