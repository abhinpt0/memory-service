//go:build ignore

// manifest-to-csv reads loadtest/results/seed-manifest.json and writes three
// CSV files consumed by Hyperfoil benchmark flows:
//
//   - loadtest/results/conversation-ids.csv          (all seeded conversations)
//   - loadtest/results/long-tail-conversation-ids.csv (conversations with >100 entries)
//   - loadtest/results/fork-root-ids.csv              (fork root conversation IDs)
//
// Usage:
//
//	go run ./loadtest/benchmarks/manifest-to-csv.go [--manifest path] [--results-dir dir]
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

type conversationRecord struct {
	ID              string `json:"id"`
	EntryCount      int    `json:"entryCount"`
	ParticipantType string `json:"participantType"`
}

type forkRecord struct {
	RootID          string `json:"rootId"`
	ForkID          string `json:"forkId"`
	ForkedAtEntryID string `json:"forkedAtEntryId"`
}

type seedManifest struct {
	BaseURL            string               `json:"baseURL"`
	TotalConversations int                  `json:"totalConversations"`
	Conversations      []conversationRecord `json:"conversations"`
	Forks              []forkRecord         `json:"forks"`
}

func main() {
	manifestPath := flag.String("manifest", "loadtest/results/seed-manifest.json", "Path to seed-manifest.json")
	resultsDir := flag.String("results-dir", "loadtest/results", "Directory to write CSV files")
	flag.Parse()

	data, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifest-to-csv: read manifest: %v\n", err)
		os.Exit(1)
	}

	var manifest seedManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		fmt.Fprintf(os.Stderr, "manifest-to-csv: parse manifest: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(*resultsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "manifest-to-csv: mkdir: %v\n", err)
		os.Exit(1)
	}

	// conversation-ids.csv — all seeded conversations
	allPath := *resultsDir + "/conversation-ids.csv"
	if err := writeCSV(allPath, func(w *csv.Writer) error {
		for _, c := range manifest.Conversations {
			if err := w.Write([]string{c.ID}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "manifest-to-csv: write %s: %v\n", allPath, err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %d rows to %s\n", len(manifest.Conversations), allPath)

	// long-tail-conversation-ids.csv — conversations with >100 entries
	var longTail []conversationRecord
	for _, c := range manifest.Conversations {
		if c.EntryCount > 100 {
			longTail = append(longTail, c)
		}
	}
	longPath := *resultsDir + "/long-tail-conversation-ids.csv"
	if err := writeCSV(longPath, func(w *csv.Writer) error {
		for _, c := range longTail {
			if err := w.Write([]string{c.ID}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "manifest-to-csv: write %s: %v\n", longPath, err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %d rows to %s\n", len(longTail), longPath)

	// fork-root-ids.csv — fork root conversation IDs
	forkPath := *resultsDir + "/fork-root-ids.csv"
	if err := writeCSV(forkPath, func(w *csv.Writer) error {
		for _, f := range manifest.Forks {
			if err := w.Write([]string{f.RootID}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "manifest-to-csv: write %s: %v\n", forkPath, err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %d rows to %s\n", len(manifest.Forks), forkPath)
}

func writeCSV(path string, fn func(*csv.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := fn(w); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}
