# TYA Metrics & Report Format

At the end of every `tya run` (including test mode), TYA writes a JSON report to the current directory:

```
tya-report-<unix-timestamp>.json
```

## Top-Level Structure

```json
{
  "generated_at": "2025-01-01T10:00:00Z",
  "total_duration_s": 62.4,
  "flows": {
    "smoke-users": { ... },
    "person-lifecycle": { ... }
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `generated_at` | string (ISO 8601) | Timestamp when the report was written |
| `total_duration_s` | float | Wall-clock seconds from run start to completion |
| `flows` | object | Map of flow name → `FlowReport` |

---

## FlowReport

Each entry in `flows` is a `FlowReport` object:

```json
{
  "name": "person-lifecycle",
  "type": "end-to-end",
  "total_requests": 750,
  "successful_requests": 743,
  "failed_requests": 7,
  "rps_achieved": 24.8,
  "stable_rps_target": 25.0,
  "stable_rps_achieved": 24.8,
  "stable_rps_max_reached": false,
  "ramp_up_duration_s": 14.2,
  "analysis_duration_s": 60.0,
  "avg_concurrency": 4.1,
  "max_concurrency": 7,
  "think_time_applied_ms": 38.5,
  "latency_ms": { ... },
  "steps": [ ... ],
  "children": [ ... ],
  "errors_by_status": {
    "500": 5,
    "503": 2
  },
  "errors_by_step": {
    "create-person": 3,
    "patch-phone": 4
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Flow name from `config-run.yml` |
| `type` | string | `end-to-end` or `alone` |
| `total_requests` | int | Total HTTP requests sent (analysis window only) |
| `successful_requests` | int | Requests that received a 2xx response |
| `failed_requests` | int | Requests that received a non-2xx response or errored |
| `rps_achieved` | float | Measured iterations/s during the analysis window |
| `stable_rps_target` | float | Configured `requests_per_second` goal |
| `stable_rps_achieved` | float | Actual measured RPS during the analysis window |
| `stable_rps_max_reached` | bool | `true` when the target RPS was not reachable; TYA ran at max achievable rate |
| `ramp_up_duration_s` | float | Wall-clock seconds spent in the ramp-up and plateau-detection phases |
| `analysis_duration_s` | float | Wall-clock seconds of the stable analysis window (the `duration` config value) |
| `avg_concurrency` | float | Time-averaged number of goroutines running concurrently during analysis |
| `max_concurrency` | int | Peak concurrent goroutines observed during analysis |
| `think_time_applied_ms` | float | Mean sleep applied per goroutine iteration to self-regulate pace (ms) |
| `latency_ms` | LatencyStats | Aggregate latency across all steps (analysis window only) |
| `steps` | array | Per-step breakdown (see StepReport below) |
| `children` | array | Step reports from wire-flow children (omitted if none) |
| `errors_by_status` | object | Count of failures grouped by HTTP status code |
| `errors_by_step` | object | Count of failures grouped by step ID |

---

## LatencyStats

```json
{
  "min": 3.2,
  "max": 412.7,
  "mean": 38.1,
  "p50": 29.4,
  "p90": 88.2,
  "p95": 134.6,
  "p99": 389.1
}
```

All values are in **milliseconds**.

| Field | Description |
|-------|-------------|
| `min` | Fastest request |
| `max` | Slowest request |
| `mean` | Arithmetic mean |
| `p50` | Median — 50% of requests completed within this time |
| `p90` | 90th percentile |
| `p95` | 95th percentile — commonly used SLA threshold |
| `p99` | 99th percentile — captures tail latency |

---

## StepReport

Each entry in `steps` (and `children`) is a `StepReport`:

```json
{
  "step_id": "create-person",
  "requests": 187,
  "errors": 2,
  "latency_ms": {
    "min": 4.1,
    "max": 204.3,
    "mean": 41.2,
    "p50": 33.8,
    "p90": 94.1,
    "p95": 141.7,
    "p99": 199.2
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `step_id` | string | The `id` field from the step config, or `<method> <endpoint>` if no id set |
| `requests` | int | Requests sent for this step |
| `errors` | int | Non-2xx responses or network errors |
| `latency_ms` | LatencyStats | Latency stats for this step only |

---

## Interpreting Results

**Error rate:**
```
error_rate = failed_requests / total_requests
```

**Throughput gap:**
If `stable_rps_achieved` is significantly below `stable_rps_target`, check:
- `stable_rps_max_reached: true` — the system under test is saturated.
- `errors_by_status` for upstream errors (429, 503).
- `avg_concurrency` vs `max_concurrency` — a large gap suggests bursty latency spikes.

**Ramp-up cost:**
`ramp_up_duration_s` is excluded from `total_requests` and all latency stats. Only the stable analysis window contributes to the report metrics.

**Think-time:**
A non-zero `think_time_applied_ms` means goroutines are sleeping between iterations to self-regulate pace. If think-time is zero and concurrency is high, the API is slower than the target pace allows.

**Tail latency:**
A high p99 relative to p50 indicates intermittent slow responses — common causes are GC pauses, DB contention, or network jitter. The step-level breakdown in `steps[]` will isolate which endpoint is responsible.

**Wire-flow children:**
Step reports in `children` reflect teardown/cleanup steps that ran after the main load finished. Their latencies are not included in the flow-level `latency_ms` aggregate.
