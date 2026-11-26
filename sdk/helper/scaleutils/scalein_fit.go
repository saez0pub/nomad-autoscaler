// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package scaleutils

import (
	"fmt"
	"strconv"

	"github.com/hashicorp/nomad-autoscaler/sdk"
	"github.com/hashicorp/nomad-autoscaler/sdk/helper/scaleutils/nodepool"
	"github.com/hashicorp/nomad-autoscaler/sdk/helper/scaleutils/noderesources"
	"github.com/hashicorp/nomad/api"
)

// limitScaleInByFit is the optional safety check run before nodes are drained.
// It reduces the selected nodes, keeping those first in the list, until the
// allocations that would have to move off them fit on the nodes that remain.
// The allocations waiting in blocked evaluations that the current nodes can
// host are also required to keep fitting. Without that, a node added to place
// a blocked allocation would be drained again as soon as it is added, since it
// is still empty, and the cycle would repeat.
//
// When not even the first selected node can be removed safely the returned
// error is a sdk.TargetScalingNoOpError, so no cooldown is started.
//
// Only CPU and memory are modelled. Placement constraints are not, so the
// check can refuse a removal that is fine, and in rare cases accept one that
// leaves an allocation blocked.
func (c *ClusterScaleUtils) limitScaleInByFit(cfg map[string]string, selected []*api.NodeListStub) ([]*api.NodeListStub, error) {
	enabled, err := scaleInFitCheckEnabled(cfg)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return selected, nil
	}

	ids, err := nodepool.NewClusterNodePoolIdentifierList(cfg)
	if err != nil {
		return nil, err
	}

	nodes, _, err := c.client.Nodes().List(&api.QueryOptions{Params: map[string]string{"resources": "true"}})
	if err != nil {
		return nil, fmt.Errorf("failed to list Nomad nodes for the scale-in fit check: %w", err)
	}
	var poolNodes []*api.NodeListStub
	for _, n := range nodes {
		if ids.IsPoolMember(n) {
			poolNodes = append(poolNodes, n)
		}
	}

	capacities, err := noderesources.LoadPoolCapacity(c.client, poolNodes)
	if err != nil {
		return nil, fmt.Errorf("failed to load node capacity for the scale-in fit check: %w", err)
	}
	byID := make(map[string]*noderesources.NodeCapacity, len(capacities))
	for _, nc := range capacities {
		byID[nc.Node.ID] = nc
	}

	// Selected nodes that cannot take allocations, such as ineligible ones,
	// are not part of the capacity but their allocations still have to move.
	selectedCap := make([]*noderesources.NodeCapacity, len(selected))
	for i, n := range selected {
		if nc, ok := byID[n.ID]; ok {
			selectedCap[i] = nc
			continue
		}
		if selectedCap[i], err = noderesources.LoadCapacity(c.client, n); err != nil {
			return nil, fmt.Errorf("failed to load node capacity for the scale-in fit check: %w", err)
		}
	}

	asks, err := noderesources.BlockedAsks(c.client, poolNodes, c.log)
	if err != nil {
		return nil, fmt.Errorf("failed to read blocked allocations for the scale-in fit check: %w", err)
	}
	keep := keepPlaced(noderesources.Classify(asks, capacities).Placeable, capacities)

	for n := len(selected); n >= 1; n-- {
		removed := make(map[string]struct{}, n)
		items := append([]noderesources.Resources(nil), keep...)
		for _, nc := range selectedCap[:n] {
			removed[nc.Node.ID] = struct{}{}
			items = append(items, nc.MovableAllocs()...)
		}

		var free []noderesources.Resources
		for _, nc := range capacities {
			if _, ok := removed[nc.Node.ID]; !ok {
				free = append(free, nc.Free())
			}
		}

		if !noderesources.Pack(items, free) {
			continue
		}

		if n < len(selected) {
			c.log.Info("scale-in fit check reduced the number of nodes to remove",
				"requested", len(selected), "allowed", n)
		}
		return selected[:n], nil
	}

	c.log.Info("scale-in fit check found no node that can be removed without leaving allocations without room",
		"candidates", len(selected), "remaining_nodes", len(capacities), "blocked_allocations", len(keep))
	return nil, sdk.NewTargetScalingNoOpError(
		"allocations of the selected nodes would not fit on the remaining nodes")
}

// keepPlaced returns the resources of the asks that can all be placed on the
// current nodes at once. Asks that only fit one at a time are not the
// responsibility of a node removal, so they are not required to fit later.
func keepPlaced(asks []noderesources.Ask, capacities []*noderesources.NodeCapacity) []noderesources.Resources {
	free := make([]noderesources.Resources, len(capacities))
	for i, nc := range capacities {
		free[i] = nc.Free()
	}

	var out []noderesources.Resources
	for _, ask := range asks {
		if noderesources.Pack(append(out, ask.Resources), free) {
			out = append(out, ask.Resources)
		}
	}
	return out
}

// scaleInFitCheckEnabled reads the opt-in config option of the fit check.
func scaleInFitCheckEnabled(cfg map[string]string) (bool, error) {
	str, ok := cfg[sdk.TargetConfigKeyScaleInFitCheck]
	if !ok {
		return false, nil
	}

	enabled, err := strconv.ParseBool(str)
	if err != nil {
		return false, fmt.Errorf("failed to parse %q value %q: %w", sdk.TargetConfigKeyScaleInFitCheck, str, err)
	}
	return enabled, nil
}
