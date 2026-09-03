Feature: Sequenced entry retry regressions
  Exact retries return the stored entry without repeating append-time side effects.

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"
    And I have a conversation with title "Retry regression target"

  Scenario: REST retry does not reapply a stale conversation patch
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"JOURNAL","contentType":"agent/step","seq":210,"conversationPatch":{"metadata":{"status":"pending"}},"content":[{"step":"queued"}]}
    """
    And the response status should be 201
    And set "stalePatchEntryId" to the json response field "id"
    And I update the conversation with request:
    """
    {"metadata":{"status":"done"}}
    """
    And the response status should be 200
    And I get the conversation
    And set "newerUpdatedAt" to the json response field "updatedAt"
    And "alice" is connected to the SSE event stream
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"JOURNAL","contentType":"agent/step","seq":210,"conversationPatch":{"metadata":{"status":"pending"}},"content":[{"step":"queued"}]}
    """
    Then the response status should be 201
    And the response body "id" should be "${stalePatchEntryId}"
    And "alice" should not receive an SSE event with kind "conversation" and event "updated" within 2 seconds
    When I get the conversation
    Then the response status should be 200
    And the response body "metadata.status" should be "done"
    And the response body "updatedAt" should be "${newerUpdatedAt}"

  Scenario: gRPC retry does not reapply a stale conversation patch
    Given I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: JOURNAL
      content_type: "agent/step"
      seq: 211
      content { string_value: "queued" }
    }
    conversation_patch {
      metadata { fields { key: "status" value { string_value: "pending" } } }
    }
    """
    And the gRPC response should not have an error
    And set "grpcStalePatchEntryId" to the gRPC response field "id"
    And I update the conversation with request:
    """
    {"metadata":{"status":"done"}}
    """
    And the response status should be 200
    And I get the conversation
    And set "grpcNewerUpdatedAt" to the json response field "updatedAt"
    And "alice" is connected to the SSE event stream
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: JOURNAL
      content_type: "agent/step"
      seq: 211
      content { string_value: "queued" }
    }
    conversation_patch {
      metadata { fields { key: "status" value { string_value: "pending" } } }
    }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "id" should be "${grpcStalePatchEntryId}"
    And "alice" should not receive an SSE event with kind "conversation" and event "updated" within 2 seconds
    When I get the conversation
    Then the response status should be 200
    And the response body "metadata.status" should be "done"
    And the response body "updatedAt" should be "${grpcNewerUpdatedAt}"

  Scenario: REST retry ignores creation lineage on an existing empty fork
    Given I have a conversation with title "Fork parent"
    And set "forkParentId" to "${conversationId}"
    And I call POST "/v1/conversations/${forkParentId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","content":[{"role":"USER","text":"fork anchor"}]}
    """
    And the response status should be 201
    And set "forkAnchorId" to the json response field "id"
    And set "emptyForkId" to "00000000-0000-4000-8000-000000000612"
    And I call POST "/v1/conversations/${emptyForkId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":212,"forkedAtConversationId":"${forkParentId}","forkedAtEntryId":"${forkAnchorId}","content":[{"role":"USER","text":"temporary first entry"}]}
    """
    And the response status should be 201
    And set "temporaryForkEntryId" to the json response field "id"
    And I execute SQL query:
    """
    DELETE FROM entries WHERE id = '${temporaryForkEntryId}' RETURNING id
    """
    And I have a conversation with title "Different lineage parent"
    And set "differentLineageParentId" to "${conversationId}"
    When I call POST "/v1/conversations/${emptyForkId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":213,"startedByConversationId":"${differentLineageParentId}","content":[{"role":"USER","text":"existing empty fork"}]}
    """
    Then the response status should be 201
    And set "ignoredLineageEntryId" to the json response field "id"
    When I call POST "/v1/conversations/${emptyForkId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":213,"startedByConversationId":"${differentLineageParentId}","content":[{"role":"USER","text":"existing empty fork"}]}
    """
    Then the response status should be 201
    And the response body "id" should be "${ignoredLineageEntryId}"

  Scenario: gRPC retry ignores creation lineage that was ignored by the initial append
    Given I have a conversation with title "gRPC lineage parent"
    And set "grpcLineageParentId" to "${conversationId}"
    And I have a conversation with title "Existing gRPC retry target"
    And set "existingGrpcRetryTargetId" to "${conversationId}"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${existingGrpcRetryTargetId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 213
      started_by_conversation_id: "${grpcLineageParentId}"
      content { struct_value {
        fields { key: "role" value { string_value: "USER" } }
        fields { key: "text" value { string_value: "existing target" } }
      } }
    }
    """
    Then the gRPC response should not have an error
    And set "grpcIgnoredLineageEntryId" to the gRPC response field "id"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${existingGrpcRetryTargetId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 213
      started_by_conversation_id: "${grpcLineageParentId}"
      content { struct_value {
        fields { key: "role" value { string_value: "USER" } }
        fields { key: "text" value { string_value: "existing target" } }
      } }
    }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "id" should be "${grpcIgnoredLineageEntryId}"
