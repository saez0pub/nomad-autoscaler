// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package noderesources

import (
	"fmt"

	"github.com/hashicorp/nomad/api"
)

// NodeCapacity describes the schedulable capacity of a single node.
type NodeCapacity struct {
	Node *api.NodeListStub

	// Allocatable is the node resources minus the operator reserved ones.
	Allocatable Resources

	// Used is the sum of all non-terminal allocations on the node, system
	// jobs included.
	Used Resources

	// SystemUsed is the part of Used that belongs to system and sysbatch
	// allocations.
	SystemUsed Resources

	// Allocs are the non-terminal allocations on the node.
	Allocs []*api.Allocation
}

// Free returns what is left on the node for new allocations.
func (n *NodeCapacity) Free() Resources { return n.Allocatable.Sub(n.Used) }

// Fresh returns what a new, otherwise empty, node of the same kind would offer
// once its system job allocations are placed. System jobs run on every node,
// so this is the best a scale-out can provide.
func (n *NodeCapacity) Fresh() Resources { return n.Allocatable.Sub(n.SystemUsed) }

// MovableAllocs returns the non-terminal allocations that would have to be
// placed on other nodes if this node were drained, which excludes system and
// sysbatch allocations.
func (n *NodeCapacity) MovableAllocs() []Resources {
	var out []Resources
	for _, alloc := range n.Allocs {
		if IsSystemAlloc(alloc) {
			continue
		}
		out = append(out, AllocResources(alloc))
	}
	return out
}

// Schedulable returns true if the scheduler can place new allocations on the
// node: it is ready, not draining and eligible. Down, initializing, draining
// and ineligible nodes do not offer capacity.
func Schedulable(node *api.NodeListStub) bool {
	return node.Status == api.NodeStatusReady &&
		!node.Drain &&
		node.SchedulingEligibility == api.NodeSchedulingEligible
}

// LoadCapacity reads the capacity of a node from the Nomad API. The node
// resources are read from the list stub when present, which requires the node
// list to have been requested with resources=true, and from the node info
// endpoint otherwise. The non-terminal allocations always come from the node
// allocations endpoint.
func LoadCapacity(client *api.Client, node *api.NodeListStub) (*NodeCapacity, error) {
	total, reserved := node.NodeResources, node.ReservedResources

	if total == nil {
		info, _, err := client.Nodes().Info(node.ID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to read node %s: %w", node.ID, err)
		}
		if info.NodeResources == nil {
			return nil, fmt.Errorf("node %s does not contain resource info", node.ID)
		}
		total = info.NodeResources
		if reserved == nil {
			reserved = info.ReservedResources
		}
	}

	allocs, _, err := client.Nodes().Allocations(node.ID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read allocations of node %s: %w", node.ID, err)
	}

	out := &NodeCapacity{
		Node:        node,
		Allocatable: Allocatable(total, reserved),
	}
	for _, alloc := range allocs {
		if alloc == nil || !IsNonTerminal(alloc) {
			continue
		}

		res := AllocResources(alloc)
		out.Used = out.Used.Add(res)
		if IsSystemAlloc(alloc) {
			out.SystemUsed = out.SystemUsed.Add(res)
		}
		out.Allocs = append(out.Allocs, alloc)
	}
	return out, nil
}

// HasInitializing returns true if a node of the list is still initializing,
// meaning capacity is about to appear in the cluster.
func HasInitializing(nodes []*api.NodeListStub) bool {
	for _, node := range nodes {
		if node.Status == api.NodeStatusInit {
			return true
		}
	}
	return false
}

// LoadPoolCapacity loads the capacity of the schedulable nodes of a pool.
// Nodes that cannot receive allocations are skipped.
func LoadPoolCapacity(client *api.Client, poolNodes []*api.NodeListStub) ([]*NodeCapacity, error) {
	var out []*NodeCapacity

	for _, node := range poolNodes {
		if !Schedulable(node) {
			continue
		}

		c, err := LoadCapacity(client, node)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
