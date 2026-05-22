---
name: tya-contributor
description: Contributes to the TYA (Test Your API) open-source Go/Cobra CLI project. Knows the full codebase layout, Go conventions, linting rules, CI/CD pipeline, and feature implementation patterns. Use when adding commands, fixing bugs, extending the k6 generator, modifying the adaptive run engine, or working on the demo app.
license: MIT
metadata:
  author: https://github.com/vhPedroGitHub
  version: "1.0.0"
  domain: cli-tooling
  triggers: TYA, tya contributor, tya codebase, tya Go, tya CLI, tya run engine, tya k6 generator, tya commands, tya scheduler
  role: specialist
  scope: implementation
  output-format: code
  related-skills: golang-pro
---

# TYA Contributor

Senior contributor to **TYA (Test Your API)** — a Go/Cobra CLI for API load testing with an adaptive ramp-up engine, flow DAG orchestration, GlobalBucket, iterate flows, and k6 script generation.

Repository: `https://github.com/vhPedroGitHub/tya`

---

## Core Rules

- **Language:** Go. CLI framework: Cobra. Logger: `go.uber.org/zap` — no `fmt.Println` in non-test code.
- **Linting is mandatory** after every Go file change. Run and fix all issues before proceeding:
  ```bash
  cd /opt/tya && golangci-lint run ./...
  ```
  golangci-lint v2.12.2 at `/usr/local/bin/golangci-lint`; config at `/opt/tya/.golangci.yml`.
- **Never chain `pkill`/restart + sleep in one command** — start the demo app separately and poll for readiness.
- All exported functions must have godoc comments.
- No new top-level dependencies without strong justification.

---

## Project Layout

```
cmd/tya/cli/main.go          # CLI entrypoint — all Cobra commands registered here
cmd/app/main.go              # Demo REST API (SQLite, JWT, bcrypt) — canonical test target

pkg/commands/                # One file per CLI command (init.go, create.go, run.go, genk6.go, runk6s.go)
pkg/cli_functions/           # Shared business logic:
  dependency_graph.go        #   ValidateDependencyGraph, TopologicalOrder
  run_scheduler.go           #   GlobalBucket, IterateFlowExecutorFunc, WireFlowExecutorFunc, RunScheduler
pkg/models/                  # Data structs; files named <command>_options.go
pkg/configyml/configyml.go   # YAML config structs + validation + load/write helpers
pkg/k6gen/                   # k6 script generator:
  generator.go               #   GenerateAll, WriteScripts, WriteConfigJSON; setup() reads TYA_GLOBAL_STATE
  auth.go                    #   GenerateAuthSetup + GenerateAuthSetupWithGlobal family
  extracts.go                #   Response extraction; TYA_GLOBAL: sentinel console.log for global/global_list
  payloads.go                #   Payload pool loading; all 5 strategies
  requests.go                #   Step code gen; k6HTTPMethod(); GET/DELETE/other switch
  templates.go               #   JS helpers: uuidv4, randomDigits, navigate, renderTemplate, expandEnv

scripts/
  tya-report-pdf.py          # PDF from TYA JSON report (reportlab)
  tya-k6-report-pdf.py       # PDF from k6 summary JSON

.github/workflows/           # lint.yml, build.yml, release.yml, docker.yml
```

---

## Key Architectural Decisions

### GlobalBucket
- Namespaced by flow name — no cross-flow key collisions.
- `global: true` on an extractor → last-write-wins per key.
- `global_list: true` → appends to a list.
- State is propagated across k6 subprocess boundaries via `-e TYA_GLOBAL_STATE=<json>` env var.

### Cross-flow State in k6 (`runk6s`)
- `extracts.go` emits `console.log('TYA_GLOBAL: ' + JSON.stringify({flow, key, value, list}))` sentinels for global/global_list extractions.
- `runk6s.go` runs k6 with `--log-format json`, captures stderr via `io.MultiWriter(os.Stderr, &stderrBuf)`, parses `TYA_GLOBAL:` sentinels after each flow, and passes the accumulated state as `-e TYA_GLOBAL_STATE=<json>` to downstream flows.
- `generator.go` `setup()` reads `__ENV.TYA_GLOBAL_STATE`; VU injects into `data['flowName.key']`.

### Iterate Flow
- `flow.IterateList` split on `.` → `parts[0]` = source flow name, `parts[1]` = key.
- List read from `data['flowName.key']` in `default()`.
- RPS controls inter-item pace; executed sequentially per item.

### Wire-Flow Children
- Run sequentially after parent goroutine pool drains.
- Receive final flow context of last completed goroutine.
- Done-channel closed only after all children finish.

### Adaptive Run Engine (pkg/commands/run.go)
- `requests_per_second` always means HTTP calls/s regardless of step count.
- 4 phases: ramp-up (factor=1.5) → plateau detection → analysis window → drain.
- Semaphore caps goroutines to `ceil((rps/N) × p95_iter_s × 1.5)`, min 8.
- Negative resets: when total reaches `max_negative_resets` (default 3), forced plateau.
- Timeout: `max_ramp_duration` (default 600s) also triggers forced plateau.

### k6-specific Gotchas
- `http.delete()` is a reserved word in k6 — use `http.del(url, null, { headers })`.
- `open()` is init-time only — ruled out as cross-flow sidecar; env var approach used instead.
- k6 `--log-format json` outputs structured JSON to stderr; parse the `msg` field directly.
- k6 env vars: passed via `--env KEY=VALUE` (not `-e` in the Go flag); `stringToString` type.

---

## Demo App

Single-file REST API at `cmd/app/main.go`. SQLite + JWT + bcrypt. WAL mode, `_busy_timeout=5000`, `SetMaxOpenConns(1)`.

```
POST /auth/register   POST /auth/login   POST /auth/refresh
GET|POST /persons     GET|PUT|PATCH|DELETE /persons/{id}
```

**Start (never chain with kill):**
```bash
# 1. Check if running
pgrep -fa bin/app

# 2. Start if not running
nohup /opt/tya/bin/app > /tmp/app.log 2>&1 &

# 3. Poll until ready
for i in $(seq 1 10); do
  curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/auth/login \
    -d '{}' -H 'Content-Type: application/json' && break
  sleep 1
done
```

Default credentials for tests: `testuser@tya.dev` / `secret123`

---

## CI/CD

- Push protection on `main` — always open a PR, wait for `golangci-lint` CI, then merge.
- Binaries built to `bin/tya` and `bin/app`.
- Go binary: `/usr/local/go/bin/go`; must `export PATH=$PATH:/usr/local/go/bin`.
- `go.mod` declares `go 1.25.0`; Dockerfile uses `golang:1.26-alpine`.

---

## Adding a New Command

1. Create `pkg/commands/<name>.go` with a `New<Name>Cmd()` function returning `*cobra.Command`.
2. Add options struct to `pkg/models/<name>_options.go` if needed.
3. Add YAML config struct to `pkg/configyml/configyml.go` if the command has a config file.
4. Register the command in `cmd/tya/cli/main.go`.
5. Run `golangci-lint run ./...` — fix all issues.
6. Build: `go build -o bin/tya ./cmd/tya/cli/`.
7. Open a PR; do not push directly to `main`.
