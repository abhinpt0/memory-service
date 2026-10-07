package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/chirino/memory-service/internal/model"
	"github.com/google/uuid"
)

// AppendBatch is encrypted on the sequence anchor. IDs and timestamps reserve
// every position before any partial write, including positions before the anchor.
type AppendBatch struct {
	Version      int
	Request      SequencedAppendRequest
	IDs          []uuid.UUID
	Start        time.Time
	Fingerprints []string
	Attachments  [][]batchAttachment
}

func (r SequencedAppendRequest) Anchor() int {
	for i, entry := range r.Entries {
		if entry.Seq != nil {
			return i
		}
	}
	return -1
}

func NewAppendBatch(r SequencedAppendRequest, start time.Time) *AppendBatch {
	b := &AppendBatch{Version: 1, Request: r, Start: start, IDs: make([]uuid.UUID, len(r.Entries))}
	for i := range b.IDs {
		b.IDs[i] = uuid.New()
	}
	return b
}

func NextAppendTime(now, latest time.Time) time.Time {
	now = now.UTC().Truncate(time.Millisecond)
	if !now.After(latest) {
		now = latest.UTC().Truncate(time.Millisecond).Add(time.Millisecond)
	}
	return now
}

func (b *AppendBatch) Entry(i int, conversationID string, groupID uuid.UUID) model.Entry {
	req := b.Request.Entries[i]
	ch := model.Channel(strings.ToLower(req.Channel))
	if ch == "" {
		ch = model.ChannelHistory
	}
	return model.Entry{ID: b.IDs[i], ConversationID: conversationID, ConversationGroupID: groupID,
		UserID: &b.Request.UserID, ClientID: b.Request.ClientID, AgentID: b.Request.AgentID,
		Channel: ch, Epoch: EpochForChannel(ch, b.Request.Epoch), Seq: req.Seq,
		ContentType: req.ContentType, Content: req.Content, IndexedContent: req.IndexedContent,
		CreatedAt: b.Start.Add(time.Duration(i) * time.Millisecond)}
}

func DecodeAppendBatch(data []byte) (*AppendBatch, error) {
	var b AppendBatch
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("decode append batch: %w", err)
	}
	if b.Version != 1 || len(b.Fingerprints) != len(b.IDs) || len(b.Attachments) != len(b.IDs) || len(b.IDs) != len(b.Request.Entries) || len(b.IDs) < 2 || b.Request.Anchor() < 0 {
		return nil, fmt.Errorf("invalid append batch definition")
	}
	return &b, nil
}

// EncodeAppendBatch stores bounded fingerprints rather than duplicate content.
// Attachment IDs retain the original clones for positions a retry must finish.
func EncodeAppendBatch(ctx context.Context, store MemoryStore, b *AppendBatch) ([]byte, error) {
	saved := *b
	saved.Request = SequencedAppendRequest{Entries: make([]CreateEntryRequest, len(b.IDs))}
	saved.Fingerprints = make([]string, len(b.IDs))
	saved.Attachments = make([][]batchAttachment, len(b.IDs))
	for i, req := range b.Request.Entries {
		saved.Request.Entries[i].Seq = req.Seq
		fingerprint, attachments, err := appendFingerprint(ctx, store, b.Request, i)
		if err != nil {
			return nil, err
		}
		saved.Fingerprints[i] = fingerprint
		saved.Attachments[i] = attachments
	}
	return json.Marshal(saved)
}

func (b *AppendBatch) Matches(ctx context.Context, store MemoryStore, r SequencedAppendRequest) (bool, error) {
	if len(b.IDs) != len(r.Entries) || b.Request.Anchor() != r.Anchor() {
		return false, nil
	}
	for i := range r.Entries {
		fingerprint, _, err := appendFingerprint(ctx, store, r, i)
		if err != nil || fingerprint != b.Fingerprints[i] {
			return false, err
		}
	}
	return true, nil
}

