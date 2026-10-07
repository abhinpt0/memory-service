//go:build !nomongo

package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chirino/memory-service/internal/model"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func (s *MongoStore) reserveAppendBatch(ctx context.Context, conv convDoc, request registrystore.SequencedAppendRequest) (*registrystore.AppendBatch, error) {
	var latest entryDoc
	err := s.entries().FindOne(ctx, bson.M{"conversation_group_id": conv.ConversationGroupID}, options.FindOne().SetSort(bson.D{{Key: "created_at", Value: -1}})).Decode(&latest)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}
	start := registrystore.NextAppendTime(time.Now(), latest.CreatedAt)
	// MongoDB dates persist milliseconds. Atomically reserve the whole range on
	// the group so concurrent writers and forks allocate disjoint timestamps.
	var clock struct {
		End time.Time `bson:"append_clock"`
	}
	err = s.groups().FindOneAndUpdate(ctx, bson.M{"_id": conv.ConversationGroupID}, mongo.Pipeline{
		bson.D{{Key: "$set", Value: bson.M{"append_clock": bson.M{"$add": bson.A{
			bson.M{"$max": bson.A{bson.M{"$ifNull": bson.A{"$append_clock", start.Add(-time.Millisecond)}}, start.Add(-time.Millisecond)}}, len(request.Entries),
		}}}}},
	}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&clock)
	if err != nil {
		return nil, fmt.Errorf("reserve append timestamps: %w", err)
	}
	return registrystore.NewAppendBatch(request, clock.End.Add(-time.Duration(len(request.Entries)-1)*time.Millisecond)), nil
}

func (s *MongoStore) appendBatchDoc(entry model.Entry, role *string) (entryDoc, error) {
	encrypted, err := s.encryptEntryContent(entry.ID.String(), entry.Content)
	if err != nil {
		return entryDoc{}, err
	}
	return entryDoc{ID: entry.ID.String(), ConversationID: entry.ConversationID, ConversationGroupID: entry.ConversationGroupID.String(), UserID: entry.UserID, ClientID: entry.ClientID, AgentID: entry.AgentID, Channel: string(entry.Channel), Epoch: entry.Epoch, Seq: entry.Seq, ContentType: entry.ContentType, Content: encrypted, IndexedContent: entry.IndexedContent, Role: role, CreatedAt: entry.CreatedAt}, nil
}

func (s *MongoStore) RecoverAppendBatch(ctx context.Context, anchor model.Entry, request registrystore.SequencedAppendRequest) (registrystore.SequencedAppendMatch, error) {
	mismatch := registrystore.SequencedAppendMatch{AnyExisting: true}
	data, err := s.decryptEntryContent(anchor.ID.String(), anchor.AppendBatch)
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
	restored, err := batch.RestoreRequest(request)
	if err != nil {
		return mismatch, err
	}
	batch.Request = restored
	// Validate all occupied sequences before completing any missing positions.
	sequences := make([]uint32, 0, len(request.Entries))
	for _, entry := range request.Entries {
		if entry.Seq != nil {
			sequences = append(sequences, *entry.Seq)
		}
	}
	existing, err := s.GetEntriesBySequence(ctx, request.UserID, anchor.ConversationID, sequences)
	if err != nil {
		return mismatch, err
	}
	reserved := make(map[uuid.UUID]bool, len(batch.IDs))
	for _, id := range batch.IDs {
		reserved[id] = true
	}
	for _, entry := range existing {
		if !reserved[entry.ID] {
			return mismatch, nil
		}
	}
	result := registrystore.SequencedAppendMatch{Exact: true, AnyExisting: true, Entries: make([]model.Entry, len(batch.IDs))}
	for i, id := range batch.IDs {
		filter := bson.M{"_id": id.String(), "conversation_id": anchor.ConversationID, "conversation_group_id": anchor.ConversationGroupID.String()}
		var doc entryDoc
		err := s.entries().FindOne(ctx, filter).Decode(&doc)
		if errors.Is(err, mongo.ErrNoDocuments) {
			if anchor.AppendBatchComplete {
				return mismatch, nil
			}
			entry := batch.Entry(i, anchor.ConversationID, anchor.ConversationGroupID)
			doc, err = s.appendBatchDoc(entry, batch.Request.Entries[i].Role)
			if err != nil {
				return mismatch, err
			}
			_, err = s.entries().InsertOne(ctx, doc)
			if mongo.IsDuplicateKeyError(err) {
				// Decode into fresh pointers; doc's identity pointers belong to the request.
				doc = entryDoc{}
				// The reserved ID makes each position safe under concurrent recovery.
				if err = s.entries().FindOne(ctx, filter).Decode(&doc); errors.Is(err, mongo.ErrNoDocuments) {
					return mismatch, nil
				}
			}
		}
		if err != nil {
			return mismatch, err
		}
		entry, err := s.entryDocToModelWithContent(doc)
		if err != nil {
			return mismatch, err
		}
		result.Entries[i] = entry
	}
	if !anchor.AppendBatchComplete {
		// Invalidate before publishing completion, including for concurrent
		// recoveries that lose the finalizer compare-and-swap.
		if s.entriesCache != nil && s.entriesCache.Available() {
			for _, entry := range result.Entries {
				if entry.Channel == model.ChannelContext && entry.ClientID != nil {
					if err := s.entriesCache.Remove(ctx, anchor.ConversationID, scopedAgentCacheKey(*entry.ClientID, valueOrEmpty(entry.AgentID))); err != nil {
						return mismatch, fmt.Errorf("invalidate recovered context cache: %w", err)
					}
					break
				}
			}
		}
		completed, err := s.completeAppendBatch(ctx, anchor.ID)
		if err != nil {
			return mismatch, err
		}
		// One finalizer publishes the entire recovered batch, including the anchor
		// inserted by a writer that stopped before publishing append events.
		if completed {
			result.Created = append([]model.Entry(nil), result.Entries...)
		}
	}
	return result, nil
}

func (s *MongoStore) completeAppendBatch(ctx context.Context, anchorID uuid.UUID) (bool, error) {
	result, err := s.entries().UpdateOne(ctx, bson.M{"_id": anchorID.String(), "append_batch_complete": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"append_batch_complete": true}})
	if err != nil {
		return false, err
	}
	return result.ModifiedCount == 1, nil
}
