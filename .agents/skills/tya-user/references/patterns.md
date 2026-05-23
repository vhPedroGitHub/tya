# Patterns

## Pattern: CRUD Lifecycle (create + collect + cleanup)

```yaml
flows:
  - name: seed-data
    type: end-to-end
    duration: 30s
    requests_per_second: 5
    steps:
      - id: create
        endpoint: /resources
        method: POST
        payload_strategy: template
        payload_template: |
          {"name": "test-{{ uuid }}"}
        extract:
          - field: response.body.id
            as: resource_id
            global_list: true

  - name: cleanup
    type: iterate
    iterate_list: seed-data.resource_id
    depends_on:
      - seed-data
    steps:
      - id: delete
        endpoint: /resources/{{ .item }}
        method: DELETE
```

---

## Pattern: Fetch Existing Resources + Iterate Lifecycle (expand: true)

```yaml
flows:
  - name: fetch-items
    type: alone
    duration: 1s
    requests_per_second: 1
    steps:
      - id: list
        endpoint: /items?per_page=100
        method: GET
        extract:
          - field: response.body.items
            as: items_list
            global_list: true
            expand: true

  - name: process-items
    type: iterate
    iterate_list: fetch-items.items_list
    item_variable: item
    depends_on:
      - fetch-items
    steps:
      - id: get
        endpoint: /items/{{ .item }}
        method: GET
      - id: delete
        endpoint: /items/{{ .item }}
        method: DELETE
```

**When iterate items are objects**, use `index`:

```yaml
item_variable: cust
steps:
  - id: get
    endpoint: /customers/{{ index .cust "external_id" }}
    method: GET
```

---

## Pattern: Seed Data + Load Test (Minimal Seed Overhead)

```yaml
flows:
  - name: seed-customers
    type: alone
    duration: 10s
    requests_per_second: 20
    steps:
      - id: create
        endpoint: /customers
        method: POST
        payload_strategy: template
        payload_template: |
          {
            "customer": {
              "external_id": "load-{{ uuid }}",
              "name": "Load User {{ randomInt }}",
              "country": "US"
            }
          }
        extract:
          - field: response.body.customer.external_id
            as: customer_external_id
            global_list: true

  - name: bill-customers
    type: iterate
    iterate_list: seed-customers.customer_external_id
    depends_on:
      - seed-customers
    duration: 60s
    requests_per_second: 5
    steps:
      - id: create-subscription
        endpoint: /subscriptions
        method: POST
        payload_strategy: template
        payload_template: |
          {
            "subscription": {
              "external_customer_id": "{{ .item }}",
              "plan_code": "standard"
            }
          }
```

---

## Pattern: Flow Dependencies (Parallel After Barrier)

```yaml
flows:
  - name: setup
    type: alone
    duration: 5s
    requests_per_second: 10
    steps:
      - id: create-config
        endpoint: /config
        method: POST
        ...

  - name: load-test-a
    type: end-to-end
    depends_on:
      - setup
    ...

  - name: load-test-b
    type: end-to-end
    depends_on:
      - setup          # runs in parallel with load-test-a
    ...
```

---

## Pattern: Wire-Flow Children (Cleanup After Pool Drains)

```yaml
flows:
  - name: test-lifecycle
    type: end-to-end
    steps:
      - id: create
        endpoint: /resources
        method: POST
        extract:
          - field: response.body.id
            as: resource_id
            global_list: true

    children:
      - name: run-after-all-goroutines
        type: alone
        steps:
          - id: verify
            endpoint: /resources
            method: GET
```

Children run **after** all parent goroutines complete. They do NOT support `depends_on` or nested children.

---

## Pattern: Split Config Files by Concern

```yaml
# config-seed.yml        → create resources only
# config-run.yml         → full pipeline (seed → load → cleanup)
# config-cleanup.yml     → delete/teardown only
```

Run specific configs:

```bash
tya run --config config-seed.yml
tya run --config config-run.yml
```

---

## Pattern: template-json with payload_overrides

```yaml
steps:
  - id: create-customer
    endpoint: /customers
    method: POST
    payload_strategy: template-json
    payload_overrides:
      customer.external_id: "cust-{{ uuid }}"
      customer.name: "User {{ randomInt }}"
      customer.country: US
```

Uses a base JSON file from `api/<endpoint>/<METHOD>/` (like `random`) but applies template-based field overrides on top. Supports dot-notation paths for nested fields.