// RestoreRequest uses the validated retry content and the original attachment
// IDs, which may have been cloned before the interrupted append.
func (b *AppendBatch) RestoreRequest(r SequencedAppendRequest) (SequencedAppendRequest, error) {
	r.Entries = append([]CreateEntryRequest(nil), r.Entries...)
	for i, attachments := range b.Attachments {
		if len(attachments) == 0 {
			continue
		}
		value, err := decodeBatchContent(r.Entries[i].Content)
		if err != nil {
			return r, err
		}
		for _, ref := range attachments {
			block := value.([]any)[ref.Block].(map[string]any)
			block["attachments"].([]any)[ref.Index].(map[string]any)["attachmentId"] = ref.ID
		}
		content, err := json.Marshal(value)
		if err != nil {
			return r, err
		}
		r.Entries[i].Content = content
	}
	return r, nil
}

type batchAttachmentIdentity string

type batchAttachment struct {
	Block int
	Index int
	ID    string
}

func decodeBatchContent(content []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func appendFingerprint(ctx context.Context, store MemoryStore, r SequencedAppendRequest, i int) (string, []batchAttachment, error) {
	req := r.Entries[i]
	ch := model.Channel(strings.ToLower(req.Channel))
	if ch == "" {
		ch = model.ChannelHistory
	}
	value, err := decodeBatchContent(req.Content)
	if err != nil {
		return "", nil, err
	}
	var attachments []batchAttachment
	if ch == model.ChannelHistory {
		blocks, _ := value.([]any)
		for blockIndex, item := range blocks {
			block, _ := item.(map[string]any)
			refs, _ := block["attachments"].([]any)
			for index, item := range refs {
				ref, _ := item.(map[string]any)
				id, ok := ref["attachmentId"].(string)
				if !ok {
					continue
				}
				parsed, err := uuid.Parse(id)
				if err != nil {
					continue
				}
				attachment, err := store.GetAttachment(ctx, r.UserID, "", parsed)
				if err != nil {
					return "", nil, err
				}
				attachments = append(attachments, batchAttachment{Block: blockIndex, Index: index, ID: id})
				// A private type prevents caller JSON from impersonating a storage key.
				if attachment.StorageKey != nil {
					ref["attachmentId"] = batchAttachmentIdentity(*attachment.StorageKey)
				}
			}
		}
	}
	req.Content = nil
	req.Channel = string(ch)
	// Identity belongs to the authenticated append context, not optional entry hints.
	req.UserID = nil
	req.AgentID = nil
	metadata, err := json.Marshal(struct {
		Entry         CreateEntryRequest
		User          string
		Client, Agent *string
		Epoch         *int64
	}{req, r.UserID, r.ClientID, r.AgentID, EpochForChannel(ch, r.Epoch)})
	if err != nil {
		return "", nil, err
	}
	digest := sha256.New()
	_, _ = digest.Write(metadata)
	writeBatchValue(digest, value)
	return fmt.Sprintf("%x", digest.Sum(nil)), attachments, nil
}

// Type tags and sorted object keys preserve the exact JSON comparison semantics,
// including decimal equivalence without converting numbers to floating point.
func writeBatchValue(w io.Writer, value any) {
	switch v := value.(type) {
	case nil:
		_, _ = io.WriteString(w, "z")
	case bool:
		_, _ = fmt.Fprintf(w, "b%t", v)
	case batchAttachmentIdentity:
		data, _ := json.Marshal(string(v))
		_, _ = io.WriteString(w, "a")
		_, _ = w.Write(data)
	case string:
		data, _ := json.Marshal(v)
		_, _ = io.WriteString(w, "s")
		_, _ = w.Write(data)
	case json.Number:
		n, ok := normalizeJSONNumber(v)
		if ok {
			_, _ = fmt.Fprintf(w, "n%t:%s:%d;", n.negative, n.digits, n.exponent)
		} else {
			_, _ = fmt.Fprintf(w, "r%s;", v)
		}
	case []any:
		_, _ = io.WriteString(w, "[")
		for _, item := range v {
			writeBatchValue(w, item)
		}
		_, _ = io.WriteString(w, "]")
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		_, _ = io.WriteString(w, "{")
		for _, key := range keys {
			writeBatchValue(w, key)
			writeBatchValue(w, v[key])
		}
		_, _ = io.WriteString(w, "}")
	}
}
