# Getting Started with TYA

TYA (Test Your API) is a CLI tool for testing and load-testing REST APIs. This guide walks you from installation to your first successful load run.

## Prerequisites

- **Docker** — must be installed and reachable (used by `tya create` to run `openapi-generator-cli`)
- **Java** — required for `openapi-generator-cli`
- **Go 1.22+** — to build from source

## Installation

```bash
git clone <repo>
cd tya
export PATH=$PATH:/usr/local/go/bin
go build -o bin/tya ./cmd/tya/cli
export PATH=$PATH:$(pwd)/bin
```

## Quick Start

### 1. Initialise a project

```bash
mkdir my-project && cd my-project
tya init
```

This scaffolds the project layout and writes default `config-create.yml` and `config-run.yml` files. TYA checks that Docker and Java are available before proceeding.

To name the project explicitly:

```bash
tya init --name my-api-tests
```

### 2. Generate payloads from your OpenAPI spec

```bash
tya create openapi.yaml
```

TYA reads your OpenAPI spec, generates Go model stubs via `openapi-generator-cli`, and writes:

- `models/<ModelName>.json` — one JSON schema per model
- `api/<endpoint>/<METHOD>/payload_N.json` — realistic fake payloads seeded with `gofakeit`

The number of payloads per endpoint+method is controlled by `config-create.yml`.

### 3. Configure your flows

Edit `config-run.yml` to define one or more flows. A minimal example:

```yaml
flows:
  - name: smoke-users
    type: alone
    duration: 30s
    requests_per_second: 10
    steps:
      - endpoint: /users
        method: GET
```

See [concepts.md](concepts.md) for the full flow configuration reference.

### 4. Run in test mode first

```bash
tya run -t
```

Test mode (`-t` / `--test`) executes each flow step exactly once, ignoring `requests_per_second`. Use this to verify your configuration is correct before a real load run.

### 5. Run a load test

```bash
tya run
```

TYA spins up a goroutine-based load engine targeting your configured RPS per flow. On completion it writes a JSON report:

```
tya-report-<timestamp>.json
```

To run a single named flow:

```bash
tya run --flow smoke-users
```

## Using the Demo App

TYA ships with a demo REST API (persons CRUD + JWT auth) you can use to test TYA itself.

```bash
# Build and start the demo app
go build -o bin/app ./cmd/app
bin/app &

# Run against it
tya run -t
```

See [AGENTS.md](../AGENTS.md) for the full demo app API reference and a matching `config-run.yml`.

## Next Steps

- [concepts.md](concepts.md) — flows, payload strategies, auth, execution context
- [commands.md](commands.md) — full flag reference for every command
- [metrics.md](metrics.md) — understanding the JSON report output
