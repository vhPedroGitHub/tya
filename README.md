# TYA — Test Your API

TYA is a CLI tool for testing and load-testing REST APIs. Define flows in a YAML config, generate realistic payloads from your OpenAPI spec, and run single-pass smoke tests or sustained load tests — all from the terminal.

Built with **Go** and **Cobra**.

---

## Why use TYA?

Most API testing tools make you choose between simplicity and power. Simple tools (curl scripts, Postman collections) fall apart the moment you need to chain requests — you can't feed the ID from a `POST /users` response into a `DELETE /users/{id}` without writing glue code. Powerful tools (k6, Gatling, Locust) require you to learn a scripting language or a DSL just to describe what your API already documents in OpenAPI.

TYA takes a different approach:

**Your OpenAPI spec is the source of truth.** Run `tya create openapi.yaml` and TYA generates typed, realistic payloads for every endpoint automatically — no hand-writing JSON fixtures, no placeholder data that breaks your validations.

**Flows are plain YAML, not code.** You describe *what* should happen (chain these endpoints, extract this ID, use it here), and TYA handles *how* — goroutine scheduling, token refresh, RPS pacing, latency collection. A flow that registers a user, logs in, creates a resource, and deletes it is ~20 lines of config.

**The execution model is honest.** `requests_per_second` means exactly that — HTTP calls per second, regardless of how many steps a flow has. The goroutine pool is sized by your latency, not by a pre-configured thread count you have to tune. Results in the JSON report match what you configured.

**Test mode prevents surprises.** `tya run -t` executes every flow exactly once before you commit to a load run, so you catch config mistakes (wrong endpoint, bad payload template, broken auth) in seconds rather than discovering them mid-test.

**It runs everywhere your code runs.** A single static binary with no runtime dependencies. Works in CI, in Docker, on a developer laptop — the same binary, the same config, the same results.

---

## Features

- **OpenAPI-driven payload generation** — point TYA at your OpenAPI v3 spec and it generates realistic, type-aware JSON payloads using `gofakeit`, one file per endpoint + method.
- **End-to-end flows** — chain steps sequentially, extract values from responses (e.g. IDs, tokens), and inject them into later steps via Go templates.
- **Multiple payload strategies** — `random`, `fixed`, `template`, and `extracted` give you full control over what each step sends.
- **Flow dependency graph** — declare `depends_on` between flows; TYA validates the DAG (cycle detection included) and executes flows in topological order.
- **Wire-flow children** — attach teardown/cleanup flows that run after a parent's load pool drains.
- **Authentication** — built-in support for `oauth2_password`, `oauth2_client_credentials`, `api_key`, `basic`, and `custom_login` auth profiles with automatic token refresh.
- **Goroutine-based load engine** — auto-scaling worker pool targets your configured RPS, backs off on diminishing returns, and streams metrics throughout the run.
- **Test mode** — `tya run -t` executes each step exactly once before committing to a full load run.
- **JSON reports** — p50/p90/p95/p99 latency, error rates, per-step breakdowns, and per-flow summaries written to `tya-report-<timestamp>.json`.
- **Demo app** — a fully functional Go REST API (persons CRUD + JWT auth, SQLite) at `cmd/app/main.go` to use as a load-test target out of the box.

---

## Documentation

| Doc | Description |
|-----|-------------|
| [Getting Started](docs/getting-started.md) | Installation, quick start, and first load run |
| [Concepts](docs/concepts.md) | Flows, steps, payload strategies, auth, execution context, config reference |
| [Commands](docs/commands.md) | Full flag reference for `init`, `create`, and `run` |
| [Metrics](docs/metrics.md) | JSON report format and how to interpret results |
| [Docker Deploy](docs/docker.md) | Running TYA via Docker, GHCR image, Compose example |
| [Distributed Testing](docs/distributed.md) | Coordinator node design for multi-machine load tests |

---

## Quick Start

```bash
# Build
go build -o bin/tya ./cmd/tya/cli

# Initialise a project
tya init

# Generate payloads from your OpenAPI spec
tya create openapi.yaml

# Verify your flows (single-pass, no load)
tya run -t

# Run a load test
tya run
```

See [docs/getting-started.md](docs/getting-started.md) for the full walkthrough.

---

## Upcoming Features

### Distributed Load Testing via Coordinator Node

> **Goal:** reach RPS targets (e.g. 1,000,000 req/s) that are not achievable from a single machine by coordinating multiple TYA worker nodes through a dedicated TYA coordinator node.

One TYA instance runs in **coordinator mode** and is the single source of truth for the cluster. Worker nodes do not communicate with each other — they only talk to the coordinator.

**Each worker node**, on every ramp-up window, sends the coordinator two numbers:
- its **current measured RPS**
- its **local RPS target** (the `requests_per_second` defined in its own config)

**The coordinator** keeps a running sum of all workers' reported RPS (the cluster aggregate) and compares it against the global `max_rps` ceiling configured for the run. It then replies to each worker with a simple instruction:
- **`grow`** — the aggregate is below the ceiling; the worker may continue ramping up.
- **`hold`** — the aggregate has reached the ceiling; the worker must stop growing and maintain its current rate.

This design avoids any shared mutable state: the coordinator is the only process writing aggregates, workers are the only processes sending load. There are no concurrent counter updates, no drift, and no external dependencies. If the coordinator goes away workers fall back to their local `requests_per_second` ceiling autonomously.

See [docs/distributed.md](docs/distributed.md) for the detailed design proposal.

---

## License

[MIT](LICENSE)
