# Distributed Load Testing — Design Proposal

This document describes a proposed design for coordinating multiple TYA worker nodes through a dedicated TYA coordinator node to reach RPS targets not achievable from a single machine.

---

## Motivation

A single machine can typically sustain 10,000–50,000 HTTP calls/s before hitting CPU, network, or file-descriptor limits. For targets like 500,000 or 1,000,000 req/s you need to spread load across many machines.

The challenge with independent nodes is that without coordination each one ramps to its own local ceiling and the aggregate overshoots the desired global target — potentially sending far more load than intended and destabilising the system under test.

The coordinator model solves this with a single authoritative process that aggregates reported RPS from all workers and tells each one whether to keep growing or hold its current rate. No shared mutable state, no external dependencies.

---

## Architecture

```
┌──────────────────────────────────────────────────────┐
│                   Coordinator Node                   │
│                                                      │
│  - Holds global max_rps ceiling                      │
│  - Maintains per-worker state table                  │
│  - Computes cluster aggregate RPS                    │
│  - Replies grow / hold to each worker                │
└────────────┬─────────────────────────────────────────┘
             │  gRPC (or HTTP/2)
    ┌────────┴────────┐
    │                 │
┌───▼───┐         ┌───▼───┐         ┌───────┐
│Worker │         │Worker │   ...   │Worker │
│  A    │         │  B    │         │  N    │
│rps=50k│         │rps=50k│         │rps=50k│
└───────┘         └───────┘         └───────┘
```

Workers never talk to each other. All coordination flows through the coordinator.

---

## Proposed Configuration

### Coordinator (`config-coordinator.yml`)

```yaml
coordinator:
  listen: ":7777"          # address workers connect to
  max_rps: 1000000         # global RPS ceiling for this run
  report_interval: 500ms   # how often workers are expected to report
  worker_timeout: 3s       # mark a worker dead if no report received within this window
```

### Worker (`config-run.yml`)

```yaml
coordinator:
  addr: "coordinator-host:7777"   # omit to run standalone (no coordination)
  report_interval: 500ms          # how often this worker reports its RPS
  fallback: standalone            # behaviour if coordinator unreachable: standalone | hold | stop

flows:
  - name: my-flow
    requests_per_second: 50000    # local ceiling — the worker never exceeds this regardless of coordinator
    ...
```

When `coordinator.addr` is absent TYA behaves exactly as today — no coordinator dependency, no behaviour change.

---

## Protocol

### Worker → Coordinator (every `report_interval`)

```json
{
  "worker_id":    "worker-a-pid-1234",
  "run_id":       "load-test-2025-01",
  "current_rps":  42000,
  "target_rps":   50000
}
```

- `current_rps`: measured HTTP calls/s over the last window.
- `target_rps`: the worker's local `requests_per_second` ceiling (so the coordinator knows the worker's maximum contribution).

### Coordinator → Worker (reply)

```json
{
  "instruction": "grow",
  "aggregate_rps": 840000,
  "max_rps": 1000000
}
```

| Instruction | Meaning |
|-------------|---------|
| `grow` | Aggregate is below ceiling. Continue ramping up normally. |
| `hold` | Aggregate has reached ceiling. Freeze at current RPS; do not increase. |

The worker never needs to know about other workers. It only acts on the instruction it receives.

---

## Coordinator Decision Logic

On every incoming report the coordinator:

1. Updates the worker's entry in its state table: `{worker_id → {current_rps, target_rps, last_seen}}`.
2. Recomputes `aggregate_rps = SUM(current_rps for all workers seen within worker_timeout)`.
3. If `aggregate_rps >= max_rps` → reply `hold` to this worker.
4. If `aggregate_rps < max_rps` → reply `grow` to this worker.

This is intentionally simple. The coordinator does not try to distribute headroom evenly across workers or predict future growth — it just reflects the current state of the aggregate and lets each worker's local ramp-up engine handle the rest.

### Worker Table Expiry

If a worker stops reporting (crash, network partition) its entry is removed from the aggregate after `worker_timeout`. This prevents a dead worker from artificially depressing the aggregate and blocking live workers from growing.

---

## Worker Behaviour

### During ramp-up

At the end of each ramp-up window the worker reports its measured RPS and waits for the coordinator's reply before deciding whether to fire the next ramp tick:

```
ramp window completes
  → report {current_rps, target_rps} to coordinator
  → receive instruction
  → if "grow":  proceed with normal ramp step (factor × current)
  → if "hold":  skip ramp step, keep current ticker interval
```

