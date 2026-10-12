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
	batch := registrystore.NewAppendBatch(request, clock.End.Add(-time.Duration(len(request.Entries)-1)*time.Millisecond))
	batch.ConversationPatch = registrystore.AppendConversationPatchFromContext(ctx)
	return batch, nil
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
	registrystore.SetAppendConversationPatchResult(ctx, registrystore.AppendConversationPatchResult{Handled: true})
	if !anchor.AppendBatchComplete {
		if err := s.requireAppendBatchPatchAccess(ctx, anchor, batch); err != nil {
			return mismatch, err
		}
	}
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
		completed, err := s.finishAppendBatch(ctx, anchor, batch)
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

func (s *MongoStore) appendBatchPatch(ctx context.Context, batch *registrystore.AppendBatch) *registrystore.AppendConversationPatch {
	// BACKWARD COMPATIBILITY: remove in a future breaking release.
	// Version 1 did not retain the original patch. Only incomplete legacy batches
	// can use the validated retry patch; completed retries still skip it.
	if batch.Version == 1 {
		return registrystore.AppendConversationPatchFromContext(ctx)
	}
	return batch.ConversationPatch
}

func (s *MongoStore) requireAppendBatchPatchAccess(ctx context.Context, anchor model.Entry, batch *registrystore.AppendBatch) error {
	level := model.AccessLevelWriter
	if patch := s.appendBatchPatch(ctx, batch); patch != nil && patch.Archived != nil {
		level = model.AccessLevelOwner
	}
	_, err := s.requireAccess(ctx, batch.Request.UserID, anchor.ConversationGroupID.String(), level)
	return err
}

// patch revisions fence every target write independently on standalone MongoDB.
// Existing schema-v2 documents migrate lazily: an absent revision is zero, and
// the first normal mutation or guarded patch initializes it atomically.
func appendPatchRevisionFilter(id string, revision int64) bson.M {
	filter := bson.M{"_id": id}
	if revision == 0 {
		// BACKWARD COMPATIBILITY: remove in a future breaking release.
		// Existing schema-v2 documents initialize the revision lazily on first mutation.
		filter["$or"] = bson.A{bson.M{"patch_revision": 0}, bson.M{"patch_revision": bson.M{"$exists": false}}}
	} else {
		filter["patch_revision"] = revision
	}
	return filter
}

type appendPatchReceipt struct {
	Anchor  string `bson:"anchor"`
	Changed bool   `bson:"changed"`
}

func appendPatchStageReceipt(anchor model.Entry, previous *appendPatchReceipt, changed bool) appendPatchReceipt {
	if previous != nil && previous.Anchor == anchor.ID.String() {
		changed = changed || previous.Changed
	}
	return appendPatchReceipt{Anchor: anchor.ID.String(), Changed: changed}
}

// A snapshot must be captured BEFORE this check. A winner's patch or any normal
// edit then invalidates the snapshot, even if completion follows the check.
func (s *MongoStore) appendPatchMayApply(ctx context.Context, anchor model.Entry) (bool, error) {
	var current entryDoc
	if err := s.entries().FindOne(ctx, bson.M{"_id": anchor.ID.String()}).Decode(&current); err != nil {
		return false, err
	}
	if current.AppendBatchComplete {
		return false, nil
	}
	if s.appendBatchPatchBeforeApply != nil {
		if err := s.appendBatchPatchBeforeApply(); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *MongoStore) rememberAppendPatchChange(ctx context.Context, anchor model.Entry, changed bool, archived *bool) error {
	if !changed {
		return nil
	}
	set := bson.M{"append_batch_patch_changed": true}
	if archived != nil {
		set["append_batch_patch_archived"] = *archived
	}
	_, err := s.entries().UpdateOne(ctx, bson.M{"_id": anchor.ID.String()}, bson.M{"$set": set})
	return err
}

func (s *MongoStore) finishAppendBatchArchive(ctx context.Context, anchor model.Entry, archived bool) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var group groupDoc
		if err := s.groups().FindOne(ctx, bson.M{"_id": anchor.ConversationGroupID.String()}).Decode(&group); err != nil {
			return false, err
		}
		apply, err := s.appendPatchMayApply(ctx, anchor)
		if err != nil || !apply {
			return apply, err
		}
		receipt := appendPatchStageReceipt(anchor, group.AppendPatch, archived != (group.ArchivedAt != nil))
		set := bson.M{"patch_revision": group.PatchRevision + 1, "append_patch": bson.M{"$literal": receipt}}
		var archiveValue any = "$$REMOVE"
		if archived {
			archiveValue = time.Now().UTC()
			if group.ArchivedAt != nil {
				archiveValue = *group.ArchivedAt
			}
		}
		set["archived_at"] = archiveValue
		updated, err := s.groups().UpdateOne(ctx, appendPatchRevisionFilter(group.ID, group.PatchRevision), mongo.Pipeline{bson.D{{Key: "$set", Value: set}}})
		if err != nil {
			return false, err
		}
		if updated.MatchedCount == 0 {
			continue
		}
		if err := s.rememberAppendPatchChange(ctx, anchor, receipt.Changed, &archived); err != nil {
			return false, err
		}
		group.PatchRevision++
		// Each conversation is fenced separately; UpdateMany cannot bind each target
		// to a snapshot captured before the anchor completion check.
		cur, err := s.conversations().Find(ctx, bson.M{"conversation_group_id": group.ID})
		if err != nil {
			return false, err
		}
		var conversations []convDoc
		if err := cur.All(ctx, &conversations); err != nil {
			return false, err
		}
		restart := false
		for _, conversation := range conversations {
			for {
				var current convDoc
				if err := s.conversations().FindOne(ctx, bson.M{"_id": conversation.ID}).Decode(&current); errors.Is(err, mongo.ErrNoDocuments) {
					break
				} else if err != nil {
					return false, err
				}
				// A normal archive operation may have completed before this snapshot.
				// Reconfirm authoritative group state before applying conversation state.
				var authoritative groupDoc
				if err := s.groups().FindOne(ctx, bson.M{"_id": group.ID}).Decode(&authoritative); err != nil {
					return false, err
				}
				if authoritative.PatchRevision != group.PatchRevision {
					restart = true
					break
				}
				apply, err := s.appendPatchMayApply(ctx, anchor)
				if err != nil || !apply {
					return apply, err
				}
				receipt := appendPatchStageReceipt(anchor, current.AppendPatch, false)
				updated, err := s.conversations().UpdateOne(ctx, appendPatchRevisionFilter(current.ID, current.PatchRevision), mongo.Pipeline{bson.D{{Key: "$set", Value: bson.M{"archived_at": archiveValue, "patch_revision": current.PatchRevision + 1, "append_patch": bson.M{"$literal": receipt}}}}})
				if err != nil {
					return false, err
				}
				if updated.MatchedCount > 0 {
					break
				}
			}
			if restart {
				break
			}
		}
		if !restart {
			return true, nil
		}
	}
}

