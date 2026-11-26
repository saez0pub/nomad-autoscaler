// Copyright IBM Corp. 2020, 2026
// SPDX-License-Identifier: MPL-2.0

package noderesources

import (
	"fmt"
	"sort"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/api"
)

const (
	// blockedEvalFilter selects the blocked evaluations, those which the
	// scheduler could not place completely and which wait for capacity.
	blockedEvalFilter = `Status == "` + api.EvalStatusBlocked + `"`

	// allNamespaces makes the Nomad API return results of every namespace.
	allNamespaces = "*"

	dimensionCPU    = "cpu"
	dimensionMemory = "memory"
)

// Ask is the resource request of a task group that the scheduler failed to
// place and that is waiting in a blocked evaluation. A blocked evaluation does
// not carry the request, so it is rebuilt from the job specification.
type Ask struct {
	EvalID    string
	Namespace string
	JobID     string
	TaskGroup string

	// Resources is the request of one allocation of the group, the sum of its
	// tasks. Reserved cores, devices, disk and ports are not included.
	Resources Resources
}

// String returns a short identifier of the ask for logging.
func (a Ask) String() string {
	return fmt.Sprintf("%s/%s.%s", a.Namespace, a.JobID, a.TaskGroup)
}

// BlockedAsks returns the requests of the task groups waiting in blocked
// evaluations because a resource, CPU or memory, is exhausted on the nodes of
// the pool. Asks blocked for another reason are left out, as adding nodes of
// this pool does not help them: constraints that filter every node of the
// pool, other node pools or datacenters, and system and sysbatch jobs.
//
// A job that cannot be read, because it has been purged for example, is
// skipped with a warning.
func BlockedAsks(client *api.Client, poolNodes []*api.NodeListStub, log hclog.Logger) ([]Ask, error) {
	evals, _, err := client.Evaluations().List(&api.QueryOptions{
		Namespace: allNamespaces,
		Filter:    blockedEvalFilter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list blocked evaluations: %w", err)
	}

	pool := newPoolAttributes(poolNodes)
	jobs := make(map[string]*api.Job)

	var out []Ask
	for _, eval := range evals {
		// The filter is applied by the server, this protects against
		// misbehaving servers or proxies.
		if eval == nil || eval.Status != api.EvalStatusBlocked {
			continue
		}

		groups := make([]string, 0, len(eval.FailedTGAllocs))
		for name := range eval.FailedTGAllocs {
			groups = append(groups, name)
		}
		sort.Strings(groups)

		for _, name := range groups {
			metric := eval.FailedTGAllocs[name]
			if !pool.exhaustedBy(metric) {
				log.Debug("ignoring blocked allocation not limited by pool resources",
					"eval_id", eval.ID, "job_id", eval.JobID, "group", name)
				continue
			}

			job, err := cachedJob(client, jobs, eval)
			if err != nil {
				log.Warn("failed to read job of blocked evaluation, ignoring it",
					"eval_id", eval.ID, "job_id", eval.JobID, "error", err)
				break
			}
			if job == nil {
				break
			}

			res, ok := groupResources(job, name)
			if !ok {
				continue
			}
			out = append(out, Ask{
				EvalID:    eval.ID,
				Namespace: eval.Namespace,
				JobID:     eval.JobID,
				TaskGroup: name,
				Resources: res,
			})
		}
	}
	return out, nil
}

// cachedJob reads the job of an evaluation once per evaluation namespace and
// job ID. It returns nil for jobs whose blocked allocations never need new
// nodes: system and sysbatch jobs, which run on every node, and stopped jobs.
func cachedJob(client *api.Client, cache map[string]*api.Job, eval *api.Evaluation) (*api.Job, error) {
	key := eval.Namespace + "/" + eval.JobID

	job, ok := cache[key]
	if !ok {
		var err error
		job, _, err = client.Jobs().Info(eval.JobID, &api.QueryOptions{Namespace: eval.Namespace})
		if err != nil {
			return nil, err
		}
		cache[key] = job
	}

	if job == nil || (job.Stop != nil && *job.Stop) {
		return nil, nil
	}
	if job.Type != nil && (*job.Type == api.JobTypeSystem || *job.Type == api.JobTypeSysbatch) {
		return nil, nil
	}
	return job, nil
}

// groupResources sums the resources of the tasks of a job task group.
func groupResources(job *api.Job, group string) (Resources, bool) {
	for _, tg := range job.TaskGroups {
		if tg == nil || tg.Name == nil || *tg.Name != group {
			continue
		}

		var out Resources
		for _, task := range tg.Tasks {
			if task == nil || task.Resources == nil {
				continue
			}
			if task.Resources.CPU != nil {
				out.CPU += int64(*task.Resources.CPU)
			}
			if task.Resources.MemoryMB != nil {
				out.MemoryMB += int64(*task.Resources.MemoryMB)
			}
		}
		return out, true
	}
	return Resources{}, false
}

// poolAttributes are the node attributes the scheduler reports its failures
// by, used to tell whether a failure concerns the nodes of a pool.
type poolAttributes struct {
	classes     map[string]struct{}
	hasNoClass  bool
	nodePools   map[string]struct{}
	datacenters map[string]struct{}
}

func newPoolAttributes(nodes []*api.NodeListStub) *poolAttributes {
	p := &poolAttributes{
		classes:     make(map[string]struct{}),
		nodePools:   make(map[string]struct{}),
		datacenters: make(map[string]struct{}),
	}
	for _, n := range nodes {
		if n.NodeClass == "" {
			p.hasNoClass = true
		} else {
			p.classes[n.NodeClass] = struct{}{}
		}
		if n.NodePool != "" {
			p.nodePools[n.NodePool] = struct{}{}
		}
		if n.Datacenter != "" {
			p.datacenters[n.Datacenter] = struct{}{}
		}
	}
	return p
}

// exhaustedBy returns true when the scheduler failure reported by the metric
// was caused by CPU or memory running out on nodes of the pool.
func (p *poolAttributes) exhaustedBy(m *api.AllocationMetric) bool {
	if m == nil {
		return false
	}

	// Filtered nodes are a constraint problem, which more nodes of this pool
	// do not solve. Only exhausted CPU or memory does.
	if m.DimensionExhausted[dimensionCPU] == 0 && m.DimensionExhausted[dimensionMemory] == 0 {
		return false
	}

	// The scheduler only looks at the nodes of the job node pool and
	// datacenters. When they are not those of this pool, the failure is about
	// other nodes. The checks are skipped when the pool attributes are unknown,
	// as on Nomad versions without node pools.
	if m.NodePool != "" && len(p.nodePools) > 0 {
		if _, ok := p.nodePools[m.NodePool]; !ok {
			return false
		}
	}
	if len(m.NodesAvailable) > 0 && len(p.datacenters) > 0 {
		found := false
		for dc := range m.NodesAvailable {
			if _, ok := p.datacenters[dc]; ok {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// ClassExhausted is keyed by node class and has no entry for nodes
	// without a class, so those cannot be excluded.
	if p.hasNoClass {
		return true
	}
	for class := range p.classes {
		if m.ClassExhausted[class] > 0 {
			return true
		}
	}
	return false
}

// Classification splits blocked asks by what adding nodes would do for them.
type Classification struct {
	// Placeable asks fit on the free capacity of an existing node. They are
	// blocked for another reason, or about to be placed.
	Placeable []Ask

	// NeedsCapacity asks fit on no existing node but would fit on a new node
	// of the pool, so scaling out helps.
	NeedsCapacity []Ask

	// Unsatisfiable asks would not fit even on a new node of the pool, once
	// its system jobs are placed. Scaling out never helps them.
	Unsatisfiable []Ask
}

// Classify sorts asks by how they relate to the capacity of the nodes. A new
// node is assumed to have the capacity of the largest existing one.
func Classify(asks []Ask, nodes []*NodeCapacity) Classification {
	var out Classification

	for _, ask := range asks {
		switch {
		case anyFits(nodes, func(n *NodeCapacity) Resources { return n.Free() }, ask.Resources):
			out.Placeable = append(out.Placeable, ask)
		case anyFits(nodes, func(n *NodeCapacity) Resources { return n.Fresh() }, ask.Resources):
			out.NeedsCapacity = append(out.NeedsCapacity, ask)
		default:
			out.Unsatisfiable = append(out.Unsatisfiable, ask)
		}
	}
	return out
}

func anyFits(nodes []*NodeCapacity, capacity func(*NodeCapacity) Resources, ask Resources) bool {
	for _, n := range nodes {
		if capacity(n).Fits(ask) {
			return true
		}
	}
	return false
}
