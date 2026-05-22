# Config-Run Example: Full API Lifecycle Load Test

This document walks through a complete, production-grade `config-run.yml` that exercises every major TYA feature in a single run.

## The Scenario

We are load-testing a REST API that manages "persons" (CRUD resource). The test simulates a realistic workload:

1. **Seed phase** — Create a pool of test users via concurrent POST requests
2. **Stress phase** — Hammer a read-only endpoint with sustained load
3. **Iterate phase** — Process every seeded item through a multi-step lifecycle (GET → PATCH → verify)
4. **Smoke phase** — Run a lightweight verification after all load has drained
5. **Cleanup** — Wire-flow children delete all created resources

## Full Configuration

```yaml
# ─── Target ─────────────────────────────────────────────────────────────────
base_url: http://localhost:8080

# ─── Authentication ─────────────────────────────────────────────────────────
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

# ─── Flows ──────────────────────────────────────────────────────────────────
flows:
  # ── Phase 1: Seed ────────────────────────────────────────────────────────
  - name: seed-persons
    type: end-to-end
    auth: app-user
    duration: 10s
    requests_per_second: 20
    steps:
      - id: create
        endpoint: /persons
        method: POST
        payload_strategy: template
        payload_template: |
          {
            "first_name": "Seed",
            "last_name":  "User",
            "email":      "seed-{{ timestampMs }}@test.com"
          }
        extract:
          # Store each created ID in a global list for later iteration.
          # expand: true means each ID becomes a separate list entry
          # (not a single array entry).
          - field: response.body.id
            as: person_id
            global_list: true
            expand: true

  # ── Phase 2: Stress Read ─────────────────────────────────────────────────
  - name: stress-read
    type: alone
    auth: app-user
    duration: 30s
    requests_per_second: 50
    depends_on:
      - seed-persons
    steps:
      - id: list
        endpoint: /persons
        method: GET

  # ── Phase 3: Iterate Lifecycle ───────────────────────────────────────────
  - name: iterate-lifecycle
    type: iterate
    auth: app-user
    requests_per_second: 15
    iterate_list: seed-persons.person_id
    depends_on:
      - stress-read
    steps:
      # Step 1: Read the person
      - id: get-person
        endpoint: /persons/{{ .item }}
        method: GET
        extract:
          - field: response.body.first_name
            as: original_name
          - field: response.body.email
            as: original_email

      # Step 2: Patch the phone number
      - id: patch-phone
        endpoint: /persons/{{ .item }}
        method: PATCH
        payload_strategy: template
        payload_template: |
          { "phone": "+1-555-{{ randomDigits 4 }}" }

      # Step 3: Verify the person still exists and name is unchanged
      - id: verify
        endpoint: /persons/{{ .item }}
        method: GET
        extract:
          - field: response.body.first_name
            as: verified_name

  # ── Phase 4: Smoke Test ──────────────────────────────────────────────────
  - name: smoke-verify
    type: alone
    auth: app-user
    duration: 5s
    requests_per_second: 5
    depends_on:
      - iterate-lifecycle
    steps:
      - id: count
        endpoint: /persons
        method: GET

  # ── Phase 5: Cleanup (wire-flow children) ────────────────────────────────
  - name: iterate-cleanup
    type: iterate
    auth: app-user
    requests_per_second: 20
    iterate_list: seed-persons.person_id
    depends_on:
      - smoke-verify
    steps:
      - id: delete
        endpoint: /persons/{{ .item }}
        method: DELETE
```

## Detailed Explanation

### Auth Profile: `custom_login`

```yaml
auth_profiles:
  - name: app-user
    type: custom_login
```

TYA logs in via `POST /auth/login` before each goroutine starts its flow. The tokens are stored in the flow context and automatically injected as `Authorization: Bearer <access_token>` on every request. When the access token nears expiry, TYA performs the refresh flow transparently.

Environment variable interpolation (`${TEST_USER}`, `${TEST_PASS}`) keeps secrets out of the config file.

### Phase 1: Seed (`end-to-end`)

```yaml
- name: seed-persons
  type: end-to-end
  duration: 10s
  requests_per_second: 20
```

**What happens:** TYA fires goroutines at a rate that produces 20 HTTP calls/s. Since this flow has 1 step, that means ~20 iterations/s, each creating 1 person.

**Key feature — `global_list` + `expand`:**

```yaml
extract:
  - field: response.body.id
    as: person_id
    global_list: true
    expand: true
```

Each created person's ID is appended to a list in the global bucket under `seed-persons.person_id`. The `expand: true` flag ensures that if the response field were an array, each element would become a separate list entry (useful for `GET /persons` returning `{ "data": [...] }`).

After 10s at 20 RPS with 1 step, you'll have ~200 person IDs in the list.

### Phase 2: Stress Read (`alone`)

```yaml
- name: stress-read
  type: alone
  duration: 30s
  requests_per_second: 50
  depends_on:
    - seed-persons
```

**`depends_on`** ensures this flow doesn't start until `seed-persons` has fully completed. The adaptive ramp-up engine kicks in: it starts at a low RPS and multiplicatively increases (factor 1.5) until it reaches 50 HTTP calls/s or detects a plateau.

**`alone` type** means this is a single-endpoint load test — no sequential chaining between steps. The flow context is not shared between iterations.

### Phase 3: Iterate Lifecycle (`iterate`)

