Feature: Client-assigned entry sequence (REST)
  As an agent client
  I want to assign sequence numbers to entries
  So that I can use a stable cursor for ordered replay

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"
    And I have a conversation with title "Seq Test Conversation"

  Scenario: Accept an entry with a seq value
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 1,
      "content": [{"type": "text", "text": "First"}]
    }
    """
    Then the response status should be 201
    And the response body should be json:
    """
    {
      "id": "${response.body.id}",
      "conversationId": "${conversationId}",
      "channel": "context",
      "contentType": "test.v1",
      "seq": 1,
      "epoch": 1,
      "content": [{"type": "text", "text": "First"}],
      "createdAt": "${response.body.createdAt}"
    }
    """

  Scenario: Reject a duplicate seq in the same conversation
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 5,
      "content": [{"type": "text", "text": "First"}]
    }
    """
    And the response status should be 201
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 5,
      "content": [{"type": "text", "text": "Duplicate"}]
    }
    """
    Then the response status should be 409

  Scenario: Return the stored entry for an exact retry after a later append
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 50,
      "content": [
        {"type": "text", "text": "Original first item"},
        {"type": "text", "text": "Original second item"}
      ]
    }
    """
    And the response status should be 201
    And set "originalEntryId" to the json response field "id"
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 51,
      "content": [{"type": "text", "text": "Later"}]
    }
    """
    And the response status should be 201
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 50,
      "content": [
        {"text": "Original first item", "type": "text"},
        {"text": "Original second item", "type": "text"}
      ]
    }
    """
    Then the response status should be 201
    And the response body "id" should be "${originalEntryId}"
    When I call GET "/v1/conversations/${conversationId}/entries?channel=context&fromSeq=50"
    Then the response status should be 200
    And the response should contain 2 entries
    Given I am authenticated as admin user "alice"
    When I call GET "/v1/admin/conversations/${conversationId}/entries?channel=context&fromSeq=50"
    Then the response status should be 200
    And the response should contain 2 entries

  Scenario: Retry a sequenced history entry that cross-references an attachment
    Given I am authenticated as agent with API key "test-agent-key"
    And the conversation exists
    And I upload a file "retry.txt" with content type "text/plain" and content "retry attachment"
    And the response status should be 201
    And set "retrySourceAttachmentId" to the json response field "id"
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","content":[{"role":"USER","text":"source","attachments":[{"attachmentId":"${retrySourceAttachmentId}","contentType":"text/plain","name":"retry.txt"}]}]}
    """
    And the response status should be 201
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":130,"content":[{"role":"USER","text":"reuse","attachments":[{"attachmentId":"${retrySourceAttachmentId}","contentType":"text/plain","name":"retry.txt"}]}]}
    """
    Then the response status should be 201
    And set "retryAttachmentEntryId" to the json response field "id"
    And set "retryCloneAttachmentId" to the json response field "content[0].attachments[0].attachmentId"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":130,"content":[{"role":"USER","text":"reuse","attachments":[{"attachmentId":"${retrySourceAttachmentId}","contentType":"text/plain","name":"retry.txt"}]}]}
    """
    Then the response status should be 201
    And the response body "id" should be "${retryAttachmentEntryId}"
    And the response body "content[0].attachments[0].attachmentId" should be "${retryCloneAttachmentId}"
    Given I am authenticated as admin user "alice"
    When I call GET "/v1/admin/attachments?userId=alice"
    Then the response status should be 200
    And the response body "data" should have 2 items

  Scenario: Return the stored entry when an auto-create retry changes creation-only lineage
    Given I am authenticated as agent with API key "test-agent-key"
    And set "firstRetryParentId" to "${conversationId}"
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","content":[{"role":"USER","text":"first parent"}]}
    """
    And the response status should be 201
    And set "firstParentEntryId" to the json response field "id"
    And I have a conversation with title "Other retry parent"
    And set "otherRetryParentId" to the json response field "id"
    And I call POST "/v1/conversations/${otherRetryParentId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","content":[{"role":"USER","text":"other parent"}]}
    """
    And the response status should be 201
    And set "otherRetryParentEntryId" to the json response field "id"
    And set "retryForkConversationId" to "00000000-0000-4000-8000-000000000528"
    And I call POST "/v1/conversations/${retryForkConversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":140,"forkedAtConversationId":"${firstRetryParentId}","forkedAtEntryId":"${firstParentEntryId}","content":[{"role":"USER","text":"fork continuation"}]}
    """
    And the response status should be 201
    And set "retryForkEntryId" to the json response field "id"
    When I call POST "/v1/conversations/${retryForkConversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":140,"forkedAtConversationId":"${otherRetryParentId}","forkedAtEntryId":"${otherRetryParentEntryId}","content":[{"role":"USER","text":"fork continuation"}]}
    """
    Then the response status should be 201
    And the response body "id" should be "${retryForkEntryId}"

  Scenario: Arbitrary attachmentId keys do not use history attachment equivalence
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"CONTEXT","contentType":"tool.v1","seq":150,"content":[{"tool":{"attachmentId":"00000000-0000-4000-8000-000000000001"}}]}
    """
    And the response status should be 201
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"CONTEXT","contentType":"tool.v1","seq":150,"content":[{"tool":{"attachmentId":"00000000-0000-4000-8000-000000000002"}}]}
    """
    Then the response status should be 409

  Scenario: Retry an exact sequenced unarchive append while already active
    Given I archive the conversation
    And the response status should be 200
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":160,"conversationPatch":{"archived":false},"content":[{"role":"USER","text":"unarchive once"}]}
    """
    Then the response status should be 201
    And set "unarchiveRetryEntryId" to the json response field "id"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":160,"conversationPatch":{"archived":false},"content":[{"role":"USER","text":"unarchive once"}]}
    """
    Then the response status should be 201
    And the response body "id" should be "${unarchiveRetryEntryId}"

  Scenario: Allow the same seq in a different conversation
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 7,
      "content": [{"type": "text", "text": "Entry in conv-a"}]
    }
    """
    And the response status should be 201
    And I have a conversation with title "Other Seq Conversation"
    Then set "otherConvId" to the json response field "id"
    When I call POST "/v1/conversations/${otherConvId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 7,
      "content": [{"type": "text", "text": "Entry in conv-b"}]
    }
    """
    Then the response status should be 201

  Scenario: fromSeq returns seq-ordered entries and excludes null-seq entries
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "content": [{"type": "text", "text": "No seq"}]
    }
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 30,
      "content": [{"type": "text", "text": "Seq 30"}]
    }
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 10,
      "content": [{"type": "text", "text": "Seq 10"}]
    }
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 20,
      "content": [{"type": "text", "text": "Seq 20"}]
    }
    """
    And the response status should be 201
    When I call GET "/v1/conversations/${conversationId}/entries?channel=context&fromSeq=10"
    Then the response status should be 200
    And the response should contain 3 entries
    And the response body should be json:
    """
    {
      "afterCursor": null,
      "data": [
        {"seq": 10, "content": [{"type": "text", "text": "Seq 10"}]},
        {"seq": 20, "content": [{"type": "text", "text": "Seq 20"}]},
        {"seq": 30, "content": [{"type": "text", "text": "Seq 30"}]}
      ]
    }
    """

  Scenario: fromSeq filters with minimum threshold
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 10,
      "content": [{"type": "text", "text": "Seq 10"}]
    }
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 20,
      "content": [{"type": "text", "text": "Seq 20"}]
    }
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 30,
      "content": [{"type": "text", "text": "Seq 30"}]
    }
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "seq": 40,
      "content": [{"type": "text", "text": "Seq 40"}]
    }
    """
    And the response status should be 201
    When I call GET "/v1/conversations/${conversationId}/entries?channel=context&fromSeq=25"
    Then the response status should be 200
    And the response should contain 2 entries
    And the response body should be json:
    """
    {
      "afterCursor": null,
      "data": [
        {"seq": 30, "content": [{"type": "text", "text": "Seq 30"}]},
        {"seq": 40, "content": [{"type": "text", "text": "Seq 40"}]}
      ]
    }
    """

  Scenario: Omitting fromSeq preserves default created_at ordering
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel": "CONTEXT", "contentType": "test.v1", "seq": 100, "content": [{"type": "text", "text": "High seq first"}]}
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel": "CONTEXT", "contentType": "test.v1", "content": [{"type": "text", "text": "No seq second"}]}
    """
    And the response status should be 201
    When I call GET "/v1/conversations/${conversationId}/entries?channel=context&epoch=all"
    Then the response status should be 200
    And the response should contain 2 entries

  Scenario: Default timestamp ties use null seq first then seq order
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel": "HISTORY", "contentType": "history", "seq": 20, "content": [{"role": "USER", "text": "Seq 20"}]}
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel": "HISTORY", "contentType": "history", "content": [{"role": "USER", "text": "No seq"}]}
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel": "HISTORY", "contentType": "history", "seq": 10, "content": [{"role": "USER", "text": "Seq 10"}]}
    """
    And the response status should be 201
    And the conversation entries share the same createdAt timestamp
    When I call GET "/v1/conversations/${conversationId}/entries?channel=history"
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "afterCursor": null,
      "data": [
        {"content": [{"role": "USER", "text": "No seq"}]},
        {"seq": 10, "content": [{"role": "USER", "text": "Seq 10"}]},
        {"seq": 20, "content": [{"role": "USER", "text": "Seq 20"}]}
      ]
    }
    """
