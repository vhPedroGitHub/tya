# Troubleshooting

## Known TYA Issues

### inject_as: bearer with api_key type

**Problem:** Setting `inject_as: bearer` on an `api_key` auth profile sends `X-Api-Key` header instead of `Authorization: Bearer`.

**Workaround:** Use `inject_as: header` + `header_name: Authorization` + `value: "Bearer ${KEY}"`:

```yaml
auth_profiles:
  - name: lago-api
    type: api_key
    value: "Bearer ${LAGO_API_KEY}"
    inject_as: header
    header_name: Authorization
```

---

### expand: true required for array extraction

**Problem:** Extracting an array field (e.g. `GET /customers` → `response.body.customers`) into `global_list` without `expand: true` stores the whole array as a single list item. The iterate flow then builds URLs like `/customers/["cust-1","cust-2"]`.

**Fix:** Always add `expand: true`:

```yaml
extract:
  - field: response.body.customers
    as: customers_list
    global_list: true
    expand: true
```

---

### Global bucket NOT persisted between runs

**Problem:** Data extracted with `global: true` or `global_list: true` only exists for the duration of a single `tya run` invocation. A second run starts with an empty bucket.

**Consequence:** You cannot seed data in one run and clean it up in another independent run. Each config file must be self-contained.

**Workaround for cleanup:** Include cleanup flows in the same config file with `depends_on`:

```yaml
flows:
  - name: seed
    ...
  - name: cleanup
    depends_on:
      - seed
    ...
```
### Iterate flow with scalar vs object items

**Scalar items** (e.g. list of strings):

```yaml
iterate_list: seed-data.customer_external_id
# item_variable: (not needed or defaults to "item")
steps:
  - id: get
    endpoint: /customers/{{ .item }}
```

**Object items** (e.g. list of objects from `expand: true`):

```yaml
iterate_list: fetch-customers.customers_list
item_variable: cust
steps:
  - id: get
    endpoint: /customers/{{ index .cust "external_id" }}
```

Using `{{ .item }}` on object items renders `map[key:value ...]` which produces broken URLs.

---

### Check which headers TYA sends

Start a TCP listener on a port, point `base_url` there, run `tya run -t`:

```bash
# Terminal 1: listen on port 9999
nc -l -p 9999

# config-run.yml
base_url: http://localhost:9999

# Terminal 2:
tya run -t
```

This shows the raw HTTP request including headers, method, path, and body.

---

### Test mode (`tya run -t`)

Always run `tya run -t` before a full load run:

```bash
tya run -t                 # test all flows
tya run -t --flow <name>   # test a single flow
```

Test mode sends exactly **one request per step** (no concurrency). It validates:
- Endpoint paths resolve correctly
- Template functions render
- Auth headers are attached
- Extraction paths exist in responses
- Global bucket writes and reads work

**If test mode fails**, fix the error before running a full load.

---

### Check flow order with --verbose

TYA may have a verbose/debug flag. Check with:

```bash
tya run -t 2>&1 | head -50
```

Look for:
- Auth token acquisition (success/failure)
- Flow dependency resolution order
- Extraction results
- Any error messages or panics

---

### Validate YAML syntax

```bash
python3 -c "import yaml; yaml.safe_load(open('config-run.yml'))" && echo "OK"
```

---

### Common Errors

| Error | Likely Cause |
|-------|-------------|
| `response.body.field not found` | Extraction path doesn't match API response; check response structure |
| `template: ... map has no entry for key "item"` | iterate step using `{{ .item }}` without default variable; add `item_variable` or use correct key |
| `global bucket key not found` | Source flow hasn't populated the bucket; check `depends_on` |
| `connection refused` | API not running or wrong `base_url` |
| `401 Unauthorized` | Invalid API key or wrong auth header format |
| `x-api-key` header sent instead of `Authorization: Bearer` | Using `inject_as: bearer` with `api_key` type; use workaround above |
| `URL contains [" ... "]` | Array extracted without `expand: true`; add it |
| `step-id not found in flow` | `from_step` or expressed value references a step that doesn't exist or hasn't completed yet |

---

## Tips

- **Start simple:** Write one `alone` flow with one step, test it, then add complexity.
- **Use auth profiles for clarity:** Define auth once and reference by name across flows.
- **Name steps with `id:`** to reference them in extraction paths and expressed values.
- **Use `depends_on`** to control flow ordering — iterate flows must wait for their data source.
- **`expand: true` is required** when extracting arrays into `global_list`.
- **`inject_as: header` is required** for `api_key` type to send `Authorization: Bearer`.
- **Global bucket is ephemeral** — design self-contained config files.
- **Run `tya run -t` first** — it catches most config errors without wasting load-test resources.
- **Check the report:** After a run, check `tya-report-<timestamp>.json` for success/failure counts and timings.
