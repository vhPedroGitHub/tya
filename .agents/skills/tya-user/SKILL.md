---
name: tya-user
description: Helps configure and use TYA (Test Your API) — a CLI load-testing tool. Knows how to write config-run.yml flows, config-create.yml, auth profiles, payload strategies, data extraction, flow dependencies, GlobalBucket, iterate flows, wire-flow children, and k6 script generation (tya genk6 / tya runk6s). Use when setting up a TYA project, writing flows against a real API, or debugging a run.
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
tya genk6                         # Generate k6 scripts
tya runk6s                        # Run generated k6 scripts (requires k6 binary)
```

### k6 commands

```bash
tya genk6                         # Generate k6/ scripts from config-run.yml
tya runk6s                        # Execute all k6 scripts via system k6 binary
```

See `references/k6-integration.md` for full details, limitations, and debugging.
```

---

## Project Structure After `tya init`

```
my-project/
  config-create.yml    # Payload generation settings
  config-run.yml       # Flow definitions
  models/              # JSON schemas (one per OpenAPI model)
  api/
    <endpoint>/
      config.yml
      <METHOD>/
        payload_1.json
        ...
```

---

## Sub-documents

| File | Contents |
|------|----------|
| `references/config-reference.md` | Auth profiles, flow types, steps, payload strategies, extraction, GlobalBucket, templates, dependencies, wire-flow children |
| `references/patterns.md` | CRUD lifecycle, seed + load test, fetch + iterate with `expand: true`, flow dependencies |
| `references/troubleshooting.md` | Known TYA issues, debugging, tips |
| `references/k6-integration.md` | `tya genk6` / `tya runk6s`, requirements, workflow, limitations |

---

## Quick Reference

### Commands

```bash
tya init                          # Scaffold
tya create openapi.yaml           # Generate payloads
tya run -t                        # Test mode (single pass)
tya run                           # Full load
tya run --flow <name>             # Single named flow
tya run --config <file>           # Specific config file
```

### Config-run.yml skeleton

```yaml
base_url: http://localhost:8080

auth_profiles:
  - name: my-auth
    type: api_key | basic | oauth2_password | oauth2_client_credentials | custom_login

flows:
  - name: my-flow
    type: end-to-end | alone | iterate
    duration: 60s
    requests_per_second: 20
    auth: my-auth
    depends_on:
      - other-flow
    children:
      - name: cleanup
        type: alone
        steps:
          - endpoint: /cleanup
            method: POST
    steps:
      - id: step-1
        endpoint: /resources
        method: POST
        payload_strategy: random | fixed | template | extracted | template-json
        extract:
          - field: response.body.id
            as: resource_id
            global: true
            global_list: true
            expand: true
```

### Template functions

| Function | Description |
|----------|-------------|
| `uuid` | Random UUID v4 |
| `randomInt` | Random non-negative integer |
| `randomInt64` | Random int64 |
| `randomDigits N` | N random decimal digits |
| `timestamp` | Unix timestamp (seconds) |
| `timestampMs` | Unix timestamp (ms) |
| `upper` / `lower` | String case conversion |
| `globalGet "flow" "key"` | Read from global bucket |
