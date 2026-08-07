---
layout: ../../../layouts/DocsLayout.astro
title: Conversations
description: Understanding conversations in Memory Service.
---

Conversations are the fundamental unit of organization in Memory Service. A conversation represents a sequence of entries between users, agents, and AI models.

## What is a Conversation?

A conversation in Memory Service is:

- A container for a sequence of **entries**
- Identified by a unique **conversation ID**
- Owned by a **user** (for access control)
- Optionally associated with **metadata** — arbitrary key-value pairs agents can read and write

## Conversation Lifecycle

### Creating a Conversation

```bash
curl -X POST http://localhost:8080/v1/conversations \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{"title": "Support chat", "metadata": {"topic": "support"}}'
```

Response:

```json
{
  "id": "conv_01HF8XH1XABCD1234EFGH5678",
  "title": "Support chat",
  "ownerUserId": "user_1234",
  "metadata": { "topic": "support" },
  "createdAt": "2025-01-10T14:32:05Z",
  "updatedAt": "2025-01-10T14:32:05Z",
  "accessLevel": "owner"
}
```

### Retrieving a Conversation

```bash
curl http://localhost:8080/v1/conversations/{conversationId} \
  -H "Authorization: Bearer <token>"
```

### Listing Conversations

```bash
curl "http://localhost:8080/v1/conversations?limit=20" \
  -H "Authorization: Bearer <token>"
```

Each summary in the list response includes the `metadata` map so agents can read conversation state without an extra fetch.

### Updating a Conversation

`PATCH /v1/conversations/{id}` updates a conversation's `title`, `metadata`, and `archived` state.

- **`title`**: Replaces the current title when provided. Setting `title` to JSON `null` clears the title.
- **`metadata`**: Uses top-level merge-patch semantics. Each non-null value replaces
  the complete value at that top-level key; nested objects are not merged recursively:
  - Keys present in the patch are set.
  - Keys absent from the patch are left unchanged.
  - Keys explicitly set to `null` in the patch are removed.

```bash
# Set or overwrite metadata keys
curl -X PATCH http://localhost:8080/v1/conversations/{conversationId} \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{"metadata": {"status": "running", "priority": "high"}}'

# Remove the "priority" key by setting it to null
curl -X PATCH http://localhost:8080/v1/conversations/{conversationId} \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{"metadata": {"priority": null}}'

# Update title
curl -X PATCH http://localhost:8080/v1/conversations/{conversationId} \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{"title": "New title"}'

# Archive the conversation
curl -X PATCH http://localhost:8080/v1/conversations/{conversationId} \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{"archived": true}'
```

Updating the title or metadata requires **writer** access or higher. Archiving or unarchiving requires **owner** access.

### Filtering Conversations by Metadata

Use `metadata[<key>]=<value>` query parameters to filter the list to conversations whose metadata contains a specific key-value pair. Only one metadata filter is accepted per request, and the comparison is an exact string match — numeric or boolean metadata values do not match a string query value.

```bash
# List only conversations where metadata.status equals "waiting"
curl --globoff "http://localhost:8080/v1/conversations?metadata[status]=waiting" \
  -H "Authorization: Bearer <token>"

# Combine with mode and other filters
curl --globoff "http://localhost:8080/v1/conversations?mode=all&metadata[status]=running" \
  -H "Authorization: Bearer <token>"
```

The filter key may only contain alphanumeric characters, underscores, and hyphens. Dots are rejected. Invalid keys return `400 Bad Request`.

### Archiving a Conversation

Archiving soft-deletes the conversation (and its entire fork tree). Archived conversations are excluded from the default list but can be retrieved with `archived=include` or `archived=only`.

```bash
# Archive
curl -X PATCH http://localhost:8080/v1/conversations/{conversationId} \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{"archived": true}'

# Unarchive
curl -X PATCH http://localhost:8080/v1/conversations/{conversationId} \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{"archived": false}'

# List archived conversations
curl "http://localhost:8080/v1/conversations?archived=only" \
  -H "Authorization: Bearer <token>"

# Include both active and archived
curl "http://localhost:8080/v1/conversations?archived=include" \
  -H "Authorization: Bearer <token>"
```

Archived conversations remain readable until they are evicted by a retention policy.

## Inline `conversationPatch` on Entry Append

Agents frequently need to update conversation metadata at the same time as appending an entry — for example, transitioning a `status` metadata key from `"running"` to `"done"` when the last journal entry is written. The `conversationPatch` field on `POST /v1/conversations/{id}/entries` and `POST /v1/conversations/{id}/entries/sync` applies the patch with the entry write:

```bash
curl -X POST http://localhost:8080/v1/conversations/{conversationId}/entries \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <agent-token>" \
  -d '{
    "channel": "journal",
    "contentType": "agent/step",
    "content": [{"stepType": "tool_call"}],
    "conversationPatch": {
      "metadata": {"status": "done"},
      "title": "Completed task"
    }
  }'
```

`conversationPatch` supports:

- **`title`**: Replaces the current title when provided. Setting `title` to JSON `null` clears the title.
- **`metadata`**: Uses the same top-level patch semantics (set/preserve/remove keys). A top-level `null` or empty object is a no-op.
- **`archived`**: Archives or unarchives the conversation.

**Atomicity**: On **PostgreSQL** and **SQLite**, the entry write and conversation patch are committed in a single transaction and are always consistent. On **MongoDB**, the entry write and patch are currently separate operations (see [WORKAROUNDS.md](https://github.com/chirino/memory-service/blob/main/WORKAROUNDS.md#mongodb-inwritetx-is-intent-only-non-transactional) for details).

## Conversation Properties

| Property                 | Description                                                          |
| ------------------------ | -------------------------------------------------------------------- |
| `id`                     | Unique identifier (string)                                           |
| `title`                  | Optional conversation title                                          |
| `ownerUserId`            | User who owns the conversation                                       |
| `metadata`               | Arbitrary key-value map, agent-defined                               |
| `createdAt`              | Creation timestamp                                                   |
| `updatedAt`              | Last modification timestamp                                          |
| `archived`               | Boolean indicating whether the conversation is archived              |
| `lastEntryPreview`       | Preview of the last entry                                            |
| `accessLevel`            | Current user's access level (`owner`, `manager`, `writer`, `reader`) |
| `forkedAtConversationId` | ID of conversation this was forked from (if forked)                  |
| `forkedAtEntryId`        | Entry ID where the fork occurred (if forked)                         |

## Best Practices

1. **Use metadata for agent state** — store job status, task IDs, or routing keys so you can filter without fetching individual conversations.
2. **Use `conversationPatch` on append** — on PostgreSQL and SQLite, this keeps metadata transitions and entry writes atomic and avoids separate PATCH calls that can race. MongoDB currently applies the two operations separately.
3. **Handle pagination** — use `limit` and `afterCursor` for large conversation lists.

## Next Steps

- Learn about [Entries](/docs/concepts/entries/)
- Understand [Conversation Forking](/docs/concepts/forking/)
- Review [Real-Time Events](/docs/concepts/events/) to receive `conversation/updated` notifications