```yaml
- name: iterate-lifecycle
  type: iterate
  requests_per_second: 15
  iterate_list: seed-persons.person_id
  depends_on:
    - stress-read
```

**This is the most important flow.** It processes every item in the `seed-persons.person_id` list through 3 sequential steps:

| Step | Method | Endpoint | Purpose |
|------|--------|----------|---------|
| `get-person` | GET | `/persons/{{ .item }}` | Read person, extract name/email |
| `patch-phone` | PATCH | `/persons/{{ .item }}` | Update phone with random digits |
| `verify` | GET | `/persons/{{ .item }}` | Confirm person still exists |

**How RPS works with iterate:**

`requests_per_second: 15` means **15 HTTP calls/s**, not 15 items/s. Since each item has 3 steps, the engine processes items at `15 / 3 = 5 items/s`. Each goroutine handles 1 item (all 3 steps), then sleeps the think-time remainder to self-regulate pace.

**Item injection:** The current list item is available in templates as `{{ .item }}`. For object items, use `{{ index .item "field" }}`.

**Data extraction between steps:** The `get-person` step extracts `original_name` and `original_email` into the flow context. The `verify` step could reference these (e.g., to assert the name hasn't changed).

### Phase 4: Smoke Test (`alone`)

```yaml
- name: smoke-verify
  type: alone
  duration: 5s
  requests_per_second: 5
  depends_on:
    - iterate-lifecycle
```

A lightweight verification that runs after all iterate processing is complete. Low RPS (5) and short duration (5s) — just enough to confirm the API is still responsive after the heavy load.

### Phase 5: Cleanup (`iterate` + wire-flow pattern)

```yaml
- name: iterate-cleanup
  type: iterate
  requests_per_second: 20
  iterate_list: seed-persons.person_id
  depends_on:
    - smoke-verify
```

Processes the same list of person IDs, but this time each item gets a single DELETE request. Higher RPS (20) to clean up quickly.

**Alternative with wire-flow children:**

If you want cleanup to run as a child of the iterate flow (inheriting its final context), you can declare it inline:

```yaml
- name: iterate-lifecycle
  type: iterate
  requests_per_second: 15
  iterate_list: seed-persons.person_id
  steps:
    - id: get-person
      endpoint: /persons/{{ .item }}
      method: GET
    - id: patch-phone
      endpoint: /persons/{{ .item }}
      method: PATCH
      payload_strategy: template
      payload_template: |
        { "phone": "+1-555-{{ randomDigits 4 }}" }
    - id: verify
      endpoint: /persons/{{ .item }}
      method: GET
  children:
    - name: cleanup
      type: iterate
      auth: app-user
      requests_per_second: 20
      iterate_list: seed-persons.person_id
      steps:
        - id: delete
          endpoint: /persons/{{ .item }}
          method: DELETE
```

Children run sequentially after the parent's goroutine pool drains, inheriting the final flow context.

## Execution

```bash
# Dry-run (single pass, ignores RPS targets)
tya run -t --config config-run.yml

# Full load test
TEST_USER=admin@test.com TEST_PASS=secret tya run --config config-run.yml
```

## Expected Output

```
flow starting          flow=seed-persons       type=end-to-end
plateau reached        stable_rps=20           ramp_up_duration_s=4.2
flow finished          flow=seed-persons       total_requests=201  errors=0
flow starting          flow=stress-read        type=alone          depends_on=[seed-persons]
plateau reached        stable_rps=50           ramp_up_duration_s=3.8
flow finished          flow=stress-read        total_requests=1502 errors=0
flow starting          flow=iterate-lifecycle  type=iterate        depends_on=[stress-read]
iterate: starting      iterate_list=seed-persons.person_id  items=201  rps=15
flow finished          flow=iterate-lifecycle  total_requests=603  errors=0
flow starting          flow=smoke-verify       type=alone          depends_on=[iterate-lifecycle]
flow finished          flow=smoke-verify       total_requests=25   errors=0
flow starting          flow=iterate-cleanup    type=iterate        depends_on=[smoke-verify]
iterate: starting      iterate_list=seed-persons.person_id  items=201  rps=20
flow finished          flow=iterate-cleanup    total_requests=201  errors=0
report written         path=tya-report.json
```

## Report Highlights

The JSON report (`tya-report.json`) will contain per-flow summaries:

| Flow | Type | Target RPS | Achieved RPS | Requests | Errors |
|------|------|-----------|-------------|----------|--------|
| seed-persons | end-to-end | 20 | ~19.5 | ~200 | 0 |
| stress-read | alone | 50 | ~49.2 | ~1500 | 0 |
| iterate-lifecycle | iterate | 15 | ~14.8 | ~600 | 0 |
| smoke-verify | alone | 5 | ~4.9 | ~25 | 0 |
| iterate-cleanup | iterate | 20 | ~19.6 | ~200 | 0 |

Each flow also includes per-step latency percentiles (p50, p90, p95, p99), error breakdowns by status code, and ramp-up window diagnostics.

## k6 Equivalent

Generate k6 scripts from the same config:

```bash
tya genk6 config-run.yml -o k6-output/
```

The iterate flows will use `constant-arrival-rate` executor with:
- `rate = ceil(rps / nSteps)` iterations/s
- Each VU iteration processes exactly 1 item (via `__ITER` index)
- Early return when `__ITER >= items.length` (list exhausted)

Run with:

```bash
tya runk6s k6-output/
```
