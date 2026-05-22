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

```yaml
base_url: http://localhost:8080

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

flows:
  - name: person-lifecycle
    type: end-to-end
    duration: 60s
    requests_per_second: 20
    auth: app-user
    steps:
      - id: create-person
        endpoint: /persons
        method: POST
        payload_strategy: random          # picks a random file from api/persons/post/

      - id: get-person
        endpoint: /persons/{{ .create-person.response.body.id }}
        method: GET

      - id: patch-phone
        endpoint: /persons/{{ .create-person.response.body.id }}
        method: PATCH
        payload_strategy: template
        payload_template: |
          { "phone": "+1-555-{{ randomDigits 4 }}" }

      - id: delete-person
        endpoint: /persons/{{ .create-person.response.body.id }}
        method: DELETE

    children:
      - name: verify-empty
        type: alone
        auth: app-user
        steps:
          - id: list-persons
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

TYA spins up a goroutine pool targeting 20 RPS for 60 seconds. Each goroutine runs the full four-step lifecycle independently with its own token and its own person record — no shared state, no race conditions.

At the end it writes a JSON report:

```
tya-report-20250101-120000.json
```

### Step 7 — Generate a PDF report

```bash
pip install reportlab
python scripts/tya-report-pdf.py tya-report-20250101-120000.json
# Report written to tya-report-20250101-120000.pdf
```

The PDF includes a summary table across all flows, per-flow latency percentiles (p50/p95/p99), per-step breakdowns, and the wire-flow child results.

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
