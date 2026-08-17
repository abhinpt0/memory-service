package correctness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ---------------------------------------------------------------------------
// Manifest types — must match exactly what the generator writes.
// ---------------------------------------------------------------------------

type conversationRecord struct {
	ID              string `json:"id"`
	OwnerID         string `json:"ownerID"`
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

// ---------------------------------------------------------------------------
// Package-level state shared across tests.
// ---------------------------------------------------------------------------

var (
	manifest *seedManifest
	baseURL  string
	apiKey   string
	userID   string
)

// TestMain loads the seed manifest once and shares it across all tests.
func TestMain(m *testing.M) {
	baseURL = os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8082"
	}
	apiKey = os.Getenv("API_KEY")
	if apiKey == "" {
		apiKey = "agent-api-key-1"
	}
	userID = os.Getenv("USER_ID")
	if userID == "" {
		userID = "loadtest-user-1"
	}

	// Resolve the manifest path relative to the repo root.
	// go test changes CWD to the package directory, so we use the source
	// file location to navigate up to the repo root reliably.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	manifestPath := filepath.Join(repoRoot, "loadtest", "results", "seed-manifest.json")

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		// Manifest not present — tests will skip themselves.
		manifest = nil
	} else {
		var m seedManifest
		if jsonErr := json.Unmarshal(data, &m); jsonErr != nil {
			fmt.Fprintf(os.Stderr, "failed to parse %s: %v\n", manifestPath, jsonErr)
			os.Exit(1)
		}
		manifest = &m
	}

	code := m.Run()
	writeReport(baseURL, repoRoot)
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func doGetAs(t *testing.T, url, asUserID string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("doGetAs: build request: %v", err)
	}
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("X-User-ID", asUserID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("doGetAs %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("doGetAs %s: status %d: %s", url, resp.StatusCode, body)
	}
	return body
}

// doGet uses the package-level userID (for search and other non-owner-scoped calls).
func doGet(t *testing.T, url string) []byte {
	return doGetAs(t, url, userID)
}

