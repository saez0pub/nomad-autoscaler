# Scaling for blocked allocations that fit no single node

Cluster totals can look healthy while no single node has room for an
allocation: a pool can have 4100 MHz free spread over three nodes and still be
unable to place a 3000 MHz allocation. The Nomad APM `fit-deficit_blocked` query
detects this case, and the `node_scalein_fit_check` target option avoids
undoing the scale-out afterwards.

## The `fit-deficit_blocked` query

For the nodes of the target pool, the query:

1. lists the blocked evaluations and keeps the task groups that are blocked
   because CPU or memory is exhausted on the pool. Groups blocked by a
   constraint, in another node class, node pool or datacenter, and system or
   sysbatch jobs are ignored, as more nodes of this pool would not help them;
2. computes the free CPU and memory of every ready, eligible and not draining
   node: allocatable (total minus reserved) minus the non-terminal allocations,
   system jobs and pending allocations included;
3. returns `1` when such a task group fits no node but would fit a new node of
   the pool, `0` in every other case.

It returns `0` too while a node of the pool is still initializing, so a booting
node is waited for instead of adding another one, and when an allocation would
not fit even a new node (scaling out would only climb to `max`). That case is
logged as a warning.

The value is a number of nodes to add and not a target count, as the APM does
not know the count of the scaling target. Use it with the `threshold` strategy.

```hcl
scaling "cluster_policy" {
  enabled = true
  min     = 1
  max     = 10

  policy {
    cooldown             = "5m"
    cooldown_on_scale_up = "10m"

    # Your existing utilization checks go in the same group as the check below.
    # check "..." {
    #   group = "capacity"
    #   ...
    # }

    check "blocked_allocations_fit" {
      source = "nomad-apm"
      query  = "fit-deficit_blocked"
      group  = "capacity"

      strategy "threshold" {
        lower_bound = 1
        delta       = 1
      }
    }

    target "aws-asg" {
      aws_asg_name           = "clients"
      node_class             = "app"
      node_scalein_fit_check = "true"
    }
  }
}
```

### Grouping

The check votes `none` whenever nothing is blocked. In an ungrouped policy a
`none` vote wins over a `down` vote, so an ungrouped check would stop every
scale-in. A group of its own does not help either, as the `none` of a group
that only votes `none` still wins over the `down` of another group.

Put the check in the same `group` as the utilization checks it complements. In
a group `none` votes are ignored unless every check votes `none`, so a scale-out
vote wins over a scale-in vote, and the utilization checks can still scale in.

### Errors

A failing query, an API error for example, fails the whole policy evaluation,
as it does for the other node queries. The check-level `on_error` setting only
applies to errors of the strategy.

### Limits

- Only CPU and memory are considered. Constraints, affinities, `distinct_hosts`,
  ports, devices, disk and reserved cores are not. A group is also sized as the
  sum of its tasks, whatever their lifecycle.
- One node is requested at a time, the next evaluation after the cooldown asks
  for another one if needed. The number of blocked allocations is not used.
- The blocked evaluations are listed across all namespaces, so the Nomad token
  needs the `list-jobs` and `read-job` capabilities in them.
- The server must support filtering the evaluation list with `filter`.

## The `node_scalein_fit_check` target option

When set to `"true"` on a cluster target (it defaults to `false`), the nodes
chosen by the node selector are only drained if their allocations fit on the
remaining nodes. The allocations of system and sysbatch jobs are not counted, as
they do not move. The allocations that are blocked but could be placed on a
current node must keep fitting as well. Without that, the node added for a
blocked allocation, which is still empty, would be chosen for removal at once.

When the whole selection does not fit, the nodes are removed from its end, so
the first selected nodes, the least busy ones with the default selector, are
kept the longest, until the rest fits. If not even the first node can go, the
scale-in is skipped without starting a cooldown and is evaluated again at the
next interval.

Blocked allocations that fit no current node do not prevent a scale-in, they are
the job of the scale-out check. Set a `cooldown_on_scale_up` longer than the time
a node needs to join the cluster and to receive the blocked allocation.

The check models CPU and memory only. It can refuse a removal that would have
been fine, and in rare cases accept one that leaves an allocation blocked.
