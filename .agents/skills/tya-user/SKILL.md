---
name: tya-user
description: Helps configure and use TYA (Test Your API) — a CLI load-testing tool. Knows how to write config-run.yml flows, config-create.yml, auth profiles, payload strategies, data extraction, flow dependencies, GlobalBucket, iterate flows, and wire-flow children. Use when setting up a TYA project, writing flows against a real API, or debugging a run.
license: MIT
metadata:
  author: https://github.com/vhPedroGitHub
  version: "1.0.0"
  domain: testing
  triggers: TYA, tya config, tya flow, tya run, tya create, tya init, load test config, config-run.yml, API load testing TYA
  role: specialist
  scope: configuration
  output-format: yaml
  related-skills: golang-pro
---

# TYA User

Expert in configuring and running **TYA (Test Your API)** — a Go CLI for API load testing.

Install TYA: https://github.com/vhPedroGitHub/tya

---

## Quick Start

```bash
tya init --name my-project        # Scaffold project structure
tya create openapi.yaml           # Generate payloads from OpenAPI spec
tya run -t                        # Test mode: one pass, verify flows
tya run                           # Full load run
tya genk6                         # Generate k6 scripts from config-run.yml
tya runk6s                        # Run generated k6 scripts in order
```

---

## Project Structure After `tya init`

```
my-project/
  config-create.yml    # Payload generation settings
  config-run.yml       # Flow definitions
  models/              # Generated JSON schemas (one per OpenAPI model)
  api/
    <endpoint>/
      config.yml
      <METHOD>/
        payload_1.json
        ...
```

---

## config-create.yml

Controls how many payloads are generated per endpoint+method.

```yaml
payloads_per_method: 5

overrides:
  - endpoint: /users
    method: POST
    count: 10
```

---

## config-run.yml — Full Reference

### Top-Level Structure

```yaml
auth_profiles:    # (optional) Named auth configs
  - name: ...

flows:
  - name: ...
```

---

### Auth Profiles

#### OAuth2 Password
```yaml
auth_profiles:
  - name: my-oauth2
    type: oauth2_password
    token_url: https://api.example.com/auth/token
    client_id: my-client
    client_secret: "${CLIENT_SECRET}"
    username: "${TEST_USER}"
    password: "${TEST_PASS}"
    scopes: [read, write]
    inject_as: bearer
    refresh_before_expiry: 30s
```

#### Custom Login (most common for homegrown APIs)
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

#### API Key
```yaml
  - name: api-key-prod
    type: api_key
    value: "${API_KEY}"
    inject_as: header
    header_name: X-API-Key
```

#### Basic Auth
```yaml
  - name: basic-staging
    type: basic
    username: "${USER}"
    password: "${PASS}"
```

#### OAuth2 Client Credentials
```yaml
  - name: m2m
    type: oauth2_client_credentials
    token_url: https://api.example.com/oauth/token
    client_id: "${CLIENT_ID}"
    client_secret: "${CLIENT_SECRET}"
    scopes: [api]
    inject_as: bearer
```

---

### Flow Types

| Type | Description |
|------|-------------|
| `end-to-end` | Multi-step, sequential. Supports extraction between steps. |
| `alone` | Single endpoint + method, no chaining. |
| `iterate` | Loops over a list extracted from a previous flow's GlobalBucket. |

---

### end-to-end Flow

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
        payload_strategy: random        # random | fixed | template | extracted

      - id: get-person
        endpoint: /persons/{{ .create-person.response.body.id }}
        method: GET
        extract:
          - field: response.body.email
            as: person_email

      - id: patch-phone
        endpoint: /persons/{{ .create-person.response.body.id }}
        method: PATCH
        payload_strategy: template
        payload_template: |
          { "phone": "+1-555-0100" }

      - id: delete-person
        endpoint: /persons/{{ .create-person.response.body.id }}
        method: DELETE
```

---

### Payload Strategies

| Strategy | When to use |
|----------|-------------|
| `random` | Pick a random pre-generated payload from `api/<endpoint>/<METHOD>/` |
| `fixed` | Use a specific file: add `payload_file: api/.../payload_1.json` |
| `template` | Go template rendered against flow context; add `payload_template: \|` |
| `extracted` | Use full response body from a previous step; add `from_step: <step-id>` |

---

### Data Extraction

```yaml
extract:
  - field: response.body.items[0].id
    as: product_id
  - field: response.headers.X-Request-Id
    as: request_id
  - field: response.status
    as: last_status
```

Use extracted values in later steps via `{{ .product_id }}`.

---

### GlobalBucket — Sharing Data Between Flows

Use `global: true` or `global_list: true` on an extractor to persist a value beyond the current goroutine:

```yaml
steps:
  - id: create-user
    endpoint: /users
    method: POST
    payload_strategy: random
    extract:
      - field: response.body.id
        as: user_id
        global_list: true    # appends to a list shared across all goroutines
```

In a downstream flow, reference it via `iterate_list`:

```yaml
  - name: delete-all-users
    type: iterate
    auth: app-user
    iterate_list: create-users.user_id    # <source-flow-name>.<key>
    item_variable: uid
    steps:
      - id: delete-user
        endpoint: /users/{{ .uid }}
        method: DELETE
```

---

### Flow Dependencies

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
      - seed-data          # waits for seed-data to fully complete
    duration: 30s
    requests_per_second: 10
    steps:
      - endpoint: /users
        method: GET
```

Rules:
- All names in `depends_on` must exist in the same `config-run.yml`.
- Circular dependencies are detected at startup and cause a hard error.

---

### Wire-Flow Children

Children run **after** the parent's goroutine pool drains. Useful for teardown or assertions.

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
        extract:
          - field: response.body.id
            as: person_id
            global_list: true

    children:
      - name: verify-empty
        type: alone
        auth: app-user
        steps:
          - id: list-persons
            endpoint: /persons
            method: GET
```

- Children run sequentially in listed order.
- Children receive the final flow context of the last completed goroutine.
- Children do not support `depends_on` or nested children.

---

## Common Patterns

### Full CRUD lifecycle
```yaml
flows:
  - name: crud-flow
    type: end-to-end
    duration: 60s
    requests_per_second: 10
    auth: app-user
    steps:
      - id: create
        endpoint: /resources
        method: POST
        payload_strategy: random
      - id: read
        endpoint: /resources/{{ .create.response.body.id }}
        method: GET
      - id: update
        endpoint: /resources/{{ .create.response.body.id }}
        method: PUT
        payload_strategy: random
      - id: delete
        endpoint: /resources/{{ .create.response.body.id }}
        method: DELETE
```

### Seed then load test
```yaml
flows:
  - name: seed-users
    type: end-to-end
    duration: 10s
    requests_per_second: 5
    auth: app-user
    steps:
      - id: create-user
        endpoint: /users
        method: POST
        payload_strategy: random
        extract:
          - field: response.body.id
            as: user_id
            global_list: true

  - name: stress-read-users
    type: alone
    depends_on: [seed-users]
    duration: 120s
    requests_per_second: 100
    auth: app-user
    steps:
      - endpoint: /users
        method: GET
```

---

## Tips

- Use `tya run -t` (test mode) first — single pass, no RPS targets — to verify flows are correct before a real load run.
- Use `--flow <name>` to run a single named flow.
- Env vars in `config-run.yml` use `"${VAR_NAME}"` syntax and are expanded at runtime.
- 404 errors in delete/cleanup flows after a full run are expected and normal — the resource was already deleted.
- For APIs with single-use refresh tokens, each goroutine holds its own token; no cross-goroutine sharing.
