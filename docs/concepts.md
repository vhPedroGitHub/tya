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
    children:              # Wire-flows executed after this flow drains
      - name: cleanup
        type: alone
        auth: my-auth-profile
        steps:
          - endpoint: /cleanup
            method: POST
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

### Wire-Flow Children

`children` are flows that run **after** the parent flow's goroutine pool has fully drained. They are useful for teardown or cleanup that must happen once load has stopped but before the overall run ends. Children run sequentially after the parent and share the final flow context of the last completed goroutine.

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

The `run` command uses a goroutine-based load engine:

- A controller goroutine manages a pool of worker goroutines.
- Workers stream metrics (latency, status code, errors) to the controller via channels.
- The controller auto-scales workers to hit the target RPS, backing off on diminishing returns or resource pressure.
- Flows with `depends_on` block until their dependencies close their done-channels.
- Wire-flow children run sequentially after the parent pool drains.

See [metrics.md](metrics.md) for the report format produced at the end of a run.

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
    children: [...]
    steps:
      - id: ...
        endpoint: /path
        method: GET | POST | PUT | PATCH | DELETE
        payload_strategy: random | fixed | template | extracted
        payload_file: ...        # for fixed
        payload_template: |      # for template
          { ... }
        from_step: ...           # for extracted
        extract:
          - field: response.body.some.path
            as: my_key
```
