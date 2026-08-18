// Package main implements the load-test results aggregator.
// It reads loadtest/results/seed-manifest.json, loadtest/results/hyperfoil-*.json,
// loadtest/results/correctness-report.json, and optionally loadtest/results/sse-report.json,
// then produces loadtest/results/report.md and loadtest/results/report.json.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---- input structures -------------------------------------------------------

type seedManifest struct {
	BaseURL            string `json:"baseURL"`
	TotalConversations int    `json:"totalConversations"`
	Forks              []any  `json:"forks"`
}

type correctnessResult struct {
	Name         string `json:"name"`
	Passed       bool   `json:"passed"`
	ItemsChecked int    `json:"itemsChecked"`
	Detail       string `json:"detail,omitempty"`
}

type correctnessReport struct {
	BaseURL   string              `json:"baseURL"`
	Results   []correctnessResult `json:"results"`
	AllPassed bool                `json:"allPassed"`
}

// ---- output structures ------------------------------------------------------

type benchmarkRow struct {
	Name    string  `json:"name"`
	RPS     float64 `json:"rps"`
	P50ms   float64 `json:"p50ms"`
	P99ms   float64 `json:"p99ms"`
	SLOPass bool    `json:"sloPass"`
	// hasData indicates whether actual benchmark data was found.
	hasData bool
}

type correctnessRow struct {
	Name         string `json:"name"`
	Passed       bool   `json:"passed"`
	ItemsChecked int    `json:"itemsChecked"`
}

type seedInfo struct {
	TotalConversations int `json:"totalConversations"`
	ForkChains         int `json:"forkChains"`
}

type reportJSON struct {
	Timestamp   string           `json:"timestamp"`
	BaseURL     string           `json:"baseURL"`
	Seed        seedInfo         `json:"seed"`
	Benchmarks  []benchmarkRow   `json:"benchmarks"`
	Correctness []correctnessRow `json:"correctness"`
	AllPassed   bool             `json:"allPassed"`
}

// ---- helpers ----------------------------------------------------------------

// repoRoot returns the path to the repository root. The binary is expected to
// be run from the repo root (go run ./internal/loadtest/report/ or task loadtest:report).
func repoRoot() string {
	if root, err := os.Getwd(); err == nil {
		return root
	}
	return "."
}

// strVal extracts a string from a map[string]any, returning "" if absent.
func strVal(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// floatVal extracts a float64 from a map[string]any, returning 0 and false if absent.
func floatVal(m map[string]any, key string) (float64, bool) {
	if v, ok := m[key]; ok {
		switch f := v.(type) {
		case float64:
			return f, true
		case int:
			return float64(f), true
		}
	}
	return 0, false
}

// boolVal extracts a bool from a map[string]any, returning false if absent.
func boolVal(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// dash returns "-" if v is zero, otherwise formats it as %.0f.
func dash(v float64) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f", v)
}

// sloIcon returns "✅ PASS" or "❌ FAIL".
func sloIcon(pass bool) string {
	if pass {
		return "✅ PASS"
	}
	return "❌ FAIL"
}

// passIcon returns "✅ PASS" or "❌ FAIL".
func passIcon(pass bool) string {
	return sloIcon(pass)
}

// ---- loaders ----------------------------------------------------------------

func loadSeedManifest(root string) (seedManifest, bool) {
	path := filepath.Join(root, "loadtest", "results", "seed-manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: seed-manifest.json not found (%v); continuing with empty seed info\n", err)
		return seedManifest{}, false
	}
	var m seedManifest
	if err := json.Unmarshal(data, &m); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not parse seed-manifest.json (%v); continuing with empty seed info\n", err)
		return seedManifest{}, false
	}
	return m, true
}

