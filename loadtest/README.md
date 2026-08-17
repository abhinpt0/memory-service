# Load and Scale Test Suite

This suite validates the memory service at realistic volume: it seeds a configurable dataset,
drives sustained HTTP load via Hyperfoil benchmarks, exhaustively walks every paginated endpoint
to check for skipped or duplicate entries, and aggregates all results into a Markdown/JSON report.
Results are written to `loadtest/results/` and are suitable for capture as CI artifacts.

---

## Prerequisites

- **`task dev:memory-service` must be running** on port **8082** before executing any load-test task.
- API key: `agent-api-key-1` (configured automatically by the dev stack via `MEMORY_SERVICE_API_KEYS_AGENT`).
- [jbang](https://www.jbang.dev/download/) must be installed and on your `PATH` for Hyperfoil benchmarks (`loadtest:bench`).
- Go 1.21+ for the generator, correctness, and report binaries.

Start the dev stack first:

```sh
task dev:memory-service
```

---

## Quick Start

Run the full pipeline in one command:

```sh
task loadtest:all
```

This executes **seed → bench → correctness → report** in sequence and writes all output to `loadtest/results/`.

---

## Individual Tasks

### `task loadtest:seed`

Seeds the running memory service with 200 conversations (default). Writes
`loadtest/results/seed-manifest.json` which the benchmark and correctness tasks consume.
Re-running is a no-op if the manifest already exists (idempotent).

```sh
task loadtest:seed
# Override conversation count:
task loadtest:seed -- --total-conversations=50
```

### `task loadtest:bench`

Runs all Hyperfoil benchmark flows against the seeded service via `jbang`.
Writes per-flow JSON result files to `loadtest/results/hyperfoil-*.json`.

```sh
task loadtest:bench
```

Requires `loadtest:seed` to have completed first.

### `task loadtest:correctness`

Runs the Go pagination correctness tests. Exhaustively walks every paginated
endpoint and asserts no entries are skipped or duplicated.

```sh
task loadtest:correctness
```

Requires `loadtest:seed` to have completed first.

### `task loadtest:report`

Reads all JSON result files from `loadtest/results/` and produces:
- `loadtest/results/report.md` — Markdown table of throughput, latency, and correctness
- `loadtest/results/report.json` — machine-readable version of the same data

```sh
task loadtest:report
```

The report binary exits 0 even if result files are absent (partial report with "not yet run" notes).
It exits 1 only if it cannot write the output files.

---

## SLO Defaults

| Endpoint | p99 SLO |
|---|---|
| append-throughput | 500 ms |
| list-conversations | 300 ms |
| list-entries | 300 ms |
| search-conversations | 1000 ms |
| list-forks | 300 ms |
| sse-fan-out | N/A (connection SLO) |

Override thresholds by editing the corresponding `loadtest/benchmarks/*.hf.yaml` file.

---

## Results

All output is written to `loadtest/results/` (gitignored except `.gitkeep`):

| File | Written by | Description |
|---|---|---|
| `seed-manifest.json` | `loadtest:seed` | All seeded conversation IDs, entry counts, fork chains |
| `conversation-ids.csv` | `loadtest:bench` (via manifest-to-csv helper) | Flat list consumed by Hyperfoil |
| `hyperfoil-<name>-<timestamp>.json` | `loadtest:bench` | Raw Hyperfoil result per flow |
| `correctness-report.json` | `loadtest:correctness` | Per-test pass/fail and item counts |
| `report.md` | `loadtest:report` | Human-readable Markdown summary |
| `report.json` | `loadtest:report` | Machine-readable aggregate report |

---

## CI Artifact Capture

Add the following steps to your GitHub Actions workflow to run the suite and retain all results:

```yaml
- name: Run load tests
  run: task loadtest:all

- uses: actions/upload-artifact@v4
  with:
    name: loadtest-results
    path: loadtest/results/
    if-no-files-found: warn
```

---

## Directory Layout

```
loadtest/                          ← non-Go assets
├── README.md                      ← this file
├── benchmarks/
│   ├── shared/
│   │   └── http-config.hf.yaml   ← shared connection pool config
│   ├── append-throughput.hf.yaml
│   ├── list-conversations.hf.yaml
│   ├── list-entries.hf.yaml
│   ├── search-conversations.hf.yaml
│   ├── list-forks.hf.yaml
│   ├── sse-fan-out.hf.yaml        ← Sub-Task 6
│   ├── manifest-to-csv.sh         ← shell wrapper for the Go CSV helper
│   └── run.sh                     ← runs all flows via jbang
└── results/
    └── .gitkeep                   ← keeps dir in git; *.json and *.md are gitignored

internal/loadtest/                 ← all Go source
├── generator/                     ← data seeder binary (Sub-Task 2)
│   ├── main.go
│   ├── config.go
│   ├── distribution.go
│   └── seeder.go
├── correctness/                   ← pagination correctness tests (Sub-Task 4)
│   ├── correctness_test.go
│   └── reporter.go
├── report/                        ← results aggregator binary (Sub-Task 5)
│   └── main.go
└── sse/                           ← fallback only if Hyperfoil SSE proves unworkable
    └── sse_test.go
```
