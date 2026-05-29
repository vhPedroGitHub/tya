# TYA Concepts

This document covers the core concepts you need to understand to configure and run TYA effectively.

## Project Layout

After `tya init`, your project looks like:

```
<project>/
  config-create.yml        # Controls payload generation
  config-run.yml           # Defines flows to execute
  models/                  # Generated JSON schemas (one per OpenAPI model)
  api/                     # One sub-folder per endpoint
    <endpoint>/
      config.yml           # Parameter definitions and JSON mapping
      <HTTP_METHOD>/
        payload_1.json
        payload_2.json
        ...
```

## Flows

A **flow** is a named sequence of HTTP requests executed against your API. Flows are defined in `config-run.yml` under the `flows` key.

### Flow Types

| Type | Description |
|------|-------------|
| `end-to-end` | Multi-step flow executed sequentially. Supports data extraction between steps. |
| `alone` | Single endpoint + method, no chaining. |
| `iterate` | Processes every item in a global-bucket list sequentially. Steps run once per item at the configured RPS. |

### Flow Fields

```yaml
flows:
  - name: my-flow          # Unique name (required)
    type: end-to-end       # end-to-end | alone
    duration: 60s          # How long to run
    requests_per_second: 50
    auth: my-auth-profile  # Reference to an auth_profiles entry
    depends_on:            # Wait for these flows to complete first
      - other-flow
    steps:
      - ...
```

### Flow Dependencies

Use `depends_on` to declare that a flow must not start until listed flows have finished. TYA validates the dependency graph at startup (existence check + cycle detection) and executes flows in topological order.

```yaml
flows:
  - name: seed-data
    type: alone
    ...

  - name: run-tests
    type: end-to-end
    depends_on:
      - seed-data
    ...
```

### Iterate Flows

An **iterate flow** (`type: iterate`) processes every item in a global-bucket list sequentially. It reads the list once, then executes all steps for each item at the pace controlled by `requests_per_second`.

```yaml
flows:
  - name: seed-users
    type: end-to-end
    duration: 10s
    requests_per_second: 5
    auth: app-user
    steps:
      - id: create-person
        endpoint: /persons
        method: POST
        payload_strategy: template-json
        payload_overrides:
          email: "user-{{ uuid }}@example.com"
        extract:
          - field: response.body.id
            as: created_id
            global_list: true   # append to GlobalBucket["seed-users"]["created_id"]

  - name: update-each-user
    type: iterate
    iterate_list: seed-users.created_id   # source: "flow-name.key"
    item_variable: item                   # template key (default: "item")
    duration: 60s
    requests_per_second: 5                # 5 items/sec × 2 steps = 10 HTTP calls/sec
    auth: app-user
    depends_on:
      - seed-users
    steps:
      - id: patch-phone
        endpoint: /persons/{{ .item }}
        method: PATCH
        payload_strategy: template
        payload_template: |
          { "phone": "+1-{{ randomDigits 4 }}" }
```

**How it works:**
1. The flow reads the list from `GlobalBucket["seed-users"]["created_id"]`.
2. For each item in the list, it executes all steps sequentially.
3. The current item is available in templates as `{{ .item }}` (configurable via `item_variable`).
4. Items are processed at a rate of `1 / requests_per_second` seconds per item.
5. `duration` is a safety cap; the flow ends when all items are processed or duration expires.

**Iterate flow fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `iterate_list` | Yes | Source list in `"flow-name.key"` format |
| `item_variable` | No | Template key for the current item (default: `"item"`) |
| `requests_per_second` | Yes | Items processed per second |
| `duration` | Yes | Safety cap |
| `auth` | No | Auth profile |
| `depends_on` | No | Dependencies |
| `steps` | Yes | Steps to execute per item |

---

## Steps

A step is a single HTTP request within a flow. Each step can extract data from the response to pass to subsequent steps.

```yaml
steps:
  - id: create-user         # Optional; required for extract references
    endpoint: /users
    method: POST
    payload_strategy: random
    extract:
      - field: response.body.id
        as: user_id
```

