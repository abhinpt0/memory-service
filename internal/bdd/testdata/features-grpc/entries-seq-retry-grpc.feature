Feature: Sequenced entry retry gRPC API
  As a client of the memory service
  I want exact sequenced append retries to return stored entries
  So that a lost response does not create a conflict

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"
    And I have a conversation with title "Sequenced Retry gRPC"

  Scenario: Return the stored entry for an exact retry after a later append
    Given I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: CONTEXT
      content_type: "test.v1"
      seq: 80
      content { string_value: "original first item" }
      content { string_value: "original second item" }
    }
    """
    And the gRPC response should not have an error
    And set "originalEntryId" to the gRPC response field "id"
    And I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: CONTEXT
      content_type: "test.v1"
      seq: 81
      content { string_value: "later" }
    }
    """
    And the gRPC response should not have an error
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: CONTEXT
      content_type: "test.v1"
      seq: 80
      content { string_value: "original first item" }
      content { string_value: "original second item" }
    }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "id" should be "${originalEntryId}"
    Given I am authenticated as admin user "alice"
    When I send gRPC request "AdminEntriesService/ListEntries" with body:
    """
    conversation_id: "${conversationId}"
    channel: CONTEXT
    from_seq: 80
    page { page_size: 10 }
    """
    Then the gRPC response should not have an error
    And the gRPC response should contain 2 entries

  Scenario: Retry a sequenced history entry that cross-references an attachment
    Given I upload a file via gRPC with filename "retry.txt" content type "text/plain" and content "retry attachment"
    And the gRPC response should not have an error
    And set "retrySourceAttachmentId" to the gRPC response field "id"
    And I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      content {
        struct_value {
          fields { key: "role" value { string_value: "USER" } }
          fields { key: "text" value { string_value: "source" } }
          fields {
            key: "attachments"
            value { list_value { values { struct_value {
              fields { key: "attachmentId" value { string_value: "${retrySourceAttachmentId}" } }
              fields { key: "contentType" value { string_value: "text/plain" } }
              fields { key: "name" value { string_value: "retry.txt" } }
            } } } }
          }
        }
      }
    }
    """
    And the gRPC response should not have an error
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 130
      content {
        struct_value {
          fields { key: "role" value { string_value: "USER" } }
          fields { key: "text" value { string_value: "reuse" } }
          fields {
            key: "attachments"
            value { list_value { values { struct_value {
              fields { key: "attachmentId" value { string_value: "${retrySourceAttachmentId}" } }
              fields { key: "contentType" value { string_value: "text/plain" } }
              fields { key: "name" value { string_value: "retry.txt" } }
            } } } }
          }
        }
      }
    }
    """
    Then the gRPC response should not have an error
    And set "retryAttachmentEntryId" to the gRPC response field "id"
    And set "retryCloneAttachmentId" to the gRPC response field "content[0].attachments[0].attachmentId"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 130
      content {
        struct_value {
          fields { key: "role" value { string_value: "USER" } }
          fields { key: "text" value { string_value: "reuse" } }
          fields {
            key: "attachments"
            value { list_value { values { struct_value {
              fields { key: "attachmentId" value { string_value: "${retrySourceAttachmentId}" } }
              fields { key: "contentType" value { string_value: "text/plain" } }
              fields { key: "name" value { string_value: "retry.txt" } }
            } } } }
          }
        }
      }
    }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "id" should be "${retryAttachmentEntryId}"
    And the gRPC response field "content[0].attachments[0].attachmentId" should be "${retryCloneAttachmentId}"
    Given I am authenticated as admin user "alice"
    When I call GET "/v1/admin/attachments?userId=alice"
    Then the response status should be 200
    And the response body "data" should have 2 items

  Scenario: Arbitrary attachmentId keys do not use history attachment equivalence
    Given I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: CONTEXT
      content_type: "tool.v1"
      seq: 150
      content { struct_value { fields { key: "tool" value { struct_value { fields { key: "attachmentId" value { string_value: "00000000-0000-4000-8000-000000000001" } } } } } } }
    }
    """
    And the gRPC response should not have an error
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: CONTEXT
      content_type: "tool.v1"
      seq: 150
      content { struct_value { fields { key: "tool" value { struct_value { fields { key: "attachmentId" value { string_value: "00000000-0000-4000-8000-000000000002" } } } } } } }
    }
    """
    Then the gRPC response should have status "ABORTED"

  Scenario: Retry an exact sequenced unarchive append while already active
    Given I archive the conversation
    And the response status should be 200
    And I am authenticated as agent with API key "test-agent-key"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry { channel: HISTORY content_type: "history" seq: 160 content { struct_value { fields { key: "role" value { string_value: "USER" } } fields { key: "text" value { string_value: "unarchive once" } } } } }
    conversation_patch { archived: false }
    """
    Then the gRPC response should not have an error
    And set "unarchiveRetryEntryId" to the gRPC response field "id"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry { channel: HISTORY content_type: "history" seq: 160 content { struct_value { fields { key: "role" value { string_value: "USER" } } fields { key: "text" value { string_value: "unarchive once" } } } } }
    conversation_patch { archived: false }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "id" should be "${unarchiveRetryEntryId}"

  Scenario: Retry unarchives an archived conversation without repeating append side effects
    Given "alice" is connected to the SSE event stream
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry { channel: HISTORY content_type: "history" seq: 170 content { struct_value { fields { key: "role" value { string_value: "USER" } } fields { key: "text" value { string_value: "stored before archive" } } } } }
    """
    Then the gRPC response should not have an error
    And set "grpcArchivedRetryEntryId" to the gRPC response field "id"
    And "alice" should receive an SSE event with kind "entry" and event "created"
    Given I am authenticated as user "alice"
    When I archive the conversation
    Then the response status should be 200
    And "alice" should receive an SSE event with kind "conversation" and event "updated"
    Given I am authenticated as agent with API key "test-agent-key"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry { channel: HISTORY content_type: "history" seq: 170 content { struct_value { fields { key: "role" value { string_value: "USER" } } fields { key: "text" value { string_value: "stored before archive" } } } } }
    conversation_patch { archived: false }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "id" should be "${grpcArchivedRetryEntryId}"
    And "alice" should receive an SSE event with kind "conversation" and event "updated"
    And the SSE event data "archived" should be "false"
    And "alice" should not receive an SSE event with kind "conversation" and event "updated" within 2 seconds
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 2 seconds

  Scenario: archived false on an active conversation emits no conversation update
    Given "alice" is connected to the SSE event stream
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry { channel: HISTORY content_type: "history" seq: 180 content { struct_value { fields { key: "role" value { string_value: "USER" } } fields { key: "text" value { string_value: "already active" } } } } }
    conversation_patch { archived: false }
    """
    Then the gRPC response should not have an error
    And "alice" should receive an SSE event with kind "entry" and event "created"
    And "alice" should not receive an SSE event with kind "conversation" and event "updated" within 2 seconds

  Scenario: An exact retry does not reapply an unchanged conversation patch
    Given I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: JOURNAL
      content_type: "agent/step"
      seq: 95
      content { string_value: "complete" }
    }
    conversation_patch {
      metadata {
        fields {
          key: "status"
          value { string_value: "done" }
        }
      }
    }
    """
    And the gRPC response should not have an error
    And I send gRPC request "ConversationsService/GetConversation" with body:
    """
    conversation_id: "${conversationId}"
    """
    And the gRPC response should not have an error
    And set "patchedUpdatedAt" to the gRPC response field "updatedAt"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: JOURNAL
      content_type: "agent/step"
      seq: 95
      content { string_value: "complete" }
    }
    conversation_patch {
      metadata {
        fields {
          key: "status"
          value { string_value: "done" }
        }
      }
    }
    """
    Then the gRPC response should not have an error
    When I send gRPC request "ConversationsService/GetConversation" with body:
    """
    conversation_id: "${conversationId}"
    """
    Then the gRPC response should not have an error
    And the gRPC response field "updatedAt" should be "${patchedUpdatedAt}"

  Scenario: Exact retry with unchanged nested metadata emits no duplicate updates
    Given "alice" is connected to the SSE event stream
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 190
      content {
        struct_value {
          fields { key: "role" value { string_value: "USER" } }
          fields { key: "text" value { string_value: "complete" } }
        }
      }
    }
    conversation_patch {
      metadata {
        fields {
          key: "state"
          value {
            struct_value {
              fields { key: "status" value { string_value: "done" } }
              fields {
                key: "steps"
                value { list_value { values { number_value: 1 } values { number_value: 2 } } }
              }
            }
          }
        }
        fields {
          key: "reviewers"
          value {
            list_value {
              values {
                struct_value {
                  fields { key: "name" value { string_value: "alice" } }
                  fields {
                    key: "roles"
                    value { list_value { values { string_value: "owner" } values { string_value: "editor" } } }
                  }
                }
              }
            }
          }
        }
      }
    }
    """
    Then the gRPC response should not have an error
    And set "grpcNestedMetadataRetryEntryId" to the gRPC response field "id"
    And "alice" should receive an SSE event with kind "entry" and event "created"
    And "alice" should receive an SSE event with kind "conversation" and event "updated"
    Given I am authenticated as user "alice"
    When I get the conversation
    Then the response status should be 200
    And set "grpcNestedMetadataUpdatedAt" to the json response field "updatedAt"
    Given I am authenticated as agent with API key "test-agent-key"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 190
      content {
        struct_value {
          fields { key: "role" value { string_value: "USER" } }
          fields { key: "text" value { string_value: "complete" } }
        }
      }
    }
    conversation_patch {
      metadata {
        fields {
          key: "reviewers"
          value {
            list_value {
              values {
                struct_value {
                  fields {
                    key: "roles"
                    value { list_value { values { string_value: "owner" } values { string_value: "editor" } } }
                  }
                  fields { key: "name" value { string_value: "alice" } }
                }
              }
            }
          }
        }
        fields {
          key: "state"
          value {
            struct_value {
              fields {
                key: "steps"
                value { list_value { values { number_value: 1 } values { number_value: 2 } } }
              }
              fields { key: "status" value { string_value: "done" } }
            }
          }
        }
      }
    }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "id" should be "${grpcNestedMetadataRetryEntryId}"
    And "alice" should not receive an SSE event with kind "conversation" and event "updated" within 2 seconds
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 2 seconds
    Given I am authenticated as user "alice"
    When I get the conversation
    Then the response status should be 200
    And the response body "updatedAt" should be "${grpcNestedMetadataUpdatedAt}"

  Scenario: Retry an exact append with archived true
    Given I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 96
      content {
        struct_value {
          fields { key: "role" value { string_value: "USER" } }
          fields { key: "text" value { string_value: "archive once" } }
        }
      }
    }
    conversation_patch { archived: true }
    """
    And the gRPC response should not have an error
    And set "archivedEntryId" to the gRPC response field "id"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 96
      content {
        struct_value {
          fields { key: "role" value { string_value: "USER" } }
          fields { key: "text" value { string_value: "archive once" } }
        }
      }
    }
    conversation_patch { archived: true }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "id" should be "${archivedEntryId}"