func doPostAs(t *testing.T, url, asUserID string, payload any) []byte {
	t.Helper()
	reqBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("doPostAs: marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("doPostAs: build request: %v", err)
	}
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("X-User-ID", asUserID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("doPostAs %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("doPostAs %s: status %d: %s", url, resp.StatusCode, body)
	}
	return body
}

func doPost(t *testing.T, url string, payload any) []byte {
	return doPostAs(t, url, userID, payload)
}

// ---------------------------------------------------------------------------
// TestConversationListPagination
// ---------------------------------------------------------------------------

func TestConversationListPagination(t *testing.T) {
	if manifest == nil {
		t.Skip("seed manifest not found — run 'task loadtest:seed' first")
	}

	// Conversations are owner-scoped: each owner only sees their own conversations.
	// We collect across all unique owners and merge results to get a complete view.
	ownerConvs := make(map[string][]string) // ownerID -> []convID
	for _, conv := range manifest.Conversations {
		ownerConvs[conv.OwnerID] = append(ownerConvs[conv.OwnerID], conv.ID)
	}

	// Also include fork-chain owner (always "loadtest-user-1").
	for _, fork := range manifest.Forks {
		_ = fork // forks are listed under their owner via the conversations endpoint
	}

	collected := make(map[string]int) // convID -> page-seen count (duplicate detection)

	for ownerID := range ownerConvs {
		cursor := ""
		pageNum := 0
		for {
			url := fmt.Sprintf("%s/v1/conversations?mode=all&limit=20", baseURL)
			if cursor != "" {
				url += "&afterCursor=" + cursor
			}
			body := doGetAs(t, url, ownerID)

			var page struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
				AfterCursor *string `json:"afterCursor"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				t.Fatalf("owner %s page %d: unmarshal conversations response: %v", ownerID, pageNum, err)
			}

			for _, conv := range page.Data {
				collected[conv.ID]++
			}
			pageNum++

			if page.AfterCursor == nil || *page.AfterCursor == "" {
				break
			}
			cursor = *page.AfterCursor
		}
	}

	// Assert no duplicates within a single owner's pages.
	var duplicated []string
	for id, count := range collected {
		if count > 1 {
			duplicated = append(duplicated, fmt.Sprintf("%s (seen %d times)", id, count))
		}
	}
	if len(duplicated) > 0 {
		recordResult("TestConversationListPagination", false, len(collected),
			fmt.Sprintf("duplicate IDs: %v", duplicated))
		t.Fatalf("duplicate conversation IDs across pages: %v", duplicated)
	}

	// Assert every seeded conversation ID appears under its owner's listing.
	var missing []string
	for _, conv := range manifest.Conversations {
		if collected[conv.ID] == 0 {
			missing = append(missing, conv.ID)
		}
	}
	if len(missing) > 0 {
		detail := fmt.Sprintf("missing %d IDs: %v", len(missing), missing)
		recordResult("TestConversationListPagination", false, len(collected), detail)
		t.Fatalf("seeded conversations not found in owner-scoped list: %s", detail)
	}

	recordResult("TestConversationListPagination", true, len(collected), "")
}

// ---------------------------------------------------------------------------
// TestEntryListPagination
// ---------------------------------------------------------------------------

func TestEntryListPagination(t *testing.T) {
	if manifest == nil {
		t.Skip("seed manifest not found — run 'task loadtest:seed' first")
	}

	// Sample: short bucket (entryCount < 11) + long-tail bucket (entryCount >= 101).
	var sampled []conversationRecord
	for _, conv := range manifest.Conversations {
		if conv.EntryCount < 11 || conv.EntryCount >= 101 {
			sampled = append(sampled, conv)
		}
	}

	totalEntries := 0
	for _, conv := range sampled {
		conv := conv // capture
		t.Run(conv.ID, func(t *testing.T) {
			t.Parallel()
			// Use the conversation's actual owner — the service is owner-scoped.
			owner := conv.OwnerID
			if owner == "" {
				owner = userID // fallback for old manifests without ownerID
			}
			collected := make(map[string]int)
			cursor := ""
			pageNum := 0
			for {
				url := fmt.Sprintf("%s/v1/conversations/%s/entries?limit=10&channel=history", baseURL, conv.ID)
				if cursor != "" {
					url += "&afterCursor=" + cursor
				}
				body := doGetAs(t, url, owner)

				var page struct {
					Data []struct {
						ID string `json:"id"`
					} `json:"data"`
					AfterCursor *string `json:"afterCursor"`
				}
				if err := json.Unmarshal(body, &page); err != nil {
					t.Fatalf("page %d: unmarshal entries response: %v", pageNum, err)
				}

				for _, entry := range page.Data {
					collected[entry.ID]++
				}
				pageNum++

				if page.AfterCursor == nil || *page.AfterCursor == "" {
					break
				}
				cursor = *page.AfterCursor
			}

			// Assert no duplicates.
			for id, count := range collected {
				if count > 1 {
					t.Errorf("duplicate entry ID %s (seen %d times)", id, count)
				}
			}

			// Generator stores USER+AI pairs, so total stored = entryCount * 2.
			expected := conv.EntryCount * 2
			if len(collected) != expected {
				t.Errorf("entry count mismatch for conversation %s: got %d, want %d",
					conv.ID, len(collected), expected)
			}

			resultsMu.Lock()
			totalEntries += len(collected)
			resultsMu.Unlock()
		})
	}

	// Record aggregate result after all sub-tests complete.
	t.Cleanup(func() {
		passed := !t.Failed()
		recordResult("TestEntryListPagination", passed, totalEntries,
			fmt.Sprintf("%d conversations sampled", len(sampled)))
	})
}

// ---------------------------------------------------------------------------
// TestSearchPagination
// ---------------------------------------------------------------------------

func TestSearchPagination(t *testing.T) {
	if manifest == nil {
		t.Skip("seed manifest not found — run 'task loadtest:seed' first")
	}

	collected := make(map[string]int)
	cursor := ""
	pageNum := 0
	for {
		reqBody := map[string]any{"query": "load-test", "limit": 5}
		if cursor != "" {
			reqBody["afterCursor"] = cursor
		}
		body := doPost(t, baseURL+"/v1/conversations/search", reqBody)

		var page struct {
			Data []struct {
				ConversationID string `json:"conversationId"`
			} `json:"data"`
			AfterCursor *string `json:"afterCursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("page %d: unmarshal search response: %v", pageNum, err)
		}

		for _, result := range page.Data {
			collected[result.ConversationID]++
		}
		pageNum++

		if page.AfterCursor == nil || *page.AfterCursor == "" {
			break
		}
		cursor = *page.AfterCursor
	}

	// Assert no duplicates.
	var duplicated []string
	for id, count := range collected {
		if count > 1 {
			duplicated = append(duplicated, fmt.Sprintf("%s (seen %d times)", id, count))
		}
	}
	if len(duplicated) > 0 {
		recordResult("TestSearchPagination", false, len(collected),
			fmt.Sprintf("duplicate IDs: %v", duplicated))
		t.Fatalf("duplicate conversation IDs in search results: %v", duplicated)
	}

	recordResult("TestSearchPagination", true, len(collected), "")
}
