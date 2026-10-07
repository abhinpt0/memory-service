Feature: Mongo partial append recovery (REST)

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"
    And I have a conversation with title "Partial batch"

  Scenario: Completing an interrupted unarchive batch applies unarchive once
    Given the conversation was archived 100 days ago
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"entries":[{"channel":"JOURNAL","contentType":"test","seq":220,"content":"anchor"},{"channel":"HISTORY","contentType":"history","content":[{"role":"USER","text":"suffix"}]}],"conversationPatch":{"archived":false}}
    """
    Then the response status should be 201
    And set "anchorId" to the json response field "[0].id"
    And set "suffixId" to the json response field "[1].id"
    Given the Mongo append batch stopped after anchor "${anchorId}"
    And the conversation was archived 100 days ago
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"entries":[{"channel":"JOURNAL","contentType":"test","seq":220,"content":"anchor"},{"channel":"HISTORY","contentType":"history","content":[{"role":"USER","text":"suffix"}]}],"conversationPatch":{"archived":false}}
    """
    Then the response status should be 201
    And the response body "[0].id" should be "${anchorId}"
    And the response body "[1].id" should be "${suffixId}"
    When I get the conversation
    Then the response status should be 200
    And the response body "archived" should be "false"
    Given I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","content":[{"role":"USER","text":"later"}]}
    """
    Then the response status should be 201
    Given the conversation was archived 100 days ago
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"entries":[{"channel":"JOURNAL","contentType":"test","seq":220,"content":"anchor"},{"channel":"HISTORY","contentType":"history","content":[{"role":"USER","text":"suffix"}]}],"conversationPatch":{"archived":false}}
    """
    Then the response status should be 201
    And the response body "[0].id" should be "${anchorId}"
    And the response body "[1].id" should be "${suffixId}"
    When I get the conversation
    Then the response status should be 200
    And the response body "archived" should be "true"
