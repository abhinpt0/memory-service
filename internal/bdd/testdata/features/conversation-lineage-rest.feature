Feature: Conversation lineage across forks
  As an agent application
  I want child lineage to survive conversation forks
  So that child conversations remain discoverable as their work branches

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"

  Scenario: Forks of one child retain one logical relationship to the parent
    Given I have a conversation with title "Parent conversation"
    And set "parentConversationId" to "${conversationId}"
    And I append an entry to the conversation:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Delegate this work"}]
    }
    """
    And set "parentEntryId" to the json response field "id"
    And set "childConversationId" to "00000000-0000-4000-8000-000000000701"
    When I call POST "/v1/conversations/${childConversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "startedByConversationId": "${parentConversationId}",
      "startedByEntryId": "${parentEntryId}",
      "content": [{"role": "USER", "text": "Child work"}]
    }
    """
    Then the response status should be 201
    And set "childEntryId" to the json response field "id"
    And set "conversationId" to "${childConversationId}"
    And I append an entry to the conversation:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "AI", "text": "Child continuation"}]
    }
    """
    And set "childForkPointEntryId" to the json response field "id"
    When I fork conversation "${childConversationId}" at entry "${childForkPointEntryId}" with request:
    """
    {}
    """
    And set "childFork1Id" to "${forkedConversationId}"
    When I fork conversation "${childConversationId}" at entry "${childForkPointEntryId}" with request:
    """
    {}
    """
    And set "childFork2Id" to "${forkedConversationId}"

    When I call GET "/v1/conversations/${childConversationId}"
    Then the response status should be 200
    And the response body field "startedByConversationId" should be "${parentConversationId}"
    And the response body field "startedByEntryId" should be "${parentEntryId}"
    When I call GET "/v1/conversations/${childFork1Id}"
    Then the response status should be 200
    And the response body field "startedByConversationId" should be "${parentConversationId}"
    And the response body field "startedByEntryId" should be "${parentEntryId}"
    And the response body field "forkedAtConversationId" should be "${childConversationId}"
    When I call GET "/v1/conversations/${childFork2Id}"
    Then the response status should be 200
    And the response body field "startedByConversationId" should be "${parentConversationId}"
    And the response body field "startedByEntryId" should be "${parentEntryId}"
    And the response body field "forkedAtConversationId" should be "${childConversationId}"

    When I call GET "/v1/conversations/${parentConversationId}/children?limit=20"
    Then the response status should be 200
    And the response should contain 1 conversation
    And the response body field "data[0].id" should be "${childConversationId}"
    And the response body field "data[0].startedByConversationId" should be "${parentConversationId}"
    And the response body field "data[0].startedByEntryId" should be "${parentEntryId}"

    When I call GET "/v1/conversations?ancestry=children&mode=all"
    Then the response status should be 200
    And the response should contain 3 conversations
    And the response body should contain "${childConversationId}"
    And the response body should contain "${childFork1Id}"
    And the response body should contain "${childFork2Id}"
    When I call GET "/v1/conversations?ancestry=children&mode=latest-fork"
    Then the response status should be 200
    And the response should contain 1 conversation
    And the response body field "data[0].id" should be "${childFork2Id}"

    When I call GET "/v1/conversations/${childFork1Id}/forks"
    Then the response status should be 200
    And the response body "conversationIds" should have 3 items
    And the response body "forkPoints" should have 1 item
    And the response body "forkPoints[0].options" should have 3 items
    And the response body should contain "${childConversationId}"
    And the response body should contain "${childFork1Id}"
    And the response body should contain "${childFork2Id}"

    When I call GET "/v1/conversations?ancestry=roots&mode=all"
    Then the response status should be 200
    And the response should contain 1 conversation
    And the response body field "data[0].id" should be "${parentConversationId}"

  Scenario: A nested child's forks retain its immediate child parent without an invented source entry
    Given I have a conversation with title "Root conversation"
    And set "rootConversationId" to "${conversationId}"
    And set "childConversationId" to "00000000-0000-4000-8000-000000000711"
    When I call POST "/v1/conversations/${childConversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "startedByConversationId": "${rootConversationId}",
      "content": [{"role": "USER", "text": "Child without source entry"}]
    }
    """
    Then the response status should be 201
    And set "childEntryId" to the json response field "id"
    And set "conversationId" to "${childConversationId}"
    And I append an entry to the conversation:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "AI", "text": "Child continuation"}]
    }
    """
    And set "childForkPointEntryId" to the json response field "id"
    When I fork conversation "${childConversationId}" at entry "${childForkPointEntryId}" with request:
    """
    {}
    """
    And set "childForkId" to "${forkedConversationId}"
    When I call GET "/v1/conversations/${childForkId}"
    Then the response status should be 200
    And the response body field "startedByConversationId" should be "${rootConversationId}"
    And the response body should not contain "startedByEntryId"

    And set "nestedConversationId" to "00000000-0000-4000-8000-000000000712"
    When I call POST "/v1/conversations/${nestedConversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "startedByConversationId": "${childForkId}",
      "startedByEntryId": "${childEntryId}",
      "content": [{"role": "USER", "text": "Nested child work"}]
    }
    """
    Then the response status should be 201
    And set "nestedEntryId" to the json response field "id"
    When I fork conversation "${nestedConversationId}" at entry "${nestedEntryId}" with request:
    """
    {}
    """
    And set "nestedForkId" to "${forkedConversationId}"
    When I call GET "/v1/conversations/${nestedForkId}"
    Then the response status should be 200
    And the response body field "startedByConversationId" should be "${childForkId}"
    And the response body field "startedByEntryId" should be "${childEntryId}"
    When I call GET "/v1/conversations/${childForkId}/children?limit=20"
    Then the response status should be 200
    And the response should contain 1 conversation
    And the response body field "data[0].id" should be "${nestedConversationId}"
    When I call GET "/v1/conversations/${nestedForkId}/forks"
    Then the response status should be 200
    And the response body "conversationIds" should have 2 items
    And the response body should contain "${nestedConversationId}"
    And the response body should contain "${nestedForkId}"
