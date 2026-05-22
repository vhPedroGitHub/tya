# Redis-backed Distributed Load Testing — Design Proposal

This document describes a proposed design for coordinating multiple TYA instances through Redis to reach RPS targets that are not achievable from a single machine.

---

## Motivation

The adaptive load engine on a single node is limited by the machine's CPU, network bandwidth, and file-descriptor budget. A mid-range server can typically sustain 10,000–50,000 HTTP calls/s before hitting OS-level bottlenecks. For targets like 500,000 or 1,000,000 req/s you need to spread load across many nodes.

The challenge with multiple independent nodes is coordination: without a shared state, each node ramps to its own `requests_per_second` limit and the aggregate overshoots the desired ceiling — potentially sending 10× the intended load.

Redis provides a lightweight, atomic shared counter that lets every node see and contribute to the global RPS in real time, without requiring any centralised orchestrator process.

---

## Proposed Configuration

Add an optional `redis:` block to `config-run.yml` at the top level:

```yaml
redis:
  addr: "redis:6379"
  password: "${REDIS_PASSWORD}"   # optional; supports env var interpolation
  db: 0                           # optional; default 0
  run_id: "load-test-2025-01"     # unique key prefix for this run
  max_rps: 1000000                # cluster-wide ceiling (HTTP calls/s)
  report_interval: 500ms          # how often each node updates Redis (default: 500ms)
```

When `redis:` is absent TYA behaves exactly as today — no Redis dependency, no behaviour change.

Per-flow `requests_per_second` retains its meaning as the **local node ceiling**: each node will never exceed its own configured rate even if the global aggregate is below `max_rps`. This preserves the existing single-node semantics and allows heterogeneous node configurations in the same cluster.

---

## Key Design

### Redis Keys

| Key | Type | Description |
|-----|------|-------------|
| `tya:<run_id>:max_rps` | string | Global ceiling set once by the first node to register (or pre-seeded by the operator). |
| `tya:<run_id>:current_rps` | string | Live aggregate HTTP calls/s across all active nodes. Updated via `INCRBYFLOAT`. |
| `tya:<run_id>:nodes` | set | Set of `node_id` strings for active nodes (heartbeat-based, TTL 5 s). |
| `tya:<run_id>:node:<node_id>:rps` | string | This node's last reported RPS contribution. Used for delta computation. |

### Node Lifecycle

1. **Register** — on startup each node generates a `node_id` (hostname + PID), adds itself to `tya:<run_id>:nodes`, and sets an initial contribution of `0` in `tya:<run_id>:node:<node_id>:rps`.
2. **Heartbeat** — every `report_interval` the node refreshes its key TTL to signal it is alive.
3. **Deregister** — on clean shutdown the node subtracts its last contribution from `current_rps` and removes itself from the nodes set. If the process dies unexpectedly the TTL expiry handles cleanup (see Fault Tolerance below).

### Per-Iteration Check (hot path)

Inside `spawnIteration`, before launching the goroutine, the node performs an atomic Redis round-trip:

```
current = GET tya:<run_id>:current_rps
if current >= max_rps:
    drop this tick (do not spawn)
    return
```

This check must be fast. Using a pipelined `GET` + local comparison keeps the overhead under 1 ms on a co-located Redis instance.

### RPS Delta Reporting

Every `report_interval` each node:

1. Samples its own measured RPS over the last window (`myCurrentRPS`).
2. Reads its last reported value from `tya:<run_id>:node:<node_id>:rps` (kept locally in memory, no extra round-trip).
3. Computes `delta = myCurrentRPS - myLastReportedRPS`.
4. Executes atomically:
   ```
   INCRBYFLOAT tya:<run_id>:current_rps  <delta>
   SET         tya:<run_id>:node:<node_id>:rps  <myCurrentRPS>  EX 5
   ```
5. Updates `myLastReportedRPS = myCurrentRPS`.

Using signed `INCRBYFLOAT` means nodes can both add and subtract, keeping the aggregate accurate as nodes ramp up, ramp down, or exit.

---

## Flow of a 3-node Cluster Targeting 300 req/s Global (100 req/s each)

