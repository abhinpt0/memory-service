package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const TaskClaimLease = 5 * time.Minute

const attachmentBlobCleanupEnqueueTimeout = 5 * time.Second

// Attachment blob delete tasks stay claimed across retries. The provider may
// have completed an earlier attempt even when that attempt returned an error,
// so allowing a new reference between attempts could resurrect a deleted key.
const (
	AttachmentBlobDeleteTaskType        = "attachment_blob_delete"
	AttachmentBlobDeleteStorageKeyField = "storageKey"
	AttachmentBlobDeleteStateField      = "state"
	AttachmentBlobDeleteStateReferenced = "referenced"
	AttachmentBlobDeleteStateQueued     = "queued"
	AttachmentBlobDeleteStateClaimed    = "claimed"
	AttachmentBlobDeleteConflictCode    = "attachment_blob_delete_in_progress"
)

// AttachmentBlobDeleteTaskName returns the singleton task name for a storage key.
func AttachmentBlobDeleteTaskName(storageKey string) string {
	digest := sha256.Sum256([]byte(storageKey))
	return AttachmentBlobDeleteTaskType + ":" + hex.EncodeToString(digest[:])
}

// AttachmentBlobDeleteTaskState returns the task state, treating legacy tasks
// without an explicit state as queued.
func AttachmentBlobDeleteTaskState(body map[string]any) string {
	state, _ := body[AttachmentBlobDeleteStateField].(string)
	if state == "" {
		return AttachmentBlobDeleteStateQueued
	}
	return state
}

// NewAttachmentBlobDeleteConflict rejects a new reference while deletion is
// claimed and in progress for its storage key.
func NewAttachmentBlobDeleteConflict() error {
	return &ConflictError{
		Message: "attachment content deletion is in progress",
		Code:    AttachmentBlobDeleteConflictCode,
	}
}

// CreateAttachmentBlobDeleteTask records durable work to remove attachment
// content. Call it in the same write transaction that removes the metadata.
func CreateAttachmentBlobDeleteTask(ctx context.Context, store MemoryStore, storageKey string) error {
	if storageKey == "" {
		return fmt.Errorf("attachment blob delete task requires a storage key")
	}
	return store.QueueAttachmentBlobDelete(ctx, storageKey)
}

// EnqueueAttachmentBlobDeleteAfterFinalizationFailure durably records cleanup
// after provider storage succeeded but attachment metadata finalization failed.
// It detaches request cancellation because the newly stored blob otherwise has
// no durable owner. If the metadata commit actually succeeded, the worker's
// reference check safely retires the redundant cleanup request.
func EnqueueAttachmentBlobDeleteAfterFinalizationFailure(ctx context.Context, store MemoryStore, storageKey string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attachmentBlobCleanupEnqueueTimeout)
	defer cancel()
	return store.InWriteTx(cleanupCtx, func(txCtx context.Context) error {
		return CreateAttachmentBlobDeleteTask(txCtx, store, storageKey)
	})
}
