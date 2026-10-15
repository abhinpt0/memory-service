package store

import "context"

// AppendConversationPatch is saved in the encrypted batch definition so an
// interrupted append finishes the original patch, rather than a retry's edits.
type AppendConversationPatch struct {
	Title    *string
	Metadata MetadataPatch
	Archived *bool
}

type AppendConversationPatchResult struct {
	Handled  bool
	Changed  bool
	Archived *bool
}

type appendPatchContextKey struct{}
type appendPatchContext struct {
	patch  *AppendConversationPatch
	result *AppendConversationPatchResult
}

// WithAppendConversationPatch lets stores with non-atomic appends finish a
// durable inline patch before publishing batch completion. Transactional stores
// leave Handled false and let the handler apply the patch in the same write scope.
func WithAppendConversationPatch(ctx context.Context, patch *AppendConversationPatch) (context.Context, *AppendConversationPatchResult) {
	result := &AppendConversationPatchResult{}
	return context.WithValue(ctx, appendPatchContextKey{}, appendPatchContext{patch, result}), result
}

func AppendConversationPatchFromContext(ctx context.Context) *AppendConversationPatch {
	state, _ := ctx.Value(appendPatchContextKey{}).(appendPatchContext)
	return state.patch
}

func SetAppendConversationPatchResult(ctx context.Context, result AppendConversationPatchResult) {
	state, _ := ctx.Value(appendPatchContextKey{}).(appendPatchContext)
	if state.result != nil {
		*state.result = result
	}
}