### Payload Strategies

| Strategy | Description |
|----------|-------------|
| `random` | Picks a random payload from `api/<endpoint>/<METHOD>/` |
| `fixed` | Uses the file at `payload_file` |
| `template` | Renders `payload_template` as a Go `text/template` against the flow context |
| `extracted` | Uses the full response body from a previous step (`from_step`) |
| `template-json` | Loads a base JSON (from `payload_file` or a random payload) and applies field-level overrides rendered as templates |

**Template example:**

```yaml
- id: place-order
  endpoint: /orders
  method: POST
  payload_strategy: template
  payload_template: |
    {
      "product_id": "{{ .product_id }}",
      "quantity": 1
    }
```

**`template-json` example:**

Use `template-json` when you want to reuse a rich pre-generated payload (realistic fake data) but need to override specific fields at runtime — for example, to guarantee a unique email per goroutine:

```yaml
- id: create-user
  endpoint: /persons
  method: POST
  payload_strategy: template-json
  payload_overrides:
    email: "user-{{ uuid }}@example.com"
    phone: "+1-555-{{ randomDigits 7 }}"
  extract:
    - field: response.body.id
      as: user_id
```

`payload_file` is optional. When omitted, a random payload is selected from `api/<endpoint>/<METHOD>/` and the overrides are applied on top. Each override value is a template string rendered against the current flow context. Numeric and boolean override values are preserved as their native JSON types.

---

### Template Functions

All template strings — `payload_template`, `payload_overrides` values, and endpoint paths — support the following built-in functions in addition to standard Go `text/template` syntax:

| Function | Signature | Description |
|----------|-----------|-------------|
| `uuid` | `uuid` | Random UUID v4 string, e.g. `"a1b2c3d4-e5f6-4..."` |
| `randomInt` | `randomInt` | Random non-negative integer as a string |
| `randomInt64` | `randomInt64` | Random non-negative int64 as a string |
| `randomDigits` | `randomDigits N` | String of exactly N random decimal digits |
| `timestamp` | `timestamp` | Current Unix timestamp in seconds |
| `timestampMs` | `timestampMs` | Current Unix timestamp in milliseconds |
| `upper` | `upper "hello"` | Converts a string to upper-case |
| `lower` | `lower "HELLO"` | Converts a string to lower-case |
| `globalGet` | `globalGet "flow" "key"` | Reads a value from the global bucket snapshot (see Global Bucket) |

**Examples:**

```yaml
# Unique email per goroutine — never collides under concurrent load
email: "user-{{ uuid }}@example.com"

# Unique username with timestamp suffix
username: "load-user-{{ timestampMs }}"

# Random phone number
phone: "+1-555-{{ randomDigits 7 }}"

# Dynamic endpoint path
endpoint: /orders/{{ .order_id }}/items/{{ randomInt }}
```

Environment variables can be interpolated with `${VAR}` inside templates and fixed payloads.

---

## Flow Execution Context

Each goroutine running a flow maintains an isolated `map[string]any` context for the duration of that execution. Values are written into it by:

1. **`extract` blocks** — pull fields from the previous response (e.g. `response.body.items[0].id`) and store them under a named key (`as`).
2. **Auth injection** — the auth layer automatically stores `access_token`, `refresh_token`, etc.
3. **`_base_url`** — the base URL is always available under this key.

Extracted values are then referenced in subsequent steps via `{{ .key }}` in templates, or in dynamic endpoint paths:

```yaml
- endpoint: /persons/{{ .create-person.response.body.id }}
  method: GET
```

---

## Global Bucket

The **global bucket** is a thread-safe, cross-flow key-value store that persists extracted values across flows within a single run. Values are namespaced by the flow that wrote them, so keys from different flows never collide.

### Writing to the Global Bucket

Set `global: true` on any extractor to persist the extracted value into the global bucket under the flow's namespace:

```yaml
flows:
  - name: seed-directory
    steps:
      - id: create-person
        endpoint: /persons
        method: POST
        payload_strategy: template-json
        payload_overrides:
          email: "seed-{{ uuid }}@tya.dev"
        extract:
          - field: response.body.id
            as: created_id
            global: true   # writes to GlobalBucket["seed-directory"]["created_id"]
```

### Reading from the Global Bucket

Use `{{ globalGet "flow-name" "key" }}` in templates:

```yaml
  - name: audit-and-cleanup
    depends_on: [seed-directory]
    steps:
      - id: verify-seeded-person
        endpoint: /persons/{{ globalGet "seed-directory" "created_id" }}
        method: GET
```

Alternatively, use `{{ index .global "flow-name" "key" }}`:

```yaml
        endpoint: /persons/{{ index .global "seed-directory" "created_id" }}
```

### Synchronization

- **Write**: thread-safe via mutex, last-write-wins semantics.
- **Read**: at the start of each goroutine iteration the full bucket snapshot is injected into `fCtx["global"]` as `map[string]map[string]any`.
- **Ordering**: `depends_on` guarantees the producing flow has completed before the consuming flow starts, so the global bucket is never empty when read.

### Rules

- Each flow writes into its own namespace only (the flow name).
- The bucket is created once per run and shared across all flows.
- Values are **not** persisted to disk — the bucket exists only for the duration of the run.

---

## Authentication

Auth profiles are defined once in `config-run.yml` and referenced by name in flows.

### Supported Auth Types

| Type | Description |
|------|-------------|
| `oauth2_password` | OAuth2 Resource Owner Password flow with token refresh |
| `oauth2_client_credentials` | Machine-to-machine OAuth2 |
| `api_key` | Static key injected into a header or query parameter |
| `basic` | HTTP Basic Auth |
| `custom_login` | Login via arbitrary API endpoint with token extraction |

All `auth_profiles` values support `${ENV_VAR}` interpolation so secrets stay out of config files.

### Example: custom_login

```yaml
auth_profiles:
  - name: app-user
    type: custom_login
    login_endpoint: /auth/login
    method: POST
    payload: |
      { "email": "${TEST_USER}", "password": "${TEST_PASS}" }
    extract_token:
      access_token:  response.body.access_token
      refresh_token: response.body.refresh_token
      expires_in:    response.body.expires_in
    refresh_endpoint: /auth/refresh
    refresh_method: POST
    refresh_payload: |
      { "refresh_token": "{{ .refresh_token }}" }
    refresh_extract:
      access_token:  response.body.access_token
      refresh_token: response.body.refresh_token
      expires_in:    response.body.expires_in
```

### Token Lifecycle

- At flow start, TYA acquires a token for the goroutine.
- Before each step, if the token is within `refresh_before_expiry` of expiry, TYA refreshes it transparently.
- On 401 responses, TYA can re-authenticate once if `retry_on_401: true` is set.
- Each goroutine holds its own token — no shared state, no race conditions.

---

## Execution Engine

The `run` command uses a **four-phase adaptive load engine** per flow:

### Phase 1 & 2 — Ramp-up and Plateau Detection

Instead of slamming the target RPS from the first tick, TYA grows load incrementally:

1. Starts at `initial_rps` (default: 1).
2. Each **step window** (`step_window`, default: 2 s), it measures the observed HTTP calls/s.
3. Multiplies the ticker rate by `factor` (default: 1.5) until target RPS is reached.
4. Declares plateau when `stability_windows` (default: 3) consecutive windows are all within `stability_threshold` (default: 5 %) of each other.

#### Negative Resets

A **negative reset** is any window where the observed RPS drops below the previous window's RPS (regardless of the stability threshold). This signals the system is struggling, not just oscillating.

TYA tracks the **total** number of negative resets (not consecutive). When this count reaches `max_negative_resets` (default: 3), a **forced plateau** is triggered immediately:

- The analysis RPS is set to the average of the best `best_windows_avg` (default: 3) stable windows recorded so far — the ones closest to the target RPS.
- The report flags `forced_plateau: true`, `forced_plateau_reason: "negative_resets"`, and records the computed `forced_plateau_rps`.
- All per-window diagnostics (observed RPS, variation, negative-reset flag, consecutive count) are recorded in `ramp_up_windows` in the report.

#### Ramp-up Timeout

If `max_ramp_duration` (default: 600 s) elapses before a natural or negative-reset plateau is reached, TYA forces the plateau with the same best-windows average and sets `forced_plateau_reason: "timeout"`.

If the target RPS is never reachable (system saturated), TYA logs a warning and runs the analysis at the highest achievable rate (`stable_rps_max_reached: true` in the report).

### Phase 3 — Analysis Window

Once plateau is detected, the `duration` timer starts, metrics are reset, and TYA collects the stable-state measurements. The ramp-up time is reported separately as `ramp_up_duration_s`.

### Phase 4 — Drain

After the analysis window expires, TYA waits for all in-flight goroutines to finish (`wg.Wait()`) before signalling completion to dependent flows.

### Concurrency Cap

A semaphore limits concurrent goroutines to `ceil(currentRPS × p95_latency_s × 1.5)` (minimum 8). This prevents goroutines from accumulating unboundedly when the target API is slow — a classic runaway load-generator failure mode.

### Think-Time

After executing all steps, each goroutine sleeps for the remainder of its target iteration duration:

```
think_time = max(0, (N_steps / current_rps) − actual_elapsed)
```

This self-regulates the pace without relying on an external ticker alone. The semaphore slot is held during the sleep, contributing to the concurrency measurement. The mean applied think-time is reported as `think_time_applied_ms`.

### Configuring Ramp-Up Per Flow

Add an optional `ramp_up:` section to any flow. Omit it entirely to use the built-in defaults.

```yaml
flows:
  - name: heavy-load
    type: end-to-end
    duration: 60s
    requests_per_second: 100
    ramp_up:
      initial_rps: 2             # Start here (default: 1)
      factor: 2.0                # Growth multiplier per step (default: 1.5)
      step_window: 3s            # Measurement window per ramp step (default: 2s)
      stability_windows: 4       # Consecutive stable windows needed (default: 3)
      stability_threshold: 0.03  # Max relative variation for "stable" (default: 0.05)
      max_ramp_duration: 120s    # Hard timeout for ramp-up phase (default: 600s)
      max_negative_resets: 3     # Total negative resets before forced plateau (default: 3)
      best_windows_avg: 3        # Top-N stable windows averaged for forced plateau RPS (default: 3)
    steps:
      - ...
```

See [metrics.md](metrics.md) for the full report format including all adaptive engine fields.

---

## config-create.yml Reference

```yaml
payloads_per_method: 5          # Default payloads per endpoint+method

overrides:
  - endpoint: /users
    method: POST
    count: 10
  - endpoint: /orders/{id}
    method: GET
    count: 2
```

## config-run.yml Reference

```yaml
base_url: http://localhost:8080  # Target API base URL

auth_profiles:
  - name: ...
    type: ...
    # (type-specific fields)

flows:
  - name: ...
    type: end-to-end | alone
    duration: 60s
    requests_per_second: 20
    auth: <profile-name>
    depends_on: [...]
    ramp_up:                     # optional; all sub-fields have defaults
      initial_rps: 1
      factor: 1.5
      step_window: 2s
      stability_windows: 3
      stability_threshold: 0.05
    steps:
      - id: ...
        endpoint: /path
        method: GET | POST | PUT | PATCH | DELETE
        payload_strategy: random | fixed | template | extracted | template-json
        payload_file: ...        # for fixed; optional base for template-json
        payload_template: |      # for template
          { ... }
        payload_overrides:       # for template-json — dot-path: template-string
          email: "user-{{ uuid }}@example.com"
          phone: "+1-{{ randomDigits 10 }}"
        from_step: ...           # for extracted
        extract:
          - field: response.body.some.path
            as: my_key
            global: true          # optional; also writes to the global bucket
```
