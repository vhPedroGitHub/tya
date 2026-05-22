# AGENTS.md — TYA (Test Your API)

TYA is a CLI tool designed to significantly improve how you test and load-test APIs. Built with **Go** and **Cobra CLI**.

---

## General Guidelines

- **Language:** Go. CLI framework: [Cobra](https://github.com/spf13/cobra).
- **Logging:** Structured logging via [`go.uber.org/zap`](https://pkg.go.dev/go.uber.org/zap).
- **Documentation:** All commands, usage examples, and flags must be kept up to date in Markdown files inside the `docs/` folder (single `.md` per topic).

### Linting — Mandatory After Every Code Change

**Always run `golangci-lint` after every modification to Go source files.** Fix all reported issues before proceeding.

```bash
cd /opt/tya && golangci-lint run ./...
```

If `golangci-lint` is not installed:

```bash
curl -sL "https://github.com/golangci/golangci-lint/releases/download/v2.12.2/golangci-lint-2.12.2-linux-amd64.tar.gz" \
  -o /tmp/gl.tar.gz && tar -xzf /tmp/gl.tar.gz -C /tmp && \
  cp /tmp/golangci-lint-2.12.2-linux-amd64/golangci-lint /usr/local/bin/
```

- Pinned version: **v2.12.2** (matches `.github/workflows/lint.yml`).
- Config: `/opt/tya/.golangci.yml` (v2 format, top-level `version: "2"`).
- The binary is at `/usr/local/bin/golangci-lint` once installed; `$PATH` does not need updating.
- **Never commit or push code that fails linting.**

### Starting the Demo App for Testing

**Never use `pkill`/`kill` + `sleep` + restart in a single chained command** — it blocks the shell and causes timeouts.

Instead, always do it in separate steps:

1. Check if the app is already running: `pgrep -fa bin/app`
2. If not running, start it: `nohup /opt/tya/bin/app > /tmp/app.log 2>&1 &`
3. Poll until ready (do **not** use `sleep N`):
   ```bash
   for i in $(seq 1 10); do
     curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/auth/login \
       -d '{}' -H 'Content-Type: application/json' && break
     sleep 1
   done
   ```
4. Register / seed test data only if the user doesn't already exist (a 409 is fine to ignore).

### Project Structure

```
cmd/tya/cli/
  main.go                  # CLI entrypoint

pkg/
  commands/                # One file per command (e.g. init.go, create.go, run.go)
  cli_functions/           # Shared business logic, callable from multiple commands
  models/                  # Data structures; files named [command]_options.go
  configyml/               # YAML config structs, validation, load/write helpers
```

---

## Commands

### `init`

Initializes a new TYA project, scaffolding all required folders and files. By default, the structure is created in the current directory. Use `--name` / `-n` to specify a custom project name.

**Prerequisite checks performed at init time:**
- Docker is installed and reachable.
- Java is available (required for `openapi-generator-cli`).

**Files and folders created:**

```
<project>/
  config-create.yml        # Options for the `create` command
  config-run.yml           # Flow definitions for the `run` command
  models/                  # Generated JSON schemas (one per OpenAPI model)
  api/                     # One sub-folder per endpoint
    <endpoint>/
      config.yml           # Parameter definitions and JSON mapping
      <HTTP_METHOD>/       # e.g. get/, post/, put/
        payload_1.json     # Auto-generated sample payloads
        ...
```

---

### `create`

Parses an OpenAPI YAML spec and generates everything needed to run flows: JSON model schemas and per-endpoint payload fixtures.

**Usage:**
```bash
tya create openapi.yaml
tya create openapi.yaml --config config-create.yml
```

**What it does:**

1. **Model generation** — Runs `openapi-generator-cli` to produce Go model stubs, then extracts schema information to write one `<ModelName>.json` file per model into `models/`. Already-generated models are reused across endpoints (no duplicates).

2. **Endpoint scaffolding** — For each path + method defined in the spec:
   - Creates `api/<endpoint>/config.yml` describing path/query/header/body parameters and how they map to the payload JSON.
   - Creates `api/<endpoint>/<METHOD>/` with `N` auto-generated payload JSON files (N is controlled by `config-create.yml` or per-endpoint overrides). Payloads are seeded with realistic random data using [`go-faker`](https://github.com/brianvoe/gofakeit), respecting each field's type, format, and constraints from the OpenAPI schema.

---

### `run`

Reads `config-run.yml` and executes the defined flows against live APIs.

**Usage:**
```bash
tya run                    # Execute all flows as configured
tya run -t                 # Test mode: single pass, ignores RPS targets
tya run --flow login-flow  # Execute a specific named flow
```

**Key flag:**

| Flag | Short | Description |
|------|-------|-------------|
| `--test` | `-t` | Dry-run mode. Executes each flow step exactly once. Ignores `requests_per_second`. Useful for verifying flow correctness before a real load run. |

---

## Configuration Files

### `config-create.yml`

Controls payload generation behavior.

```yaml
payloads_per_method: 5          # Default number of payloads generated per endpoint+method

overrides:
  - endpoint: /users
    method: POST
    count: 10                   # Override for a specific endpoint+method
  - endpoint: /orders/{id}
    method: GET
    count: 2
```

---

### `config-run.yml`

Defines the flows TYA will execute. Each flow is an item in the `flows` list.

```yaml
flows:
  - name: full-checkout-flow
    type: end-to-end
    duration: 60s
    requests_per_second: 50
    auth: oauth2-user           # Reference to an auth profile (see Auth section)
    steps:
      - id: register
        endpoint: /users
        method: POST
        payload_strategy: random    # random | fixed | extracted
      - id: login
        endpoint: /auth/token
        method: POST
        payload_strategy: fixed
        payload_file: api/auth/token/post/payload_1.json
      - id: get-catalog
        endpoint: /products
        method: GET
        extract:
          - field: response.body.items[0].id
            as: product_id           # Stored in flow context
      - id: add-to-cart
        endpoint: /cart/items
        method: POST
        payload_strategy: template
        payload_template: |
          { "product_id": "{{ .product_id }}" }
      - id: checkout
        endpoint: /orders
        method: POST
        payload_strategy: template
        payload_template: |
          { "cart_id": "{{ .cart_id }}" }
    children:
      - name: verify-order          # Runs after the load pool drains
        type: alone
        auth: oauth2-user
        steps:
          - id: list-orders
            endpoint: /orders
            method: GET

  - name: smoke-get-users
    type: alone
    duration: 30s
    requests_per_second: 10
    auth: api-key-prod
    depends_on:
      - full-checkout-flow          # Waits for the checkout flow to complete
    steps:
      - endpoint: /users
        method: GET
```

#### Flow Types

| Type | Description |
|------|-------------|
| `end-to-end` | Multi-step flow executed sequentially. Supports data extraction between steps. |
| `alone` | Single endpoint + method, no chaining. |

---

## Flow Dependencies

Flows can declare `depends_on` to express ordering constraints. TYA validates the entire dependency graph at startup — checking that every referenced flow name exists and that there are no cycles — then executes flows in topological order. A flow will not start until every flow it depends on has fully completed (including its wire-flow children).

```yaml
flows:
  - name: seed-data
    type: alone
    duration: 10s
    requests_per_second: 1
    steps:
      - endpoint: /admin/seed
        method: POST

  - name: smoke-tests
    type: end-to-end
    depends_on:
      - seed-data          # Waits for seed-data to finish before starting
    duration: 30s
    requests_per_second: 10
    steps:
      - endpoint: /users
        method: GET
```

**Rules:**
- `depends_on` accepts a list of flow names (strings).
- All listed names must exist in the same `config-run.yml`; TYA exits with an error otherwise.
- Circular dependencies are detected via DFS colouring and cause a startup error.
- Implemented in `pkg/cli_functions/dependency_graph.go` (`ValidateDependencyGraph`, `TopologicalOrder`).

---

## Wire-Flow Children

A flow can declare `children` — a list of wire-flows that run **after** the parent flow's entire goroutine pool has drained. Children are useful for teardown, cleanup, or assertions that must happen once load has stopped but before the parent signals completion to its own dependents.

```yaml
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
        payload_strategy: random
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

**Behaviour:**
- Children run **sequentially** in the order they are listed, after the parent pool fully drains.
- Each child receives the **final flow context** of the last goroutine that completed in the parent pool, so extracted values (IDs, tokens, etc.) are available inside child steps via `{{ .key }}` templates.
- Child step metrics are reported under `children[]` in the JSON report, separate from the parent `steps[]`.
- The parent flow's done-channel is closed only after all children finish, so any flows that `depends_on` the parent correctly wait for teardown to complete.
- Wire-flow children do **not** support `depends_on` or nested children themselves.
- Implemented in `pkg/cli_functions/run_scheduler.go` (`RunScheduler`, `WireFlowExecutorFunc`).

---

## Data Realism in End-to-End Flows

A key challenge in end-to-end flows is making requests coherent — the data passed between steps must be realistic and internally consistent, not just random noise. TYA solves this with a **flow execution context** and **payload strategies**:

### Flow Execution Context

Each goroutine running a flow maintains an isolated key-value context (`map[string]any`) that lives for the duration of that flow execution. It is populated in two ways:

1. **`extract` blocks on steps** — pull values from the previous response (body fields, headers, status code) and store them under a named key.
2. **Auth injection** — tokens and session data from the auth layer are automatically stored in the context (e.g. `access_token`, `user_id`).

### Payload Strategies

Each step can declare one of the following strategies for building its request body:

| Strategy | Description |
|----------|-------------|
| `random` | A random payload from the pre-generated pool in `api/<endpoint>/<METHOD>/`. |
| `fixed` | A specific JSON file from disk (`payload_file`). |
| `template` | A Go `text/template` string rendered against the current flow context. Use `{{ .key }}` to inject extracted values. |
| `extracted` | Uses the full response body from a previous step (identified by `from_step`) as the payload. |

This design means that a step like _"add item to cart"_ can receive the real `product_id` returned by the _"list catalog"_ step — no hardcoding, no mismatched IDs.

### Template Example

```yaml
steps:
  - id: get-product
    endpoint: /products
    method: GET
    extract:
      - field: response.body.data[0].id
        as: product_id
      - field: response.body.data[0].price
        as: product_price

  - id: place-order
    endpoint: /orders
    method: POST
    payload_strategy: template
    payload_template: |
      {
        "product_id": "{{ .product_id }}",
        "quantity": 1,
        "unit_price": {{ .product_price }}
      }
```

---

## Authentication

TYA supports multiple authentication schemes. Auth profiles are defined once in `config-run.yml` and referenced by name in flows.

### Auth Profile Definition

```yaml
auth_profiles:
  - name: oauth2-user
    type: oauth2_password
    token_url: https://api.example.com/auth/token
    client_id: my-client
    client_secret: "${CLIENT_SECRET}"    # Supports env var interpolation
    username: "${TEST_USER}"
    password: "${TEST_PASS}"
    scopes: [read, write]
    inject_as: bearer                    # bearer | header | query
    header_name: Authorization           # default for bearer
    refresh_before_expiry: 30s           # Refresh token this long before it expires

  - name: api-key-prod
    type: api_key
    value: "${API_KEY}"
    inject_as: header
    header_name: X-API-Key

  - name: basic-auth-staging
    type: basic
    username: "${STAGING_USER}"
    password: "${STAGING_PASS}"
```

### Supported Auth Types

| Type | Description |
|------|-------------|
| `oauth2_password` | Full OAuth2 Resource Owner Password flow with token refresh. |
| `oauth2_client_credentials` | Machine-to-machine OAuth2, no user credentials. |
| `api_key` | Static key injected into a header or query parameter. |
| `basic` | HTTP Basic Auth (Base64-encoded `user:pass`). |
| `custom_login` | Logs in via an API endpoint and extracts the token from the response. See below. |

### Custom Login Auth

For APIs that don't follow standard OAuth2 but have their own `/login` endpoint:

```yaml
auth_profiles:
  - name: custom-login
    type: custom_login
    login_endpoint: /auth/login
    method: POST
    payload: |
      { "email": "${TEST_USER}", "password": "${TEST_PASS}" }
    extract_token:
      access_token: response.body.token
      refresh_token: response.body.refresh_token
      expires_in: response.body.expires_in    # seconds
    refresh_endpoint: /auth/refresh
    refresh_method: POST
    refresh_payload: |
      { "refresh_token": "{{ .refresh_token }}" }
    refresh_extract:
      access_token: response.body.token
      expires_in: response.body.expires_in
```

### Token Lifecycle Management

TYA manages token state per goroutine:

- **At flow start**, the runner acquires a token using the referenced auth profile (login request or OAuth2 grant).
- **Before each step**, TYA checks if the token is within `refresh_before_expiry` of expiration. If so, it performs the refresh flow transparently before sending the request.
- **On 401 responses**, TYA can optionally re-authenticate once before failing the step (`retry_on_401: true`).
- **Token isolation** — each goroutine holds its own token, avoiding race conditions in concurrent load tests.

### Using Auth in Flows

```yaml
flows:
  - name: authenticated-flow
    type: end-to-end
    auth: oauth2-user          # Auth profile name
    steps:
      - endpoint: /profile
        method: GET
      - endpoint: /settings
        method: PUT
        payload_strategy: fixed
        payload_file: api/settings/put/payload_1.json
```

The access token is automatically injected into every step's request headers. No manual configuration per step is needed.

---

## Execution Engine

The `run` command uses a goroutine-based load engine to reach the target `requests_per_second`:

- **`requests_per_second` always means HTTP calls/s**, regardless of how many steps a flow has. For a flow with N steps, the engine fires one goroutine every `N / rps` seconds, so the resulting HTTP call rate equals `rps`.
- Each goroutine executes all N steps sequentially (one full flow iteration). After finishing it sleeps a **think-time** remainder so its total slot time equals `N / rps` seconds, self-regulating pace.
- The engine runs in **4 phases**: ramp-up (multiplicative, `factor=1.5` default) → plateau detection (N stable windows) → analysis window (`duration` config applies here) → drain.
- A semaphore caps concurrent goroutines to `ceil((rps/N) × p95_iter_s × 1.5)`, min 8.

### Negative Resets and Forced Plateau

A **negative reset** is any ramp-up window where the observed RPS drops below the previous window's RPS. Resets do **not** need to be consecutive — TYA counts the total. When `max_negative_resets` (default: 3) is reached, TYA forces a plateau immediately:

- Analysis RPS = average of the best `best_windows_avg` (default: 3) stable windows (closest to target).
- Report flags: `forced_plateau: true`, `forced_plateau_reason: "negative_resets"`, `forced_plateau_rps`.
- `ramp_up_windows[]` in the report contains per-window diagnostics with `negative_reset` and `consecutive_negative_resets` flags.

A **timeout** (`max_ramp_duration`, default: 600 s) fires the same forced-plateau logic with `forced_plateau_reason: "timeout"` if no plateau is reached in time.

### JSON Report Fields

- `rps_achieved` — measured **HTTP calls/s** during the analysis window (`totalIterations × N / analysisDuration`)
- `iterations_per_second` — measured **flow iterations/s** (`rps_achieved / N`); useful for goroutine throughput
- `stable_rps_target` / `stable_rps_achieved` — same as above, scoped to the adaptive engine section
- `forced_plateau` / `forced_plateau_reason` / `forced_plateau_rps` — forced-plateau diagnostics
- `negative_resets` — total negative-reset windows observed during ramp-up
- `ramp_up_windows[]` — per-window detail: `target_rps`, `observed_rps`, `variation`, `stable`, `negative_reset`, `consecutive_negative_resets`

At the end of the run, a **JSON report** is written with: p50/p95/p99 latency, total requests, error rate, per-step breakdown, and per-flow summary.

---

## App Example

A minimal but complete REST API written in Go, intended as the canonical TYA demo target. It covers authentication (register, login, token refresh) and a full CRUD for a `Person` resource backed by SQLite. Everything lives in a single file at `cmd/app/main.go`.

### Dependencies

```bash
go mod init app
go get github.com/mattn/go-sqlite3    # SQLite driver (requires CGO)
go get github.com/golang-jwt/jwt/v5   # JWT signing and validation
go get golang.org/x/crypto/bcrypt     # Password hashing
```

### Run

```bash
go run cmd/app/main.go

# With custom settings
PORT=9090 JWT_SECRET=supersecret DB_PATH=./data.db go run cmd/app/main.go
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP listen port |
| `JWT_SECRET` | `change-me-in-prod` | HMAC-SHA256 signing key |
| `DB_PATH` | `./app.db` | SQLite file path |
| `ACCESS_TTL` | `15m` | Access token lifetime |
| `REFRESH_TTL` | `168h` | Refresh token lifetime (7 days) |

### API Reference

#### Auth

| Method | Path | Auth required | Description |
|--------|------|:---:|-------------|
| `POST` | `/auth/register` | — | Create a new user account |
| `POST` | `/auth/login` | — | Obtain access + refresh tokens |
| `POST` | `/auth/refresh` | — | Rotate refresh token, get new pair |

**Register**
```http
POST /auth/register
Content-Type: application/json

{ "email": "alice@example.com", "password": "s3cr3t" }
```
```json
HTTP 201
{ "id": 1, "email": "alice@example.com" }
```

**Login**
```http
POST /auth/login
Content-Type: application/json

{ "email": "alice@example.com", "password": "s3cr3t" }
```
```json
HTTP 200
{
  "access_token":  "eyJ...",
  "refresh_token": "eyJ...",
  "token_type":    "Bearer",
  "expires_in":    900
}
```

**Refresh**
```http
POST /auth/refresh
Content-Type: application/json

{ "refresh_token": "eyJ..." }
```
```json
HTTP 200
{
  "access_token":  "eyJ...",
  "refresh_token": "eyJ...",   ← new token; old one is revoked
  "token_type":    "Bearer",
  "expires_in":    900
}
```

> Refresh tokens are **single-use**. Each `/auth/refresh` call deletes the old token from the DB and issues a new pair (rotation). A stolen refresh token can therefore only be used once before the legitimate client detects the mismatch.

#### Persons (all endpoints require `Authorization: Bearer <access_token>`)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/persons` | List all persons |
| `POST` | `/persons` | Create a person |
| `GET` | `/persons/{id}` | Get a person by ID |
| `PUT` | `/persons/{id}` | Full replace |
| `PATCH` | `/persons/{id}` | Partial update (only sent fields are changed) |
| `DELETE` | `/persons/{id}` | Delete a person |

**Person object**
```json
{
  "id":         1,
  "first_name": "Alice",
  "last_name":  "Smith",
  "email":      "alice@example.com",
  "phone":      "+1-555-0100",
  "birth_date": "1990-06-15",
  "created_at": "2025-01-01T10:00:00Z",
  "updated_at": "2025-01-01T10:00:00Z"
}
```

`phone` and `birth_date` are optional and omitted from responses when null.

**Create**
```http
POST /persons
Authorization: Bearer eyJ...
Content-Type: application/json

{
  "first_name": "Alice",
  "last_name":  "Smith",
  "email":      "alice@example.com",
  "phone":      "+1-555-0100",
  "birth_date": "1990-06-15"
}
```

**Partial update (PATCH)**

Only the fields included in the body are updated; the rest stay unchanged.
```http
PATCH /persons/1
Authorization: Bearer eyJ...
Content-Type: application/json

{ "phone": "+1-555-9999" }
```

### Database Schema

```sql
CREATE TABLE users (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  email      TEXT    NOT NULL UNIQUE,
  password   TEXT    NOT NULL,            -- bcrypt hash
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE refresh_tokens (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token      TEXT    NOT NULL UNIQUE,
  expires_at DATETIME NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE persons (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  first_name TEXT    NOT NULL,
  last_name  TEXT    NOT NULL,
  email      TEXT    NOT NULL UNIQUE,
  phone      TEXT,
  birth_date TEXT,                        -- YYYY-MM-DD
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### Architecture Notes

The entire app is structured around a single `app` struct that bundles the config, the DB handle, and the HTTP mux. Route registration is explicit — no magic, no global state. This makes it straightforward to test handlers in isolation by constructing an `app` with a test DB.

Authentication flow:

```
Client                        Server
  │── POST /auth/login ──────▶│  Verify bcrypt hash
  │◀── access_token (15m) ────│  Sign JWT {kind:"access"}
  │◀── refresh_token (7d) ────│  Sign JWT {kind:"refresh"} + store in DB
  │                            │
  │── GET /persons ───────────▶│  Validate access JWT
  │◀── 200 OK ────────────────│
  │                            │
  │  (access token expires)    │
  │── POST /auth/refresh ──────▶│  Validate refresh JWT
  │                            │  Delete old token from DB
  │◀── new access_token ───────│  Insert new refresh token
  │◀── new refresh_token ──────│
```

### Using This App with TYA

A matching `config-run.yml` for this app looks like:

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
        payload_strategy: random

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
```