func (s *MongoStore) finishAppendBatchConversation(ctx context.Context, anchor model.Entry, patch *registrystore.AppendConversationPatch) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var current convDoc
		if err := s.conversations().FindOne(ctx, bson.M{"_id": anchor.ConversationID}).Decode(&current); err != nil {
			return false, err
		}
		apply, err := s.appendPatchMayApply(ctx, anchor)
		if err != nil || !apply {
			return apply, err
		}
		title := patch.Title
		if title != nil {
			oldTitle, err := s.decryptConversationTitle(current.ID, current.Title)
			if err != nil {
				return false, err
			}
			if oldTitle == *title {
				title = nil
			}
		}
		metadata := patch.Metadata.ChangesAgainst(current.Metadata)
		receipt := appendPatchStageReceipt(anchor, current.AppendPatch, title != nil || len(metadata) > 0)
		pipeline, err := s.conversationPatchPipeline(current.ID, title, metadata)
		if err != nil {
			return false, err
		}
		pipeline[0][0].Value.(bson.M)["append_patch"] = bson.M{"$literal": receipt}
		updated, err := s.conversations().UpdateOne(ctx, appendPatchRevisionFilter(current.ID, current.PatchRevision), pipeline)
		if err != nil {
			return false, err
		}
		if updated.MatchedCount == 0 {
			continue
		}
		if err := s.rememberAppendPatchChange(ctx, anchor, receipt.Changed, nil); err != nil {
			return false, err
		}
		return true, nil
	}
}

func (s *MongoStore) finishAppendBatch(ctx context.Context, anchor model.Entry, batch *registrystore.AppendBatch) (bool, error) {
	result := registrystore.AppendConversationPatchResult{Handled: true}
	if patch := s.appendBatchPatch(ctx, batch); patch != nil {
		if err := s.requireAppendBatchPatchAccess(ctx, anchor, batch); err != nil {
			return false, err
		}
		if patch.Archived != nil {
			apply, err := s.finishAppendBatchArchive(ctx, anchor, *patch.Archived)
			if err != nil || !apply {
				return false, err
			}
		}
		apply, err := s.finishAppendBatchConversation(ctx, anchor, patch)
		if err != nil || !apply {
			return false, err
		}
	} else {
		apply, err := s.appendPatchMayApply(ctx, anchor)
		if err != nil || !apply {
			return false, err
		}
	}
	// Leave the batch incomplete on any stage failure. Receipts retain change
	// results when a different finalizer wins completion after a no-op stage.
	var current entryDoc
	if err := s.entries().FindOne(ctx, bson.M{"_id": anchor.ID.String()}).Decode(&current); err != nil {
		return false, err
	}
	result.Changed, result.Archived = current.AppendBatchPatchChanged, current.AppendBatchPatchArchived
	completed, err := s.completeAppendBatch(ctx, anchor.ID)
	if err != nil {
		return false, err
	}
	if completed {
		registrystore.SetAppendConversationPatchResult(ctx, result)
	}
	return completed, nil
}
