package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIndexEntriesFailsAfterRateLimitRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	err := indexEntriesWithSleep(
		server.Client(),
		GeneratorConfig{BaseURL: server.URL, APIKey: "test-key"},
		[]indexEntryRequest{{ConversationID: "conversation-1", EntryID: "entry-1", IndexedContent: "text"}},
		1,
		func(time.Duration) {},
	)
	if err == nil || !strings.Contains(err.Error(), "rate limited after 20 attempts") {
		t.Fatalf("indexEntriesWithSleep() error = %v, want retry exhaustion error", err)
	}
	if attempts != 20 {
		t.Fatalf("attempts = %d, want 20", attempts)
	}
}
