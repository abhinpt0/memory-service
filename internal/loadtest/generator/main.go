package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

// conversationRecord is a single entry in the seed manifest's conversations list.
type conversationRecord struct {
	ID              string `json:"id"`
	OwnerID         string `json:"ownerID"`
	EntryCount      int    `json:"entryCount"`
	ParticipantType string `json:"participantType"`
}

// forkRecord is a single entry in the seed manifest's forks list.
type forkRecord struct {
	RootID          string `json:"rootId"`
	ForkID          string `json:"forkId"`
	ForkedAtEntryID string `json:"forkedAtEntryId"`
}

// seedManifest is the top-level structure written to seed-manifest.json.
type seedManifest struct {
	BaseURL            string               `json:"baseURL"`
	TotalConversations int                  `json:"totalConversations"`
	Conversations      []conversationRecord `json:"conversations"`
	Forks              []forkRecord         `json:"forks"`
}

func main() {
	cfg := parseConfig()

	// Idempotency check: skip if manifest already exists.
	if _, err := os.Stat(cfg.SeedManifestPath); err == nil {
		fmt.Printf("manifest already exists, skipping seed. Delete %s to re-seed.\n", cfg.SeedManifestPath)
		os.Exit(0)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	// --- seed main conversations ---
	type job struct {
		index int
	}

	jobs := make(chan job, cfg.TotalConversations)
	for i := range cfg.TotalConversations {
		jobs <- job{index: i}
	}
	close(jobs)

	var (
		mu            sync.Mutex
		conversations []conversationRecord
		indexQueue    []indexEntryRequest
		seeded        int
	)

	var wg sync.WaitGroup
	for range cfg.WorkerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker has its own rand source to avoid lock contention.
			wr := rand.New(rand.NewSource(time.Now().UnixNano()))

			for j := range jobs {
				title := "load-test-" + uuid.New().String()
				entryCount := EntryCount(wr)
				participantType := ParticipantType(j.index)

				ownerID := fmt.Sprintf("loadtest-user-%d", j.index)
				if participantType == "two-agent" {
					ownerID = fmt.Sprintf("loadtest-agent-%d", j.index)
				}

				convID, err := createConversation(client, cfg, title, ownerID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "ERROR createConversation: %v\n", err)
					continue
				}

				entries, err := seedEntriesWithIndex(client, cfg, wr, convID, ownerID, entryCount, participantType)
				if err != nil {
					fmt.Fprintf(os.Stderr, "ERROR seedEntries conv=%s: %v\n", convID, err)
					continue
				}

				mu.Lock()
				conversations = append(conversations, conversationRecord{
					ID:              convID,
					OwnerID:         ownerID,
					EntryCount:      entryCount,
					ParticipantType: participantType,
				})
				indexQueue = append(indexQueue, entries...)
				seeded++
				fmt.Fprintf(os.Stderr, "Seeded %d/%d conversations\n", seeded, cfg.TotalConversations)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// --- index all seeded entries for search ---
	fmt.Fprintf(os.Stderr, "Indexing %d entries for search...\n", len(indexQueue))
	if err := indexEntries(client, cfg, indexQueue); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: indexEntries failed: %v (search tests may return 0 results)\n", err)
		// Non-fatal: continue writing manifest
	} else {
		fmt.Fprintf(os.Stderr, "Indexing complete.\n")
	}

	// --- seed fork chains ---
	forks, err := seedForkChains(client, cfg, r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR seeding fork chains: %v\n", err)
		// Non-fatal: continue and write manifest with whatever forks succeeded.
	}

	// --- write manifest ---
	manifest := seedManifest{
		BaseURL:            cfg.BaseURL,
		TotalConversations: cfg.TotalConversations,
		Conversations:      conversations,
		Forks:              forks,
	}

	if err := writeManifest(cfg.SeedManifestPath, manifest); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR writing manifest: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Seed complete. %d conversations, %d fork chains written to %s\n",
		len(conversations), len(forks), cfg.SeedManifestPath)
}

// seedEntries appends entryCount USER+AI entry pairs to convID according to
// the participant type. Returns index requests for all appended entries so the
// caller can submit them to POST /v1/conversations/index.
func seedEntriesWithIndex(
	client *http.Client,
	cfg GeneratorConfig,
	r *rand.Rand,
	convID, ownerID string,
	entryCount int,
	participantType string,
) ([]indexEntryRequest, error) {
	var idxReqs []indexEntryRequest
	// entryCount is the number of USER turns; each is followed by an AI reply,
	// so the actual total entries stored is entryCount*2.
	for range entryCount {
		userRole := "USER"
		aiRole := "AI"
		if participantType == "two-agent" {
			userRole = "AI"
			aiRole = "AI"
		}

		userText := EntryText(r)
		entryID, err := appendEntry(client, cfg, convID, ownerID, ownerID, userRole, userText, "", "")
		if err != nil {
			return idxReqs, err
		}
		// Only index USER/first-agent turn entries so search finds conversations.
		idxReqs = append(idxReqs, indexEntryRequest{
			ConversationID: convID,
			EntryID:        entryID,
			IndexedContent: userText,
		})

		aiText := EntryText(r)
		if _, err := appendEntry(client, cfg, convID, ownerID, ownerID, aiRole, aiText, "", ""); err != nil {
			return idxReqs, err
		}
	}
	return idxReqs, nil
}

// seedForkChains creates SeedForkChains() root conversations of 10 entries
// each, then forks each root at its 5th entry.
func seedForkChains(client *http.Client, cfg GeneratorConfig, r *rand.Rand) ([]forkRecord, error) {
	n := SeedForkChains()
	forks := make([]forkRecord, 0, n)

	for range n {
		rootTitle := "load-test-fork-root-" + uuid.New().String()
		rootID, err := createConversation(client, cfg, rootTitle, "loadtest-user-1")
		if err != nil {
			return forks, fmt.Errorf("fork root create: %w", err)
		}

		const rootEntries = 10
		const forkAt = 5 // fork at 5th entry (1-based)

		var forkAtEntryID string
		for i := 1; i <= rootEntries; i++ {
			entryID, err := appendEntry(client, cfg, rootID, "loadtest-user-1", "loadtest-user-1", "USER", EntryText(r), "", "")
			if err != nil {
				return forks, fmt.Errorf("fork root entry %d: %w", i, err)
			}
			if i == forkAt {
				forkAtEntryID = entryID
			}
			// AI reply
			if _, err := appendEntry(client, cfg, rootID, "loadtest-user-1", "loadtest-user-1", "AI", EntryText(r), "", ""); err != nil {
				return forks, fmt.Errorf("fork root AI entry %d: %w", i, err)
			}
		}

		forkID, err := createFork(client, cfg, rootID, forkAtEntryID)
		if err != nil {
			return forks, fmt.Errorf("fork create: %w", err)
		}

		forks = append(forks, forkRecord{
			RootID:          rootID,
			ForkID:          forkID,
			ForkedAtEntryID: forkAtEntryID,
		})
		fmt.Fprintf(os.Stderr, "Seeded fork chain %d/%d\n", len(forks), n)
	}

	return forks, nil
}

// writeManifest serialises manifest as indented JSON to path, creating parent
// directories as needed.
func writeManifest(path string, manifest seedManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
