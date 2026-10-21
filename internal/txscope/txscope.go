package txscope

import (
	"context"
	"sync"
)

// Intent describes the transaction intent attached to a request context.
type Intent string

const (
	IntentRead  Intent = "read"
	IntentWrite Intent = "write"
)

type contextKey struct{}

type afterCommitKey struct{}

// AfterCommitScope collects effects that must run only after the surrounding
// datastore transaction commits. A new scope is required for every retry
// attempt so effects registered by an aborted attempt are discarded.
type AfterCommitScope struct {
	mu      sync.Mutex
	actions []func(context.Context)
}

// WithIntent annotates ctx with the requested transaction intent.
func WithIntent(ctx context.Context, intent Intent) context.Context {
	return context.WithValue(ctx, contextKey{}, intent)
}

// WithAfterCommitScope attaches a fresh after-commit scope to ctx.
func WithAfterCommitScope(ctx context.Context) (context.Context, *AfterCommitScope) {
	scope := &AfterCommitScope{}
	return context.WithValue(ctx, afterCommitKey{}, scope), scope
}

// AfterCommit registers fn on the current transaction attempt. If no scope is
// active, fn runs immediately so callers remain safe outside retrying stores.
func AfterCommit(ctx context.Context, fn func(context.Context)) {
	if fn == nil {
		return
	}
	scope, ok := ctx.Value(afterCommitKey{}).(*AfterCommitScope)
	if !ok || scope == nil {
		fn(ctx)
		return
	}
	scope.mu.Lock()
	scope.actions = append(scope.actions, fn)
	scope.mu.Unlock()
}

// Run executes the actions registered by the committed transaction attempt.
func (s *AfterCommitScope) Run(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	actions := append([]func(context.Context){}, s.actions...)
	s.actions = nil
	s.mu.Unlock()
	for _, action := range actions {
		action(ctx)
	}
}
