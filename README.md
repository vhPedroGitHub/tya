# TYA — Test Your API

TYA is a CLI tool for testing and load-testing REST APIs. Define flows in a YAML config, generate realistic payloads from your OpenAPI spec, and run single-pass smoke tests or sustained load tests — all from the terminal.

Built with **Go** and **Cobra**.

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

_Nothing here yet — watch this space._

---

## License

[MIT](LICENSE)
