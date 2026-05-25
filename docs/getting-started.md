# Getting Started with TYA

TYA (Test Your API) is a CLI tool for testing and load-testing REST APIs. This guide walks you from installation to your first successful load run against a real API.

## Prerequisites

- **Docker** — must be installed and reachable (used by `tya create` to run `openapi-generator-cli`)
- **Java** — required for `openapi-generator-cli`
- **Go 1.22+** — to build from source

## Installation

```bash
git clone https://github.com/vhPedroGitHub/tya
cd tya
go build -o bin/tya ./cmd/tya/cli
export PATH=$PATH:$(pwd)/bin
```

---

## Real-World Example: Person Lifecycle Flow

This section walks through a complete, working example using the demo REST API that ships with TYA (`cmd/app`). The API has JWT authentication and a full CRUD for a `Person` resource — a realistic target for demonstrating everything TYA can do.

### Step 1 — Start the demo API

```bash
go build -o bin/app ./cmd/app
export TEST_USER=alice@example.com
export TEST_PASS=s3cr3t
bin/app &
# Listening on :8080 by default
```

Register the test user once:

```bash
curl -s -X POST http://localhost:8080/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","password":"s3cr3t"}'
# {"id":1,"email":"alice@example.com"}
```

### Step 2 — Initialise a TYA project

```bash
mkdir persons-test && cd persons-test
tya init
```

TYA checks that Docker and Java are available, then scaffolds:

```
persons-test/
  config-create.yml
  config-run.yml
  models/
  api/
```

### Step 3 — Generate payloads from the OpenAPI spec

Copy or write the OpenAPI spec for the demo app, then run:

```bash
tya create openapi.yaml
```

TYA generates:

```
models/
  Person.json          ← JSON schema for the Person model
  User.json
api/
  persons/
    config.yml
    post/
      payload_1.json   ← {"first_name":"Liam","last_name":"Torres","email":"liam.torres@example.com",...}
      payload_2.json
      ...
  persons_{id}/
    config.yml
    get/
    patch/
    delete/
```

Each payload is seeded with realistic fake data via `gofakeit`, respecting field types and constraints from the spec.

### Step 4 — Configure the flow

Replace the contents of `config-run.yml` with the following. This defines a **person lifecycle** end-to-end flow: create a person, fetch it by ID, patch its phone, then delete it. Every step reuses the `id` returned by the first step — no hardcoded IDs.

The `ramp_up` block demonstrates all the new adaptive engine options, including negative-reset detection and the ramp-up timeout:

```yaml
base_url: http://localhost:8080

auth_profiles:
  - name: app-user
    type: custom_login
    login_endpoint: /auth/login
    method: POST
    payload: |
      { "email": "alice@example.com", "password": "s3cr3t" }
    extract_token:
      access_token: response.body.access_token
      refresh_token: response.body.refresh_token
      expires_in: response.body.expires_in
    refresh_endpoint: /auth/refresh
    refresh_method: POST
    refresh_payload: |
      { "refresh_token": "{{ .refresh_token }}" }
    refresh_extract:
      access_token: response.body.access_token
      refresh_token: response.body.refresh_token
      expires_in: response.body.expires_in

flows:
  - name: person-lifecycle
    type: end-to-end
    duration: 30s
    requests_per_second: 5
    auth: app-user
    steps:
      - id: list-persons
        endpoint: /persons
        method: GET

      - id: create-person
        endpoint: /persons
        method: POST
        payload_strategy: random
        extract:
          - field: response.body.id
            as: person_id

      - id: get-person
        endpoint: /persons/{{ .person_id }}
        method: GET

      - id: patch-person
        endpoint: /persons/{{ .person_id }}
        method: PATCH
        payload_strategy: template
        payload_template: |
          { "phone": "+1-555-9999" }

      - id: delete-person
        endpoint: /persons/{{ .person_id }}
        method: DELETE

  - name: smoke-get-persons
    type: alone
    duration: 10s
    requests_per_second: 10
    auth: app-user
    steps:
      - id: list-persons-smoke
        endpoint: /persons
        method: GET

```

**What this flow does, step by step:**