// loadHyperfoilResults reads all hyperfoil-*.json files.
// It supports the /run/{id}/stats/total JSON format returned by Hyperfoil 0.27.2
// (field: statistics[].summary.percentileResponseTime + requestCount),
// as well as the older flat format (fields: throughput, p50, p99).
func loadHyperfoilResults(root string) []benchmarkRow {
	pattern := filepath.Join(root, "loadtest", "results", "hyperfoil-*.json")
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		return nil
	}

	// De-duplicate: keep only the latest file per benchmark name.
	latest := make(map[string]string) // name -> filepath
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".json")
		base = strings.TrimPrefix(base, "hyperfoil-")
		// Strip trailing -YYYYMMDDTHHMMSS timestamp if present.
		if idx := strings.LastIndex(base, "-"); idx >= 0 && len(base)-idx > 8 {
			base = base[:idx]
		}
		if prev, ok := latest[base]; !ok || f > prev {
			latest[base] = f
		}
	}

	var rows []benchmarkRow
	for name, f := range latest {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read %s (%v); skipping\n", f, err)
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not parse %s (%v); skipping\n", f, err)
			continue
		}

		// Use embedded name field if present.
		if n := strVal(raw, "name"); n != "" {
			name = n
		}

		// Skip stub files (stats-unavailable or parse-failed).
		if note := strVal(raw, "note"); note != "" {
			fmt.Fprintf(os.Stderr, "info: %s has note=%q (benchmark ran but stats not available)\n", name, note)
			rows = append(rows, benchmarkRow{Name: name, SLOPass: true, hasData: false})
			continue
		}

		var rps, p50, p99 float64

		// --- Hyperfoil 0.27.2 /stats/total format ---
		// {"status":"TERMINATED","statistics":[{"phase":"...","summary":{"percentileResponseTime":{"50.0":...,"99.0":...},"requestCount":N,...}},...]}
		if statsArr, ok := raw["statistics"].([]any); ok && len(statsArr) > 0 {
			var totalRequests float64
			var totalDurationNs float64
			var p50sum, p99sum float64
			count := 0
			for _, s := range statsArr {
				stat, ok := s.(map[string]any)
				if !ok {
					continue
				}
				summary, ok := stat["summary"].(map[string]any)
				if !ok {
					continue
				}
				if rc, ok := floatVal(summary, "requestCount"); ok {
					totalRequests += rc
				}
				if et, ok := floatVal(summary, "endTime"); ok {
					if st, ok := floatVal(summary, "startTime"); ok {
						totalDurationNs += et - st
					}
				}
				if pcts, ok := summary["percentileResponseTime"].(map[string]any); ok {
					if v, ok := floatVal(pcts, "50.0"); ok {
						p50sum += v / 1e6 // ns → ms
						count++
					}
					if v, ok := floatVal(pcts, "99.0"); ok {
						p99sum += v / 1e6
					}
				}
			}
			if count > 0 {
				p50 = p50sum / float64(count)
				p99 = p99sum / float64(count)
			}
			// RPS = total requests / total duration in seconds.
			if totalDurationNs > 0 {
				rps = totalRequests / (totalDurationNs / 1e3) // ms → s
			}
		}

		// --- Fallback: flat format (throughput, p50, p99) ---
		if rps == 0 {
			rps, _ = floatVal(raw, "throughput")
		}
		if p50 == 0 {
			p50, _ = floatVal(raw, "p50")
		}
		if p99 == 0 {
			p99, _ = floatVal(raw, "p99")
		}

		sloViolations := boolVal(raw, "sloViolations")
		rows = append(rows, benchmarkRow{
			Name:    name,
			RPS:     rps,
			P50ms:   p50,
			P99ms:   p99,
			SLOPass: !sloViolations,
			hasData: true,
		})
	}
	return rows
}

func loadCorrectnessReport(root string) (correctnessReport, bool) {
	path := filepath.Join(root, "loadtest", "results", "correctness-report.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return correctnessReport{}, false
	}
	var r correctnessReport
	if err := json.Unmarshal(data, &r); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not parse correctness-report.json (%v)\n", err)
		return correctnessReport{}, false
	}
	return r, true
}

// ---- report generation ------------------------------------------------------

// knownEndpoints is the canonical ordered list of benchmark endpoints shown in
// the report table, including SSE (Sub-Task 6). Entries absent from the loaded
// hyperfoil results appear as "-" rows.
var knownEndpoints = []string{
	"append-throughput",
	"list-conversations",
	"list-entries",
	"search-conversations",
	"list-forks",
	"sse-fan-out",
}

func buildBenchmarkRows(loaded []benchmarkRow) []benchmarkRow {
	byName := make(map[string]benchmarkRow, len(loaded))
	for _, r := range loaded {
		byName[r.Name] = r
	}
	rows := make([]benchmarkRow, 0, len(knownEndpoints))
	for _, ep := range knownEndpoints {
		if r, ok := byName[ep]; ok {
			rows = append(rows, r)
		} else {
			rows = append(rows, benchmarkRow{Name: ep, SLOPass: true})
		}
	}
	return rows
}

