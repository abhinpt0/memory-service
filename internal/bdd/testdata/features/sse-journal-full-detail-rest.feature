Feature: SSE event stream with journal channel and full detail (REST)
  As a backend agent consumer
  I want to subscribe to SSE events for journal channel entries with detail=full
  So that I can receive complete entry payloads without a follow-up fetch

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"
    And I have a conversation with title "Journal SSE Test Conversation"

  Scenario: entry_channels=journal delivers journal entries on SSE stream
    Given "alice" is connected to the SSE event stream with query "kinds=entry&entry_channels=journal"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "llm_call", "model": "gpt-4"}]
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created" where data "entry_channel" is "journal"
    And the SSE event data "entry_content_type" should be "onering-journal-v1"

  Scenario: entry_channels=journal does not deliver history entries
    Given "alice" is connected to the SSE event stream with query "kinds=entry&entry_channels=journal"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Hello from history"}]
    }
    """
    Then the response status should be 201
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 2 seconds

  Scenario: detail=full delivers complete entry object for journal entries
    Given "alice" is connected to the SSE event stream with query "kinds=entry&entry_channels=journal&detail=full"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "tool_call", "tool": "search"}]
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created" where data "entry_channel" is "journal"
    And the SSE event data should contain "entry"
    And the SSE event data should contain "conversation"

  Scenario: entry_channels=history,journal delivers both channels
    Given "alice" is connected to the SSE event stream with query "kinds=entry&entry_channels=history,journal"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "llm_call"}]
    }
    """
    And the response status should be 201
    And I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Visible message"}]
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created" where data "entry_channel" is "journal"
    And "alice" should receive an SSE event with kind "entry" and event "created" where data "entry_channel" is "history"

  Scenario: Default SSE stream does not deliver journal entries
    Given "alice" is connected to the SSE event stream
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "journal",
      "contentType": "onering-journal-v1",
      "content": [{"stepType": "llm_call"}]
    }
    """
    Then the response status should be 201
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 2 seconds