```
t=0s   Node A: local rps=0,  delta=+0  → Redis current_rps=0
t=2s   Node A: local rps=20, delta=+20 → Redis current_rps=20
       Node B: local rps=20, delta=+20 → Redis current_rps=40
       Node C: local rps=20, delta=+20 → Redis current_rps=60

t=4s   Node A: local rps=50, delta=+30 → Redis current_rps=90
       Node B: local rps=50, delta=+30 → Redis current_rps=120
       Node C: local rps=50, delta=+30 → Redis current_rps=150

t=8s   Node A: local rps=100, delta=+50 → Redis current_rps=250
       Node B: local rps=100, delta=+50 → Redis current_rps=300  ← ceiling reached
       Node C checks: current_rps(300) >= max_rps(300) → holds, does not grow

t=10s  Node A exits (crash): TTL expires → current_rps corrected via next heartbeat
       Remaining nodes: detect drop → allowed to grow again
```

---

## Fault Tolerance

### Node crash / network partition

Each `tya:<run_id>:node:<node_id>:rps` key has a TTL of `2 × report_interval` (default 1 s). If a node stops updating (crash, partition), its key expires. A background goroutine on every surviving node periodically reconciles:

```
expected_aggregate = SUM of all tya:<run_id>:node:*:rps values still alive
actual_aggregate   = GET tya:<run_id>:current_rps
drift              = actual_aggregate - expected_aggregate
if abs(drift) > threshold:
    INCRBYFLOAT tya:<run_id>:current_rps  -drift
```

This self-healing loop runs every `report_interval × 4` to avoid thundering-herd corrections.

### Redis unavailability

If Redis becomes unreachable, each node falls back to **autonomous mode**: it ignores the global counter and runs at its own local `requests_per_second` ceiling. A warning is logged every 5 s. When Redis reconnects the node re-registers and resumes coordinated mode. This ensures a Redis outage degrades gracefully (nodes run independently) rather than halting the load test.

---

## Report Changes

The JSON report would gain a top-level `distributed` block when Redis is used:

```json
"distributed": {
  "redis_addr": "redis:6379",
  "run_id": "load-test-2025-01",
  "node_id": "worker-3-pid-1842",
  "max_rps_global": 1000000,
  "peak_rps_global": 987432,
  "nodes_observed": 12,
  "autonomous_fallback_periods": 0
}
```

Per-flow `rps_achieved` continues to reflect **this node's** HTTP calls/s. The `distributed.peak_rps_global` field is the highest aggregate value seen in Redis during the analysis window — it represents the cluster-wide throughput.

---

## Implementation Scope

The feature would be implemented as a new package `pkg/redis_coordinator/` with:

| File | Responsibility |
|------|---------------|
| `coordinator.go` | `Coordinator` struct: connect, register, heartbeat loop, reconcile loop, deregister |
| `counter.go` | `CheckAndReport(myRPS float64) (globalRPS float64, err error)` — single call used by the hot path |
| `options.go` | `RedisConfig` struct mirroring the YAML schema |

The coordinator is injected into `pkg/commands/run.go` as an optional interface. When `redis:` is absent a no-op implementation is used, keeping the existing code path unchanged.

```go
type Coordinator interface {
    // ShouldDrop returns true if the global RPS ceiling has been reached
    // and this iteration should be skipped.
    ShouldDrop() bool
    // ReportRPS updates this node's contribution in Redis.
    ReportRPS(currentRPS float64)
    // Close deregisters this node cleanly.
    Close() error
}
```

Recommended client library: `github.com/redis/go-redis/v9` (already widely used in the Go ecosystem, supports pipelining and `INCRBYFLOAT`).

---

## Open Questions

1. **Clock skew between nodes** — nodes sample their own RPS over a local window. If clocks drift significantly the aggregate may lag. A simple fix is to use Redis `TIME` for window alignment, but this adds latency.
2. **Hot path overhead** — the per-iteration `GET` adds a network round-trip on every tick. For very high RPS targets (>50,000 on a single node) this could become a bottleneck. Mitigation: batch the check every N iterations (e.g. every 10 ticks) or use a local cache refreshed at `report_interval`.
3. **Global vs per-flow ceiling** — the current proposal applies `max_rps` globally across all flows. A per-flow global ceiling (e.g. `max_rps` inside each `flow:` block) would be more precise but requires one Redis key set per flow per run.
4. **Analysis window synchronisation** — ideally all nodes enter the analysis window at the same time so the global `peak_rps_global` is meaningful. A Redis barrier (a key that all nodes wait on before starting analysis) could enforce this, at the cost of added complexity.
