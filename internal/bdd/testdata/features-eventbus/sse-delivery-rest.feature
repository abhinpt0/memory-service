Feature: Cross-node SSE event delivery
  An admin stream and a user stream each receive one event for a single write.

  Scenario: A conversation and an entry are delivered once to each stream
    Given I am authenticated as admin user "alice"
    And "alice" is connected to the admin SSE event stream with query "kinds=conversation,entry&entry_channels=history,journal&detail=full"
    And "bob" is connected to the SSE event stream with query "kinds=conversation,entry&entry_channels=history,journal&detail=full"
    And I am authenticated as user "bob"
    When I call POST "/v1/conversations" with body:
    """
    {"title":"SSE delivery"}
    """
    Then the response status should be 201
    And set "conversationId" to the json response field "id"
    And "bob" should receive an SSE event with kind "conversation" and event "created"
    And "alice" should receive an SSE event with kind "conversation" and event "created"
    And "alice" should not receive an SSE event with kind "conversation" and event "created" within 1 second
    And "bob" should not receive an SSE event with kind "conversation" and event "created" within 1 second

    Given I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel":"HISTORY",
      "contentType":"history",
      "content":[{"role":"USER","text":"One entry"}]
    }
    """
    Then the response status should be 201
    And "bob" should receive an SSE event with kind "entry" and event "created"
    And "alice" should receive an SSE event with kind "entry" and event "created"
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 1 second
    And "bob" should not receive an SSE event with kind "entry" and event "created" within 1 second
