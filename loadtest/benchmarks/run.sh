#!/bin/sh
# run.sh — Run all Hyperfoil benchmark flows via jbang.
#
# Prerequisites:
#   - jbang installed and on PATH  (https://www.jbang.dev/download/)
#   - task dev:memory-service running on port 8082
#   - task loadtest:seed completed (loadtest/results/seed-manifest.json present)
#
# Usage (from repo root):
#   sh loadtest/benchmarks/run.sh
#
# Results are written to loadtest/results/ as hyperfoil-<flow>-<timestamp>.json
# A pass/fail SLO summary is printed to stdout at the end.
#
# POSIX-compatible — uses sh, not bash.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
RESULTS_DIR="${REPO_ROOT}/loadtest/results"
BENCHMARKS_DIR="${REPO_ROOT}/loadtest/benchmarks"
TIMESTAMP="$(date +%Y%m%dT%H%M%S)"

# Hyperfoil version and jbang coordinates (pinned for reproducibility)
HF_VERSION="0.27.2"
HF_MAIN="io.hyperfoil.cli.HyperfoilCli"
HF_DEPS="io.hyperfoil:hyperfoil-core:${HF_VERSION},io.hyperfoil:hyperfoil-clustering:${HF_VERSION},io.hyperfoil:hyperfoil-http:${HF_VERSION}"
HF_CLI="io.hyperfoil:hyperfoil-cli:${HF_VERSION}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() {
    printf '[run.sh] %s\n' "$*"
}

die() {
    printf '[run.sh] ERROR: %s\n' "$*" >&2
    exit 1
}

# Run a single Hyperfoil benchmark non-interactively by piping commands to the
# Hyperfoil CLI shell.  Outputs a JSON report to the given path.
run_hyperfoil() {
    YAML="$1"
    REPORT="$2"
    BENCHMARK_NAME="$(basename "${YAML}" .hf.yaml)"

    printf "start-local\nupload %s\nrun %s\nwait-run\nexport --destination=%s\nexit\n" \
        "${YAML}" "${BENCHMARK_NAME}" "${REPORT}" \
        | jbang --deps "${HF_DEPS}" --main "${HF_MAIN}" "${HF_CLI}" 2>/dev/null
}

# ---------------------------------------------------------------------------
# Prerequisite checks
# ---------------------------------------------------------------------------

command -v jbang >/dev/null 2>&1 || die "jbang not found on PATH. Install from https://www.jbang.dev/download/"

MANIFEST="${RESULTS_DIR}/seed-manifest.json"
if [ ! -f "${MANIFEST}" ]; then
    die "Seed manifest not found at ${MANIFEST}. Run 'task loadtest:seed' first."
fi

mkdir -p "${RESULTS_DIR}"

# ---------------------------------------------------------------------------
# Step 1: Generate CSV files from seed manifest
# ---------------------------------------------------------------------------

log "Generating CSV files from seed manifest..."
cd "${REPO_ROOT}"
sh "${BENCHMARKS_DIR}/manifest-to-csv.sh"
log "CSV generation complete."

# ---------------------------------------------------------------------------
# Step 2: Define flows to run
# ---------------------------------------------------------------------------

FLOWS="append-throughput list-conversations list-entries search-conversations list-forks sse-fan-out"

# ---------------------------------------------------------------------------
# Step 3: Run each flow with Hyperfoil via jbang
# ---------------------------------------------------------------------------

PASSED=""
FAILED=""

run_flow() {
    FLOW="$1"
    YAML="${BENCHMARKS_DIR}/${FLOW}.hf.yaml"
    RESULT_JSON="${RESULTS_DIR}/hyperfoil-${FLOW}-${TIMESTAMP}.json"

    if [ ! -f "${YAML}" ]; then
        log "WARNING: ${YAML} not found, skipping."
        FAILED="${FAILED} ${FLOW}(missing)"
        return
    fi

    log "Running flow: ${FLOW} ..."

    if run_hyperfoil "${YAML}" "${RESULT_JSON}"; then
        log "PASS: ${FLOW} — results written to ${RESULT_JSON}"
        PASSED="${PASSED} ${FLOW}"
    else
        log "FAIL: ${FLOW} — SLO violated or error (results may still be in ${RESULT_JSON})"
        FAILED="${FAILED} ${FLOW}"
    fi
}

for FLOW in ${FLOWS}; do
    run_flow "${FLOW}"
done

# ---------------------------------------------------------------------------
# Step 4: Print summary
# ---------------------------------------------------------------------------

printf '\n'
log "========================================"
log "Benchmark run complete — ${TIMESTAMP}"
log "========================================"
if [ -n "${PASSED}" ]; then
    log "PASSED:${PASSED}"
fi
if [ -n "${FAILED}" ]; then
    log "FAILED:${FAILED}"
fi
printf '\n'

if [ -n "${FAILED}" ]; then
    log "One or more flows failed SLO assertions. See result files in ${RESULTS_DIR}/"
    exit 1
fi

log "All flows passed SLO assertions."
exit 0
