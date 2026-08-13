# Load and Scale Test Suite

This directory contains the load and scale test suite for the memory service.
It covers three components:

1. **Data Generator** (`generator/`) — seeds a configurable realistic dataset into a running memory service before benchmarks run.
2. **Hyperfoil Benchmarks** (`benchmarks/`) — YAML user-flow definitions that drive sustained load against all target endpoints.
3. **Pagination Correctness** (`correctness/`) — walks every page of every paginated list endpoint and asserts no entries are skipped or duplicated.

Results are written to `results/` as structured JSON/Markdown reports.

---

## Prerequisites

- **`task dev:memory-service` must be running** on port **8082** before executing any load-test task.
- API key: `agent-api-key-1` (configured automatically by the dev stack via `MEMORY_SERVICE_API_KEYS_AGENT`).
- [jbang](https://www.jbang.dev/download/) must be installed and on your `PATH` for Hyperfoil benchmarks (`loadtest:bench`).
- Go 1.25+ for generator and correctness binaries.

Start the dev stack:
```sh
task dev:memory-service
```

---

## Running the Full Suite

```sh
task loadtest:all
```

This runs seed → bench → correctness → report in sequence and writes all output to `loadtest/results/`.

---

## Individual Tasks

### `task loadtest:seed`
Seeds the running memory service with 200 conversations (default). Writes a seed manifest to `loadtest/results/seed-manifest.json` which the correctness and benchmark tasks consume.

```sh
task loadtest:seed
# Pass extra flags after --:
task loadtest:seed -- --total-conversations=50
```

### `task loadtest:bench`
Runs all Hyperfoil benchmark flows against the seeded service via `jbang`. Writes per-flow JSON result files to `loadtest/results/`.

```sh
task loadtest:bench
```

Default SLO targets (see `benchmarks/` YAML files for overrides):
| Flow                  | p99 target | Notes                    |
|-----------------------|-----------|--------------------------|
| append-throughput     | 500 ms    | POST entries             |
| list-conversations    | 300 ms    | GET /v1/conversations    |
| list-entries          | 300 ms    | GET /v1/conversations/{id}/entries |
| search-conversations  | 1000 ms   | POST /v1/conversations/search |
| list-forks            | 300 ms    | GET /v1/conversations/{id}/forks |
| sse-fan-out           | N/A       | 50 concurrent SSE connections |

### `task loadtest:correctness`
Runs the Go pagination correctness tests. Requires a seeded service (run `task loadtest:seed` first).

```sh
task loadtest:correctness
```

### `task loadtest:report`
Aggregates all result files in `loadtest/results/` and produces `loadtest/results/report.md` and `loadtest/results/report.json`.

```sh
task loadtest:report
```

---

## CI Artifact Capture

To capture results as a GitHub Actions artifact, add the following step after the loadtest job:

```yaml
- uses: actions/upload-artifact@v4
  with:
    name: loadtest-results
    path: loadtest/results/
```

---

## Directory Layout

```
loadtest/
├── README.md                  ← this file
├── benchmarks/                ← Hyperfoil YAML flows (Sub-Task 3)
│   ├── shared/http-config.hf.yaml
│   ├── append-throughput.hf.yaml
│   ├── list-conversations.hf.yaml
│   ├── list-entries.hf.yaml
│   ├── search-conversations.hf.yaml
│   ├── list-forks.hf.yaml
│   ├── sse-fan-out.hf.yaml
│   ├── manifest-to-csv.go
│   └── run.sh
├── correctness/               ← Go pagination correctness tests (Sub-Task 4)
│   ├── correctness_test.go
│   └── reporter.go
├── generator/                 ← Go seeder binary (Sub-Task 2)
│   ├── main.go
│   ├── config.go
│   ├── distribution.go
│   └── seeder.go
├── report/                    ← Go aggregator binary (Sub-Task 5)
│   └── main.go
└── results/                   ← gitignored output (*.json, *.md); .gitkeep keeps dir
    └── .gitkeep
```