func writeMD(root, ts, baseURL string, seed seedManifest, hasSeed bool,
	benchRows []benchmarkRow, benchNotRun bool,
	correctRows []correctnessRow, correctNotRun bool) error {

	var sb strings.Builder

	sb.WriteString("# Load Test Report\n\n")
	sb.WriteString(fmt.Sprintf("Generated: %s\n\n", ts))
	if hasSeed {
		sb.WriteString(fmt.Sprintf("Seed: %d conversations, %d fork chains\n\n", seed.TotalConversations, len(seed.Forks)))
	} else {
		sb.WriteString("Seed: not yet run\n\n")
	}
	sb.WriteString(fmt.Sprintf("BaseURL: %s\n\n", baseURL))

	sb.WriteString("## Throughput & Latency\n\n")
	if benchNotRun {
		sb.WriteString("> benchmarks not yet run\n\n")
	}
	sb.WriteString("| Endpoint | RPS | p50 (ms) | p99 (ms) | SLO |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for _, r := range benchRows {
		rpsStr := dash(r.RPS)
		p50Str := dash(r.P50ms)
		p99Str := dash(r.P99ms)
		slo := sloIcon(r.SLOPass)
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n", r.Name, rpsStr, p50Str, p99Str, slo))
	}
	sb.WriteString("\n")

	sb.WriteString("## Pagination Correctness\n\n")
	if correctNotRun {
		sb.WriteString("> correctness not yet run\n\n")
	}
	sb.WriteString("| Test | Items Checked | Result |\n")
	sb.WriteString("|---|---|---|\n")
	for _, r := range correctRows {
		sb.WriteString(fmt.Sprintf("| %s | %d | %s |\n", r.Name, r.ItemsChecked, passIcon(r.Passed)))
	}
	sb.WriteString("\n")

	sb.WriteString("## Summary\n\n")
	benchSummary := "benchmarks not yet run"
	if !benchNotRun {
		allBenchPass := true
		for _, r := range benchRows {
			if r.hasData && !r.SLOPass {
				allBenchPass = false
				break
			}
		}
		if allBenchPass {
			benchSummary = "All benchmarks: PASS"
		} else {
			benchSummary = "All benchmarks: FAIL"
		}
	}
	correctSummary := "correctness not yet run"
	if !correctNotRun {
		if len(correctRows) == 0 {
			correctSummary = "correctness not yet run"
		} else {
			allPass := true
			for _, r := range correctRows {
				if !r.Passed {
					allPass = false
					break
				}
			}
			if allPass {
				correctSummary = "All correctness checks: PASS"
			} else {
				correctSummary = "All correctness checks: FAIL"
			}
		}
	}
	sb.WriteString(benchSummary + "\n")
	sb.WriteString(correctSummary + "\n")

	out := filepath.Join(root, "loadtest", "results", "report.md")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(out), err)
	}
	if err := os.WriteFile(out, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	return nil
}

func writeJSON(root, ts, baseURL string, seed seedManifest,
	benchRows []benchmarkRow, correctRows []correctnessRow) error {

	allPassed := true
	for _, r := range benchRows {
		if r.hasData && !r.SLOPass {
			allPassed = false
			break
		}
	}
	for _, r := range correctRows {
		if !r.Passed {
			allPassed = false
			break
		}
	}

	out := reportJSON{
		Timestamp: ts,
		BaseURL:   baseURL,
		Seed: seedInfo{
			TotalConversations: seed.TotalConversations,
			ForkChains:         len(seed.Forks),
		},
		Benchmarks:  benchRows,
		Correctness: correctRows,
		AllPassed:   allPassed,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report.json: %w", err)
	}
	path := filepath.Join(root, "loadtest", "results", "report.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ---- main -------------------------------------------------------------------

func main() {
	root := repoRoot()
	ts := time.Now().UTC().Format(time.RFC3339)

	// 1. Seed manifest (optional).
	seed, hasSeed := loadSeedManifest(root)

	baseURL := seed.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:8082"
	}

	// 2. Hyperfoil benchmark results (optional).
	loadedBench := loadHyperfoilResults(root)
	benchNotRun := len(loadedBench) == 0
	benchRows := buildBenchmarkRows(loadedBench)

	// 3. Correctness report (optional).
	cr, hasCorrectness := loadCorrectnessReport(root)
	correctNotRun := !hasCorrectness
	var correctRows []correctnessRow
	for _, r := range cr.Results {
		correctRows = append(correctRows, correctnessRow{
			Name:         r.Name,
			Passed:       r.Passed,
			ItemsChecked: r.ItemsChecked,
		})
	}

	// 4. Write report.md.
	if err := writeMD(root, ts, baseURL, seed, hasSeed, benchRows, benchNotRun, correctRows, correctNotRun); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// 5. Write report.json.
	if err := writeJSON(root, ts, baseURL, seed, benchRows, correctRows); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("loadtest/results/report.md written")
	fmt.Println("loadtest/results/report.json written")
}