| Step | What happens |
|------|-------------|
| `create-person` | `POST /persons` with a random pre-generated payload. The response `id` is stored in the flow context. |
| `get-person` | `GET /persons/{id}` — uses the real ID from the previous step via `{{ .create-person.response.body.id }}`. |
| `patch-phone` | `PATCH /persons/{id}` — sends a template payload with a randomly generated phone number. |
| `delete-person` | `DELETE /persons/{id}` — cleans up the record created in this goroutine's execution. |
| `verify-empty` (child) | Runs **once** after the entire load pool drains. Lists all persons to verify the database is clean. |

TYA acquires a JWT token at the start of each goroutine and automatically refreshes it before it expires — no manual auth management per step.

### Step 5 — Verify with test mode

Before running a full load test, execute each step exactly once to confirm the flow is correctly configured:

```bash
export TEST_USER=alice@example.com
export TEST_PASS=s3cr3t
tya run -t
```

Expected output:

```
INFO  flow started        {"flow": "person-lifecycle", "mode": "test"}
INFO  step ok             {"step": "create-person", "status": 201, "latency_ms": 12}
INFO  step ok             {"step": "get-person",    "status": 200, "latency_ms": 4}
INFO  step ok             {"step": "patch-phone",   "status": 200, "latency_ms": 5}
INFO  step ok             {"step": "delete-person", "status": 204, "latency_ms": 6}
INFO  child step ok       {"step": "list-persons",  "status": 200, "latency_ms": 3}
INFO  report written      {"path": "tya-report-20250101-120000.json"}
```

### Step 6 — Run the load test

```bash
tya run
```

TYA ramps up gradually to 20 HTTP calls/s (= 5 goroutines/s × 4 steps) and then holds the stable window for 60 seconds. You'll see ramp-up diagnostics in the log:

```
INFO  ramp-up window  {"window": 1, "current_http_rps_target": 2, "observed_http_rps": 1.5, "stable": false, "negative_reset": false}
INFO  ramp-up window  {"window": 2, "current_http_rps_target": 3, "observed_http_rps": 3.0, "stable": false, "negative_reset": false}
INFO  ramp-up window  {"window": 3, "current_http_rps_target": 4.5, "observed_http_rps": 2.8, "stable": false, "negative_reset": true,  "consecutive_negative_resets": 1}
INFO  ramp-up window  {"window": 4, "current_http_rps_target": 6.75, "observed_http_rps": 6.5, "stable": false, "negative_reset": false}
INFO  ramp-up window  {"window": 5, "current_http_rps_target": 20, "observed_http_rps": 19.4, "stable": false, "negative_reset": false}
INFO  ramp-up window  {"window": 6, "current_http_rps_target": 20, "observed_http_rps": 19.8, "stable": true,  "negative_reset": false}
INFO  ramp-up window  {"window": 7, "current_http_rps_target": 20, "observed_http_rps": 19.9, "stable": true,  "negative_reset": false}
INFO  plateau reached  {"ramp_up_duration_s": 14.0, "stable_rps": 20, "forced_plateau": false}
INFO  report written   {"path": "tya-report-20250101-120000.json"}
```

> **Negative reset example:** window 3 shows `"negative_reset": true` — the observed RPS dropped from 3.0 to 2.8. This counts towards `max_negative_resets`. If 3 total negative resets accumulate before a natural plateau, TYA forces the plateau using the average of the best stable windows seen so far.

The JSON report includes the full ramp-up window history and engine diagnostics:

```json
{
  "name": "person-lifecycle",
  "type": "end-to-end",
  "total_requests": 1200,
  "successful_requests": 1200,
  "failed_requests": 0,
  "rps_achieved": 19.9,
  "iterations_per_second": 4.97,
  "stable_rps_target": 20.0,
  "stable_rps_achieved": 19.9,
  "stable_rps_max_reached": false,
  "forced_plateau": false,
  "forced_plateau_reason": "",
  "forced_plateau_rps": 0,
  "negative_resets": 1,
  "ramp_up_duration_s": 14.0,
  "analysis_duration_s": 60.1,
  "avg_concurrency": 5.0,
  "max_concurrency": 7,
  "think_time_applied_ms": 163.2,
  "ramp_up_windows": [
    {"window_index": 1, "target_rps": 2,    "observed_rps": 1.5,  "variation": 0,      "stable": false, "negative_reset": false, "consecutive_negative_resets": 0},
    {"window_index": 2, "target_rps": 3,    "observed_rps": 3.0,  "variation": 1.0,    "stable": false, "negative_reset": false, "consecutive_negative_resets": 0},
    {"window_index": 3, "target_rps": 4.5,  "observed_rps": 2.8,  "variation": -0.067, "stable": false, "negative_reset": true,  "consecutive_negative_resets": 1},
    {"window_index": 4, "target_rps": 6.75, "observed_rps": 6.5,  "variation": 1.32,   "stable": false, "negative_reset": false, "consecutive_negative_resets": 0},
    {"window_index": 5, "target_rps": 20,   "observed_rps": 19.4, "variation": 1.98,   "stable": false, "negative_reset": false, "consecutive_negative_resets": 0},
    {"window_index": 6, "target_rps": 20,   "observed_rps": 19.8, "variation": 0.021,  "stable": true,  "negative_reset": false, "consecutive_negative_resets": 0},
    {"window_index": 7, "target_rps": 20,   "observed_rps": 19.9, "variation": 0.005,  "stable": true,  "negative_reset": false, "consecutive_negative_resets": 0}
  ],
  "latency_ms": {"min": 0.2, "mean": 0.5, "p50": 0.4, "p90": 0.8, "p95": 1.1, "p99": 3.2, "max": 48.1},
  "steps": [ ... ],
  "children": [ ... ]
}
```

**Reading the ramp-up windows:**

| Field | Meaning |
|-------|---------|
| `target_rps` | HTTP calls/s the ticker was aiming for in this window |
| `observed_rps` | Actual HTTP calls/s measured during the window |
| `variation` | Signed relative change vs previous window (`(curr−prev)/prev`) |
| `stable` | `true` when `abs(variation) ≤ stability_threshold` |
| `negative_reset` | `true` when `observed_rps < prev window's observed_rps` |
| `consecutive_negative_resets` | Running count of back-to-back negative resets (diagnostic only) |

**Forced plateau example** — if the system were struggling and accumulated 3 negative resets before stabilising, the report would show:

```json
"forced_plateau": true,
"forced_plateau_reason": "negative_resets",
"forced_plateau_rps": 14.2,
"negative_resets": 3
```

TYA then runs the analysis window at 14.2 HTTP calls/s (the average of the 3 stable windows closest to the target) instead of waiting indefinitely.

### Step 7 — Generate a PDF report

```bash
pip install reportlab
python scripts/tya-report-pdf.py tya-report-20250101-120000.json
# Report written to tya-report-20250101-120000.pdf
```

The PDF includes:
- **Summary table** — one row per flow: requests, errors, error%, RPS target vs actual, ramp duration, p50/p95/p99.
- **Per-flow detail page** — KPIs, full latency breakdown, and an **Adaptive Engine Metrics** table:
  - Ramp/analysis duration, avg/max concurrency, mean think-time.
  - Plateau type: `Natural` (green) or `Forced (negative resets)` / `Forced (timeout)` (red).
  - `Negative resets` count and `Iterations/s`.
- **Ramp-up Windows table** — one row per window with target RPS, observed RPS, variation %, stable flag, and negative-reset flag. Rows with a negative reset are highlighted in red.
- **Per-step breakdown** and **wire-flow children** results.

---

## Flow Execution Context — How Data Flows Between Steps

The key to realistic end-to-end flows is the **execution context** — a `map[string]any` that each goroutine maintains throughout its lifecycle:

```
create-person  →  response.body.id = 42   →  stored as  .create-person.response.body.id
                                                          ↓
get-person     →  GET /persons/42          (real ID injected via template)
patch-phone    →  PATCH /persons/42
delete-person  →  DELETE /persons/42
```

Values extracted from responses are available in all subsequent steps via `{{ .key }}` in endpoint paths and `payload_template` bodies. Auth tokens (`access_token`, `refresh_token`) are injected automatically by the auth layer.

---

## Quick Start (no demo app)

If you just want to run against your own API:

```bash
# 1. Init
mkdir my-project && cd my-project
tya init

# 2. Generate payloads
tya create openapi.yaml

# 3. Edit config-run.yml with your base_url and flows

# 4. Verify
tya run -t

# 5. Load test
tya run
```

---

## Next Steps

- [concepts.md](concepts.md) — flows, payload strategies, auth, execution context full reference
- [commands.md](commands.md) — full flag reference for every command
- [metrics.md](metrics.md) — understanding the JSON report output
- [docker.md](docker.md) — running TYA via Docker
