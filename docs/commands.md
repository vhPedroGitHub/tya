# TYA Command Reference

## Global Flags

| Flag | Description |
|------|-------------|
| `--help`, `-h` | Print help for any command |

---

## `tya init`

Initialises a new TYA project in the current directory.

```bash
tya init
tya init --name my-project
```

**What it does:**

1. Checks that Docker is installed and reachable.
2. Checks that Java is available (required for `openapi-generator-cli`).
3. Creates the project scaffold:

```
<project>/
  config-create.yml
  config-run.yml
  models/
  api/
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--name` | `-n` | _(current dir)_ | Project name; creates a sub-directory with this name |

**Exit codes:**

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Docker not found or not running |
| 1 | Java not found |
| 1 | File system error |

---

## `tya create`

Parses an OpenAPI YAML spec and generates JSON model schemas and per-endpoint payload fixtures.

```bash
tya create openapi.yaml
tya create openapi.yaml --config config-create.yml
```

**Arguments:**

| Argument | Required | Description |
|----------|----------|-------------|
| `<spec>` | Yes | Path to an OpenAPI v3 YAML file |

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--config` | `-c` | `config-create.yml` | Path to the create config file |

**What it does:**

1. Validates that the spec file exists.
2. Runs `openapi-generator-cli` via Docker to generate Go model stubs.
3. Parses model stubs and writes one `models/<ModelName>.json` schema file per model. Existing models are reused (no duplicates).
4. For each path + method in the spec:
   - Creates `api/<endpoint>/config.yml` with parameter and mapping definitions.
   - Creates `api/<endpoint>/<METHOD>/payload_N.json` files seeded with realistic fake data via `gofakeit`, respecting field types, formats, and constraints from the schema.
5. The number of payloads per endpoint+method defaults to `payloads_per_method` in `config-create.yml`. Per-endpoint overrides apply.

---

## `tya run`

Reads `config-run.yml` and executes the defined flows.

```bash
tya run
tya run -t
tya run --flow person-lifecycle
tya run --config my-config.yml
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--test` | `-t` | false | Test mode: execute each step exactly once; ignore `requests_per_second` |
| `--flow` | | _(all flows)_ | Execute only the named flow |
| `--config` | `-c` | `config-run.yml` | Path to the run config file |

**Behaviour:**

1. Loads and validates `config-run.yml`.
2. Validates the flow dependency graph (existence checks + cycle detection). Exits with an error if a cycle is found.
3. Resolves `base_url` (from config or `--base-url` override) and injects it into every flow context as `_base_url`.
4. Executes flows in topological order:
   - Flows without `depends_on` start immediately.
   - Flows with `depends_on` block until all dependencies have completed.
   - Each flow runs its goroutine pool for the configured `duration` targeting `requests_per_second`.
5. Writes a JSON report to `tya-report-<unix-timestamp>.json`.

**Test mode (`-t`):**

In test mode TYA executes each step exactly once per flow (one request, no concurrency). `requests_per_second` and `duration` are ignored. This is the recommended way to verify your `config-run.yml` before a full load run.

**Report:**

See [metrics.md](metrics.md) for the full report format.

**Exit codes:**

| Code | Meaning |
|------|---------|
| 0 | Run completed (check report for per-flow error rates) |
| 1 | Config load or validation error |
| 1 | Dependency graph contains a cycle |
| 1 | Named flow (`--flow`) not found |

---

## Configuration Files

### config-create.yml

```yaml
payloads_per_method: 5

overrides:
  - endpoint: /users
    method: POST
    count: 10
```

### config-run.yml

See [concepts.md](concepts.md) for the full reference.
