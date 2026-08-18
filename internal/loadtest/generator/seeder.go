package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// createConversation posts a new conversation to the memory service and returns
// the assigned conversation ID.
func createConversation(client *http.Client, cfg GeneratorConfig, title, ownerUserID string) (string, error) {
	body, err := json.Marshal(map[string]string{"title": title})
	if err != nil {
		return "", err
	}

	for attempt := 0; attempt < 20; attempt++ {
		req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/v1/conversations", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", cfg.APIKey)
		req.Header.Set("X-User-ID", ownerUserID)

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			backoff := time.Duration(100*(1<<attempt)) * time.Millisecond
			if backoff > time.Second {
				backoff = time.Second
			}
			time.Sleep(backoff)
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("createConversation: status %d: %s", resp.StatusCode, respBody)
		}

		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(respBody, &result); err != nil {
			return "", fmt.Errorf("createConversation: unmarshal: %w", err)
		}
		return result.ID, nil
	}
	return "", fmt.Errorf("createConversation: rate limited after retries")
}

// appendEntry appends a single content entry to a conversation and returns the
// new entry ID. userID is sent as the X-User-ID header. role must be "USER" or
// "AI". For fork seeding, forkedAtConversationId and forkedAtEntryId may be
// non-empty to make this append implicitly create a fork.
func appendEntry(
	client *http.Client,
	cfg GeneratorConfig,
	convID, authUserID, entryUserID, role, text string,
	forkedAtConversationId, forkedAtEntryId string,
) (string, error) {
	payload := map[string]any{
		"contentType": "history",
		"content":     []map[string]string{{"role": role, "text": text}},
		"userId":      entryUserID,
	}
	if forkedAtConversationId != "" {
		payload["forkedAtConversationId"] = forkedAtConversationId
	}
	if forkedAtEntryId != "" {
		payload["forkedAtEntryId"] = forkedAtEntryId
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/v1/conversations/%s/entries", cfg.BaseURL, convID)

	for attempt := 0; attempt < 20; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", cfg.APIKey)
		req.Header.Set("X-User-ID", authUserID)

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			backoff := time.Duration(100*(1<<attempt)) * time.Millisecond
			if backoff > time.Second {
				backoff = time.Second
			}
			time.Sleep(backoff)
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("appendEntry conv=%s: status %d: %s", convID, resp.StatusCode, respBody)
		}

		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(respBody, &result); err != nil {
			return "", fmt.Errorf("appendEntry: unmarshal: %w", err)
		}
		return result.ID, nil
	}
	return "", fmt.Errorf("appendEntry: rate limited after retries")
}

// indexEntries calls POST /v1/conversations/index to make all seeded entries
// searchable via fulltext and semantic search. The agent API key has indexer
// role (MEMORY_SERVICE_ROLES_INDEXER_CLIENTS=agent) so no separate admin key
// is required.
//
// indexedContent for each entry is the entry's text content — the same words
// used in the entry body, which guarantees "load test" search terms will match.
func indexEntries(client *http.Client, cfg GeneratorConfig, entries []indexEntryRequest) error {
	if len(entries) == 0 {
		return nil
	}
	// Index in batches of 100 to avoid oversized requests.
	const batchSize = 100
	for i := 0; i < len(entries); i += batchSize {
		end := i + batchSize
		if end > len(entries) {
			end = len(entries)
		}
		batch := entries[i:end]

		body, err := json.Marshal(batch)
		if err != nil {
			return err
		}

		for attempt := 0; attempt < 20; attempt++ {
			req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/v1/conversations/index", bytes.NewReader(body))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-API-Key", cfg.APIKey)
			req.Header.Set("X-User-ID", "loadtest-user-1") // indexer-role user

			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode == http.StatusTooManyRequests {
				backoff := time.Duration(100*(1<<attempt)) * time.Millisecond
				if backoff > time.Second {
					backoff = time.Second
				}
				time.Sleep(backoff)
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("indexEntries: status %d: %s", resp.StatusCode, respBody)
			}
			break
		}
	}
	return nil
}

// indexEntryRequest is a single entry to submit to POST /v1/conversations/index.
type indexEntryRequest struct {
	ConversationID string `json:"conversationId"`
	EntryID        string `json:"entryId"`
	IndexedContent string `json:"indexedContent"`
}

// createFork creates a new conversation and seeds it as a fork of rootConvID
// branching at atEntryID. Returns the new fork conversation ID.
func createFork(client *http.Client, cfg GeneratorConfig, rootConvID, atEntryID string) (string, error) {
	// Create the new (fork) conversation shell.
	forkID, err := createConversation(client, cfg, "load-test-fork-"+rootConvID, "loadtest-user-1")
	if err != nil {
		return "", fmt.Errorf("createFork: create conversation: %w", err)
	}

	// The first entry append with forkedAt fields is what implicitly registers
	// the fork in the ancestry closure table (per AGENTS.md).
	_, err = appendEntry(client, cfg, forkID, "loadtest-user-1", "loadtest-user-1", "USER",
		"fork branch entry", rootConvID, atEntryID)
	if err != nil {
		return "", fmt.Errorf("createFork: append first entry: %w", err)
	}

	return forkID, nil
}
