# config-run.yml — Full Reference

## Auth Profiles

### OAuth2 Password
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

### Custom Login (most common for homegrown APIs)
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

### API Key — Bearer token workaround
```yaml
  - name: lago-api
    type: api_key
    value: "Bearer ${API_KEY}"     # literal "Bearer " prefix required
    inject_as: header
    header_name: Authorization
```

**IMPORTANT:** `inject_as: bearer` does NOT work for `api_key` type — TYA sends `X-Api-Key` instead of `Authorization: Bearer`. Always use `inject_as: header` with `header_name: Authorization` and prefix the value with `"Bearer "`.

### Basic Auth
```yaml
  - name: basic-staging
    type: basic
    username: "${USER}"
    password: "${PASS}"
```

### OAuth2 Client Credentials
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

## Flow Types

| Type | Description |
|------|-------------|
| `end-to-end` | Multi-step, sequential. Supports extraction between steps. |
| `alone` | Single endpoint + method, no chaining. 1 step only. |
| `iterate` | Loops over items from a GlobalBucket list. |

---

## Payload Strategies

| Strategy | When to use |
|----------|-------------|
| `random` | Pick a random pre-generated payload from `api/<endpoint>/<METHOD>/` |
| `fixed` | Use a specific file: add `payload_file: api/.../payload_1.json` |
| `template` | Go template rendered against flow context; add `payload_template: \|` |
| `extracted` | Use full response body from a previous step; add `from_step: <step-id>` |
| `template-json` | Load base JSON + field-level overrides (`payload_overrides`) |

---

## Data Extraction

```yaml
extract:
  - field: response.body.items[0].id
    as: product_id
  - field: response.headers.X-Request-Id
    as: request_id
  - field: response.status
    as: last_status
    global: true           # single value → global bucket (last-write-wins)
    global_list: true      # append to a list in global bucket
    expand: true           # if field is an array, split into individual items
```

---

## GlobalBucket — Sharing Data Between Flows

Set `global: true` or `global_list: true` on an extractor to persist beyond the current goroutine.

### Writing

```yaml
steps:
  - id: create-user
    endpoint: /users
    method: POST
    extract:
      - field: response.body.id
        as: user_id
        global_list: true    # appends to a list across all goroutines
```

### Reading

```yaml
# In iterate flow's iterate_list:
iterate_list: create-users.user_id    # <source-flow-name>.<key>

# In templates:
{{ globalGet "seed-data" "customer_external_id" }}
{{ index .global "seed-data" "customer_external_id" }}
```

### expand: true — Split arrays into individual items

When extracting from a list endpoint (e.g. `GET /resources` returning `{"resources": [...]}`), use `expand: true`:

```yaml
extract:
  - field: response.body.customers
    as: customers_list
    global_list: true
    expand: true    # each element becomes a separate list item
```

**Without** `expand: true`, the entire array is stored as a single item. With it, each element becomes its own entry.

When iterate items are objects, access fields with:
```yaml
endpoint: /customers/{{ index .cust "external_id" }}
```

---

## Template Functions

All template strings support:

| Function | Signature | Description |
|----------|-----------|-------------|
| `uuid` | `uuid` | Random UUID v4 |
| `randomInt` | `randomInt` | Random non-negative integer |
| `randomInt64` | `randomInt64` | Random non-negative int64 |
| `randomDigits` | `randomDigits N` | N random decimal digits |
| `timestamp` | `timestamp` | Unix timestamp in seconds |
| `timestampMs` | `timestampMs` | Unix timestamp in ms |
| `upper` | `upper "hello"` | Converts to upper-case |
| `lower` | `lower "HELLO"` | Converts to lower-case |
| `globalGet` | `globalGet "flow" "key"` | Reads value from global bucket |

Env vars: use `${VAR_NAME}` syntax.

---

## Expressed values in templates

```yaml
# From extraction:
endpoint: /customers/{{ .customer_external_id }}

# From full step response:
endpoint: /customers/{{ .create-customer.response.body.customer.external_id }}

# From global bucket:
endpoint: /customers/{{ globalGet "seed-data" "customer_external_id" }}

# Iterate scalar item:
endpoint: /subscriptions/{{ .item }}

# Iterate object item field:
endpoint: /customers/{{ index .cust "external_id" }}
```

---

## Flow Dependencies

```yaml
flows:
  - name: step-one
    ...
  - name: step-two
    depends_on:
      - step-one          # waits for step-one to complete
    ...
  - name: step-three
    depends_on:
      - step-one          # runs in parallel with step-two
    ...
```

Rules:
- All names in `depends_on` must exist in the same `config-run.yml`.
- Circular dependencies cause a hard error at startup.

---

## Wire-Flow Children

Children run **after** the parent's goroutine pool drains. Useful for teardown.

```yaml
flows:
  - name: person-lifecycle
    type: end-to-end
    steps:
      - id: create-person
        endpoint: /persons
        method: POST
        extract:
          - field: response.body.id
            as: person_id
            global_list: true

    children:
      - name: verify-empty
        type: alone
        steps:
          - id: list-persons
            endpoint: /persons
            method: GET
```

Rules:
- Children run sequentially in listed order.
- Children receive the final flow context of the last completed goroutine.
- Children do NOT support `depends_on` or nested children.