The existing 4-phase engine (ramp-up → plateau detection → analysis → drain) runs unchanged inside the worker. The coordinator only influences whether the ramp-up phase is allowed to grow — it does not affect the analysis window or drain phases.

### Fallback on coordinator unreachability

Controlled by `fallback` in worker config:

| Value | Behaviour |
|-------|-----------|
| `standalone` | Worker ignores coordinator and ramps to its own `requests_per_second` ceiling. Logs a warning every 5 s. |
| `hold` | Worker freezes at its last known RPS until coordinator reconnects. |
| `stop` | Worker aborts the run with an error. |

---

## Flow of a 4-worker Cluster Targeting 200 req/s Global (50 req/s each)

```
t=0s   All workers start, aggregate=0
       Coordinator: aggregate(0) < max(200) → grow to all

t=4s   Each worker at ~20 rps, aggregate=80
       Coordinator: 80 < 200 → grow to all

t=8s   Each worker at ~40 rps, aggregate=160
       Coordinator: 160 < 200 → grow to all

t=10s  Worker A reports 50 rps, others ~45, aggregate=185
       Coordinator: 185 < 200 → grow to all

t=12s  Worker A reports 50 rps, B=50, C=50, D=50, aggregate=200
       Coordinator: 200 >= 200 → hold to all

t=14s  Worker B crashes. Coordinator removes B after worker_timeout.
       aggregate drops to 150. Coordinator: 150 < 200 → grow to A, C, D.
       Remaining workers resume ramping to fill the gap.
```

---

## Report Changes

The JSON report gains a top-level `distributed` block when coordinator mode is active:

```json
"distributed": {
  "mode": "worker",
  "coordinator_addr": "coordinator-host:7777",
  "worker_id": "worker-a-pid-1234",
  "run_id": "load-test-2025-01",
  "hold_periods": 3,
  "fallback_periods": 0
}
```

For the coordinator node, a separate report is written:

```json
"distributed": {
  "mode": "coordinator",
  "max_rps": 1000000,
  "peak_aggregate_rps": 987432,
  "workers_registered": 20,
  "workers_timed_out": 1
}
```

Per-flow `rps_achieved` continues to reflect **this worker's** HTTP calls/s. The coordinator report's `peak_aggregate_rps` is the cluster-wide peak.

---

## Implementation Scope

| Package / File | Responsibility |
|----------------|---------------|
| `pkg/coordinator/server.go` | gRPC server: accepts reports, maintains worker table, replies with instruction |
| `pkg/coordinator/client.go` | gRPC client embedded in worker: sends report, receives instruction |
| `pkg/coordinator/options.go` | `CoordinatorConfig` and `WorkerCoordinatorConfig` structs |
| `pkg/commands/coordinate.go` | New `tya coordinate` command that starts the coordinator node |
| `pkg/commands/run.go` | Extended to instantiate coordinator client when `coordinator.addr` is set; pass instruction into ramp loop |

The coordinator client is injected into the ramp loop as an optional interface. When `coordinator.addr` is absent a no-op implementation is used, keeping the existing single-node code path unchanged:

```go
type CoordinatorClient interface {
    // Report sends current and target RPS to the coordinator and returns
    // the instruction for this ramp window.
    Report(ctx context.Context, currentRPS, targetRPS float64) (Instruction, error)
    Close() error
}

type Instruction int

const (
    InstructionGrow Instruction = iota
    InstructionHold
)
```

Recommended transport: gRPC with a simple Protobuf message — low overhead, bidirectional streaming available if polling proves too slow.

---

## Open Questions

1. **Push vs pull** — the current proposal is worker-push (workers report on a timer). An alternative is coordinator-pull (coordinator polls workers). Push is simpler and keeps latency low.
2. **Per-flow vs per-node coordination** — the proposal coordinates at the node level (one report per worker per interval). Per-flow coordination (one report per flow per worker) would allow finer-grained control but multiplies the message rate.
3. **Coordinator HA** — a single coordinator is a point of failure. For production use a standby coordinator with leader election (e.g. via a lock file or etcd) would be needed. Out of scope for v1.
4. **Analysis window synchronisation** — ideally all workers enter the analysis window simultaneously so `peak_aggregate_rps` is meaningful. A barrier signal from the coordinator (broadcast "start analysis now") could enforce this.
