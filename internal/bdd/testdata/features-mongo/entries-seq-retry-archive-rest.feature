Feature: Mongo sequenced retry archive retention (REST)

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"
    And I have a conversation with title "Mongo Retry Archive REST"

  Scenario: A conflicting unarchive retry preserves the original archive timestamp
    Given I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":200,"content":[{"role":"USER","text":"original"}]}
    """
    And the response status should be 201
    And the conversation was archived 100 days ago
    When I execute MongoDB query:
    """
    {"collection":"conversation_groups","operation":"find","filter":{"_id":"${conversationGroupId}"},"projection":{"_id":1,"archived_at":1}}
    """
    Then the MongoDB result should have 1 row
    And set "originalGroupArchivedAt" to the json response field "[0].archived_at"
    Given I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":200,"conversationPatch":{"archived":false},"content":[{"role":"USER","text":"changed"}]}
    """
    Then the response status should be 409
    Given I am authenticated as user "alice"
    When I get the conversation
    Then the response status should be 200
    And the response body "archived" should be "true"
    When I execute MongoDB query:
    """
    {"collection":"conversation_groups","operation":"find","filter":{"_id":"${conversationGroupId}"},"projection":{"_id":1,"archived_at":1}}
    """
    Then the MongoDB result should have 1 row
    And the MongoDB result at row 0 column "archived_at" should be "${originalGroupArchivedAt}"
