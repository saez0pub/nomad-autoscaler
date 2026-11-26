// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

// Package noderesources computes the free capacity of Nomad client nodes from
// the Nomad API data and answers whether a resource request fits on a single
// node. The scheduler works with per node capacity, so cluster totals are not
// enough to know whether an allocation can be placed.
//
// Only CPU and memory are modelled. Constraints, affinities, distinct_hosts,
// ports, devices and disk are not, so a positive "fits" answer is a necessary
// condition for placement, not a guarantee.
package noderesources

import (
	"sort"

	"github.com/hashicorp/nomad/api"
)

// Resources is an amount of schedulable resources, CPU in MHz shares and
// memory in MB.
type Resources struct {
	CPU      int64
	MemoryMB int64
}

// Add returns the sum of r and o.
func (r Resources) Add(o Resources) Resources {
	return Resources{CPU: r.CPU + o.CPU, MemoryMB: r.MemoryMB + o.MemoryMB}
}

// Sub returns r minus o. The result can be negative when o is larger than r,
// which happens on overcommitted nodes.
func (r Resources) Sub(o Resources) Resources {
	return Resources{CPU: r.CPU - o.CPU, MemoryMB: r.MemoryMB - o.MemoryMB}
}

// Fits returns true if ask can be satisfied by r.
func (r Resources) Fits(ask Resources) bool {
	return ask.CPU <= r.CPU && ask.MemoryMB <= r.MemoryMB
}

// Allocatable returns the resources the scheduler can hand out on a node,
// which is the total node resources minus the operator reserved resources. A
// nil reserved object is treated as nothing reserved and nil node resources
// as an empty node.
func Allocatable(total *api.NodeResources, reserved *api.NodeReservedResources) Resources {
	if total == nil {
		return Resources{}
	}

	out := Resources{CPU: total.Cpu.CpuShares, MemoryMB: total.Memory.MemoryMB}
	if reserved != nil {
		out.CPU -= int64(reserved.Cpu.CpuShares)
		out.MemoryMB -= int64(reserved.Memory.MemoryMB)
	}
	return out
}

// AllocResources returns the CPU shares and memory MB held by an allocation.
// It reads AllocatedResources, and falls back to the deprecated Resources
// field only when AllocatedResources is absent, as newer Nomad versions only
// populate the former.
func AllocResources(alloc *api.Allocation) Resources {
	var out Resources

	if alloc.AllocatedResources != nil {
		for _, task := range alloc.AllocatedResources.Tasks {
			if task == nil {
				continue
			}
			out.CPU += task.Cpu.CpuShares
			out.MemoryMB += task.Memory.MemoryMB
		}
		return out
	}

	if alloc.Resources != nil {
		if alloc.Resources.CPU != nil {
			out.CPU = int64(*alloc.Resources.CPU)
		}
		if alloc.Resources.MemoryMB != nil {
			out.MemoryMB = int64(*alloc.Resources.MemoryMB)
		}
	}
	return out
}

// IsNonTerminal returns true when the allocation still holds resources on its
// node: neither the server wants it stopped nor the client reports it ended.
// This mirrors how the scheduler computes the proposed allocations of a node.
// Pending allocations are included as they are already placed.
func IsNonTerminal(alloc *api.Allocation) bool {
	return !alloc.ServerTerminalStatus() && !alloc.ClientTerminalStatus()
}

// IsSystemAlloc returns true for allocations of system and sysbatch jobs. They
// are tied to their node, so they are not rescheduled elsewhere on a drain.
func IsSystemAlloc(alloc *api.Allocation) bool {
	if alloc.Job == nil || alloc.Job.Type == nil {
		return false
	}
	return *alloc.Job.Type == api.JobTypeSystem || *alloc.Job.Type == api.JobTypeSysbatch
}

// Pack reports whether all asks can be placed at the same time on the nodes
// described by free, where each ask must fit entirely on one node. It uses
// first-fit decreasing, ordering asks by their dominant share of the total
// free capacity. Neither slice is modified.
//
// Like any bin packing heuristic it can answer false when a better packing
// exists. The Nomad scheduler is a heuristic too, so this errs on the side of
// not removing capacity.
func Pack(asks []Resources, free []Resources) bool {
	if len(asks) == 0 {
		return true
	}

	var total Resources
	for _, f := range free {
		if f.CPU > 0 {
			total.CPU += f.CPU
		}
		if f.MemoryMB > 0 {
			total.MemoryMB += f.MemoryMB
		}
	}

	share := func(r Resources) float64 {
		var s float64
		if total.CPU > 0 {
			s = float64(r.CPU) / float64(total.CPU)
		}
		if total.MemoryMB > 0 {
			if m := float64(r.MemoryMB) / float64(total.MemoryMB); m > s {
				s = m
			}
		}
		return s
	}

	ordered := make([]Resources, len(asks))
	copy(ordered, asks)
	sort.SliceStable(ordered, func(i, j int) bool { return share(ordered[i]) > share(ordered[j]) })

	remaining := make([]Resources, len(free))
	copy(remaining, free)

	for _, ask := range ordered {
		placed := false
		for i := range remaining {
			if remaining[i].Fits(ask) {
				remaining[i] = remaining[i].Sub(ask)
				placed = true
				break
			}
		}
		if !placed {
			return false
		}
	}
	return true
}
