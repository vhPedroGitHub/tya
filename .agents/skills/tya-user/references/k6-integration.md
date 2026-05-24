# k6 Integration

TYA can generate [k6](https://k6.io/) JavaScript test scripts from `config-run.yml` and run them with the real k6 binary.

## Requirements

- [k6](https://k6.io/docs/getting-started/installation/) installed on the system and available in `$PATH`
- `config-run.yml` with flow definitions

## Workflow

```bash
# 1. Generate k6 scripts from config-run.yml
tya genk6

# 2. Run the generated scripts
tya runk6s
```

## What tya genk6 produces

Reads each flow in `config-run.yml` and generates one or more `.js` files under a `k6/` directory:

```
k6/
├── <flow-name-1>.js
├── <flow-name-2>.js
└── ...
```

Each script contains:
- The k6 `http` calls for each step
- Auth headers from the auth profile
- Template-rendered payloads and paths
- Extraction logic as k6 variable assignments

## What tya runk6s does

Executes all generated `k6/` scripts sequentially using the system `k6` binary. Equivalent to:

```bash
k6 run k6/<flow-name-1>.js
k6 run k6/<flow-name-2>.js
```

k6 output (metrics, summary) is printed to stdout.

## When to use k6 vs native TYA runner

| Aspect | `tya run` (native) | `tya genk6` + `tya runk6s` |
|--------|-------------------|---------------------------|
| Concurrency model | Goroutine pool per flow | k6 VUs (virtual users) |
| Metrics | JSON report file | k6 stdout summary + optional output |
| Protocol support | HTTP/1.1 | HTTP/1.1 + HTTP/2 |
| gRPC | No | Yes (k6 supports gRPC) |
| Browser testing | No | Yes (k6 browser extension) |
| Setup complexity | No extra deps | Requires k6 binary |
| Flow dependencies | Native `depends_on` | Must be handled manually or via k6 options |
| Payload strategies | All (random, template, extracted, etc.) | Translated subset; complex templates may need manual adjustment |

## Limitations

- **Flow dependencies are NOT translated** — generated scripts run independently. You must orchestrate ordering manually (e.g., shell script, k6 `setup()`/`teardown()`).
- **GlobalBucket** (`globalGet`, `global_list`, `expand`) is a TYA-native concept and may not translate cleanly to k6 shared variables.
- **`depends_on`** and **wire-flow children** have no k6 equivalent.
- **Complex templates** may produce invalid JavaScript string literals; inspect generated scripts before running.

## Debugging generated scripts

```bash
# Inspect the generated JS
cat k6/my-flow.js

# Run a single generated script directly
k6 run k6/my-flow.js
```
