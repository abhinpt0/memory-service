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

				// Assign owners from a small fixed pool so each user owns many
				// conversations. This ensures list and search pagination is
				// exercised in correctness tests and benchmarks.
				// Pool: loadtest-user-1..5 for single/two-user, loadtest-agent-1..5 for two-agent.
				poolIdx := j.index%5 + 1
				ownerID := fmt.Sprintf("loadtest-user-%d", poolIdx)
				if participantType == "two-agent" {
					ownerID = fmt.Sprintf("loadtest-agent-%d", poolIdx)
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
	fmt.Fprintf(os.Stderr, "Indexing %d entries for search (batch size %d)...\n", len(indexQueue), cfg.IndexBatchSize)
	if err := indexEntries(client, cfg, indexQueue, cfg.IndexBatchSize); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: indexEntries failed: %v (search tests may return 0 results)\n", err)
		// Non-fatal: continue writing manifest
	} else {
		fmt.Fprintf(os.Stderr, "Indexing complete.\n")
	}

	// --- seed fork chains ---
	forks, err := seedForkChains(client, cfg, r, cfg.ForkChains)
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

// seedEntriesWithIndex appends entryCount entry pairs to convID according to
// the participant type. Returns index requests for all appended entries so the
// caller can submit them to POST /v1/conversations/index.
//
// Participant types:
//   - "single-user": USER+AI turns; both authenticated as ownerID
//   - "two-user":    USER+USER turns; first as ownerID, second as ownerID+"2"
//   - "two-agent":   AI+AI turns;   first as ownerID, second as ownerID+"2"
//
// For two-user and two-agent, the second participant is granted writer access
// on the conversation before any entries are appended, so their X-User-ID is
// authorised to write.
func seedEntriesWithIndex(
	client *http.Client,
	cfg GeneratorConfig,
	r *rand.Rand,
	convID, ownerID string,
	entryCount int,
	participantType string,
) ([]indexEntryRequest, error) {
	var idxReqs []indexEntryRequest

	// Derive a second participant ID for two-user / two-agent conversations.
	// Convention: append "2" to the owner ID (e.g. "loadtest-user-12" or
	// "loadtest-agent-12") so the second participant is distinct but stable.
	secondParticipant := ownerID + "2"

	// Grant the second participant writer access before appending entries on
	// their behalf. This is only needed for multi-identity participant types.
	if participantType == "two-user" || participantType == "two-agent" {
		if err := shareConversation(client, cfg, ownerID, convID, secondParticipant, "writer"); err != nil {
			return idxReqs, fmt.Errorf("shareConversation: %w", err)
		}
	}

	// entryCount is the number of first-turn exchanges; each is followed by a
	// second-turn reply, so the total entries stored is entryCount*2.
	for range entryCount {
		var firstRole, secondRole, firstUserID, secondUserID string
		switch participantType {
		case "two-agent":
			firstRole, secondRole = "AI", "AI"
			firstUserID, secondUserID = ownerID, secondParticipant
		case "two-user":
			firstRole, secondRole = "USER", "USER"
			firstUserID, secondUserID = ownerID, secondParticipant
		default: // "single-user"
			firstRole, secondRole = "USER", "AI"
			firstUserID, secondUserID = ownerID, ownerID
		}

		userText := EntryText(r)
		// Authenticate as firstUserID — X-User-ID must match userId in payload.
		entryID, err := appendEntry(client, cfg, convID, firstUserID, firstUserID, firstRole, userText, "", "")
		if err != nil {
			return idxReqs, err
		}
		// Only index first-turn entries so search finds conversations.
		idxReqs = append(idxReqs, indexEntryRequest{
			ConversationID: convID,
			EntryID:        entryID,
			IndexedContent: userText,
		})

		aiText := EntryText(r)
		// Authenticate as secondUserID — X-User-ID must match userId in payload.
		if _, err := appendEntry(client, cfg, convID, secondUserID, secondUserID, secondRole, aiText, "", ""); err != nil {
			return idxReqs, err
		}
	}
	return idxReqs, nil
}

// seedForkChains creates n root conversations of 10 entries each, then forks
// each root at its 5th entry. n is controlled by the --fork-chains flag.
func seedForkChains(client *http.Client, cfg GeneratorConfig, r *rand.Rand, n int) ([]forkRecord, error) {
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
