# `capacity` test fixtures

Hand written JSON bodies of the Nomad API endpoints read by the `noderesources`
helpers, served by `FixtureServer` in `testing.go`. They are not recordings of a
real cluster. All nodes are in node class `app` and have 4000 MHz and 8000 MB
allocatable unless noted.

| Node           | State        | Free (cpu/mem) | Notes |
|----------------|--------------|----------------|-------|
| `n-a`          | eligible     | 2000/4000      | 200/512 reserved. Has a pending allocation (counts), and a completed one and one stopped by the server (do not count). |
| `n-b`          | eligible     | 1100/2100      | A service allocation and a large system job allocation. |
| `n-c`          | eligible     | 1000/2000      | List stub has no resources, so they are read from the node endpoint. The allocation only has the deprecated `Resources`. |
| `n-d`          | eligible     | 2500/5000      | |
| `n-empty`      | eligible     | 4000/8000      | No allocations. |
| `n-ineligible` | ineligible   | -              | Has allocations, loaded only when selected for removal. |
| `n-draining`   | draining     | -              | No allocations file, a request for it returns 404. |
| `n-down`       | down         | -              | No allocations file, a request for it returns 404. |
| `n-init`       | initializing | -              | Only in `nodes-init.json`. |

Node lists: `nodes.json` (a, b, c, empty), `nodes-fragmented.json` (a, b, c),
`nodes-needs-capacity.json` (a, d), `nodes-init.json` (a, b, c, init) and
`nodes-unschedulable.json` (only the ineligible, draining and down ones). Each also
lists the ineligible, draining and down nodes.

In `nodes-fragmented.json` the free capacity totals 4100 MHz and 8100 MB, but no
single node has more than 2000 MHz or 4000 MB.

Evaluations: `evaluations.json` has one of each case (a request of 3000/6000
that is exhausted on the pool, constraint filtered, other node class, system
job, not blocked, too big for any node, small, other node pool, purged job).
The `evaluations-<name>.json` files hold a single one of those, or none.
