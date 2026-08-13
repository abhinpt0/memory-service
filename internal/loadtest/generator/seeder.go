package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// createConversation posts a new conversation to the memory service and returns
// the assigned conversation ID.
func createConversation(client *http.Client, cfg GeneratorConfig, title string) (string, error) {
	body, err := json.Marshal(map[string]string{"title": title})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/v1/conversations", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
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

// appendEntry appends a single content entry to a conversation and returns the
// new entry ID. userID is sent as the X-User-ID header. role must be "USER" or
// "AI". For fork seeding, forkedAtConversationId and forkedAtEntryId may be
// non-empty to make this append implicitly create a fork.
func appendEntry(
	client *http.Client,
	cfg GeneratorConfig,
	convID, userID, role, text string,
	forkedAtConversationId, forkedAtEntryId string,
) (string, error) {
	payload := map[string]any{
		"contentType": "history",
		"content":     []map[string]string{{"role": role, "text": text}},
		"userId":      userID,
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
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", cfg.APIKey)
	req.Header.Set("X-User-ID", userID)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
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

// createFork creates a new conversation and seeds it as a fork of rootConvID
// branching at atEntryID. Returns the new fork conversation ID.
func createFork(client *http.Client, cfg GeneratorConfig, rootConvID, atEntryID string) (string, error) {
	// Create the new (fork) conversation shell.
	forkID, err := createConversation(client, cfg, "load-test-fork-"+rootConvID)
	if err != nil {
		return "", fmt.Errorf("createFork: create conversation: %w", err)
	}

	// The first entry append with forkedAt fields is what implicitly registers
	// the fork in the ancestry closure table (per AGENTS.md).
	_, err = appendEntry(client, cfg, forkID, "loadtest-user-1", "USER",
		"fork branch entry", rootConvID, atEntryID)
	if err != nil {
		return "", fmt.Errorf("createFork: append first entry: %w", err)
	}

	return forkID, nil
}
