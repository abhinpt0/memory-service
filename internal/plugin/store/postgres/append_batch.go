//go:build !nopostgresql

package postgres

import (
	"context"

	"github.com/chirino/memory-service/internal/model"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
)

func (s *PostgresStore) RecoverAppendBatch(ctx context.Context, anchor model.Entry, request registrystore.SequencedAppendRequest) (registrystore.SequencedAppendMatch, error) {
	mismatch := registrystore.SequencedAppendMatch{AnyExisting: true}
	data, err := s.decryptEntryContent(anchor.ID, anchor.AppendBatch)
	if err != nil {
		return mismatch, err
	}
	batch, err := registrystore.DecodeAppendBatch(data)
	if err != nil {
		return mismatch, err
	}
	matches, err := batch.Matches(ctx, s, request)
	if err != nil || !matches {
		return mismatch, err
	}
	var entries []model.Entry
	if err := s.dbFor(ctx).Where("conversation_id = ? AND conversation_group_id = ? AND id IN ?", anchor.ConversationID, anchor.ConversationGroupID, batch.IDs).Find(&entries).Error; err != nil {
		return mismatch, err
	}
	if len(entries) != len(batch.IDs) {
		return mismatch, nil
	}
	if err := decryptEntries(s, entries); err != nil {
		return mismatch, err
	}
	byID := make(map[string]model.Entry, len(entries))
	for _, entry := range entries {
		byID[entry.ID.String()] = entry
	}
	ordered := make([]model.Entry, len(batch.IDs))
	for i, id := range batch.IDs {
		entry, ok := byID[id.String()]
		if !ok {
			return mismatch, nil
		}
		ordered[i] = entry
	}
	return registrystore.SequencedAppendMatch{Entries: ordered, Exact: true, AnyExisting: true}, nil
}
