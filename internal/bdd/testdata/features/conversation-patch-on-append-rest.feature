Feature: Inline conversation patch on entry append and sync (REST)
  As an agent app
  I want to patch conversation metadata atomically with an entry append or sync
  So that status updates and entry writes are a single unit of work

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"
    And I have a conversation with title "Patch On Append Test"

  # ── Append endpoint ───────────────────────────────────────────────────────

  Scenario: conversationPatch sets metadata atomically with an append
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "llm_call"}],
      "conversationPatch": {
        "metadata": { "status": "running" }
      }
    }
    """
    Then the response status should be 201
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "metadata": { "status": "running" }
    }
    """

  Scenario: conversationPatch updates title atomically with an append
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Hello"}],
      "conversationPatch": {
        "title": "Renamed via Append"
      }
    }
    """
    Then the response status should be 201
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "title": "Renamed via Append"
    }
    """

  Scenario: conversationPatch with null metadata key removes that key
    Given I update the conversation with request:
    """
    { "metadata": { "status": "running", "keep": "yes" } }
    """
    And the response status should be 200
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "tool_call"}],
      "conversationPatch": {
        "metadata": { "status": null }
      }
    }
    """
    Then the response status should be 201
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "metadata": { "keep": "yes" }
    }
    """
    And the response body should not contain "status"

  Scenario: omitting conversationPatch leaves conversation unchanged
    Given I update the conversation with request:
    """
    { "metadata": { "status": "idle" } }
    """
    And the response status should be 200
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "No patch"}]
    }
    """
    Then the response status should be 201
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "metadata": { "status": "idle" }
    }
    """

  Scenario: conversationPatch absent keys are left unchanged (merge-patch)
    Given I update the conversation with request:
    """
    { "metadata": { "a": "1", "b": "2" } }
    """
    And the response status should be 200
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "llm_call"}],
      "conversationPatch": {
        "metadata": { "b": "updated" }
      }
    }
    """
    Then the response status should be 201
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "metadata": { "a": "1", "b": "updated" }
    }
    """

  # ── Sync endpoint ─────────────────────────────────────────────────────────

  Scenario: conversationPatch on sync sets metadata atomically
    When I call POST "/v1/conversations/${conversationId}/entries/sync" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "history/lc4j",
      "content": [{"type": "text", "text": "Synced context"}],
      "conversationPatch": {
        "metadata": { "status": "waiting" }
      }
    }
    """
    Then the response status should be 200
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "metadata": { "status": "waiting" }
    }
    """

  Scenario: conversationPatch on sync is a no-op when patch is null
    Given I update the conversation with request:
    """
    { "metadata": { "status": "idle" } }
    """
    And the response status should be 200
    When I call POST "/v1/conversations/${conversationId}/entries/sync" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "history/lc4j",
      "content": [{"type": "text", "text": "Synced context"}],
      "conversationPatch": null
    }
    """
    Then the response status should be 200
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "metadata": { "status": "idle" }
    }
    """

  Scenario: conversationPatch with archived:true archives the conversation atomically with an append
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "llm_call"}],
      "conversationPatch": {
        "archived": true
      }
    }
    """
    Then the response status should be 201
    When I call GET "/v1/conversations?archived=only&mode=all"
    Then the response status should be 200
    And the response body should contain "${conversationId}"

  Scenario: conversationPatch with archived and metadata combined applies both
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "llm_call"}],
      "conversationPatch": {
        "archived": true,
        "metadata": { "status": "done" }
      }
    }
    """
    Then the response status should be 201
    When I call GET "/v1/conversations?archived=only&mode=all"
    Then the response status should be 200
    And the response body should contain "${conversationId}"
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "metadata": { "status": "done" }
    }
    """

  Scenario: conversationPatch with archived:false unarchives the conversation atomically with an append
    Given I update the conversation with request:
    """
    { "archived": true }
    """
    And the response status should be 200
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "llm_call"}],
      "conversationPatch": {
        "archived": false
      }
    }
    """
    Then the response status should be 201
    When I call GET "/v1/conversations?mode=all"
    Then the response status should be 200
    And the response body should contain "${conversationId}"

  Scenario: conversationPatch empty object is a no-op with a valid history append
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Hello"}],
      "conversationPatch": {}
    }
    """
    Then the response status should be 201

  Scenario: conversationPatch with metadata null is a no-op with a valid history append
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Hello"}],
      "conversationPatch": {
        "metadata": null
      }
    }
    """
    Then the response status should be 201

  # ── Validation: invalid patches rejected before writes ───────────────────

  Scenario: conversationPatch with metadata containing dots in keys is accepted
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Hello"}],
      "conversationPatch": {
        "metadata": {"a.b": "value"}
      }
    }
    """
    Then the response status should be 201
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {
      "metadata": {"a.b": "value"}
    }
    """

  Scenario: conversationPatch with invalid title (not a string) is rejected before entry write
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Hello"}],
      "conversationPatch": {
        "title": 123
      }
    }
    """
    Then the response status should be 400
    When I list entries for the conversation
    Then the response status should be 200
    And the response should contain an empty list of entries

  Scenario: conversationPatch with invalid metadata (not an object) is rejected before entry write
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Hello"}],
      "conversationPatch": {
        "metadata": "not-an-object"
      }
    }
    """
    Then the response status should be 400
    When I list entries for the conversation
    Then the response status should be 200
    And the response should contain an empty list of entries

  Scenario: conversationPatch with invalid archived (not a boolean) is rejected before entry write
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Hello"}],
      "conversationPatch": {
        "archived": "yes"
      }
    }
    """
    Then the response status should be 400
    When I list entries for the conversation
    Then the response status should be 200
    And the response should contain an empty list of entries

  Scenario: conversationPatch validation on sync rejects invalid title before entry write
    When I call POST "/v1/conversations/${conversationId}/entries/sync" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "history/lc4j",
      "content": [{"type": "text", "text": "Synced"}],
      "conversationPatch": {
        "title": 123
      }
    }
    """
    Then the response status should be 400
    When I call GET "/v1/conversations/${conversationId}/entries?channel=context"
    Then the response status should be 200
    And the response should contain an empty list of entries
