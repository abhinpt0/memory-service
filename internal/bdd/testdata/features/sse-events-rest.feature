Feature: SSE Event Stream
  As a frontend application
  I want to receive real-time events via Server-Sent Events
  So that I can invalidate caches and update the UI when server-side changes occur

  Background:
    Given I am authenticated as user "alice"

  Scenario: Receive conversation created event
    Given "alice" is connected to the SSE event stream
    When I call POST "/v1/conversations" with body:
    """
    {
      "title": "SSE Test Conversation"
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "conversation" and event "created"
    And the SSE event data should contain "conversation"
    And the SSE event data should contain "conversation_group"

  Scenario: Receive conversation updated event
    Given I have a conversation with title "Update Test"
    And "alice" is connected to the SSE event stream
    When I call PATCH "/v1/conversations/${conversationId}" with body:
    """
    {
      "title": "Updated Title"
    }
    """
    Then the response status should be 200
    And "alice" should receive an SSE event with kind "conversation" and event "updated"
    And the SSE event data should contain "conversation"

  Scenario: Receive conversation archived event
    Given I have a conversation with title "Archive Test"
    And "alice" is connected to the SSE event stream
    When I archive the conversation
    Then the response status should be 200
    And "alice" should receive an SSE event with kind "conversation" and event "updated"
    And the SSE event data should contain "conversation"

  Scenario: Receive entry created event
    Given I have a conversation with title "Entry Test"
    And "alice" is connected to the SSE event stream
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Hello from SSE test"}]
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created"
    And the SSE event data should contain "conversation"
    And the SSE event data should contain "conversation_group"
    And the SSE event data should contain "entry"
    And the SSE event data "entry_channel" should be "history"
    And the SSE event data "entry_content_type" should be "history"
    And the SSE event data "entry_role" should be "USER"

  Scenario: Retry unarchives an archived conversation with one update event and no entry event
    Given I have a conversation with title "Sequenced Unarchive Retry Events"
    And "alice" is connected to the SSE event stream
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":170,"content":[{"role":"USER","text":"stored before archive"}]}
    """
    Then the response status should be 201
    And set "archivedRetryEntryId" to the json response field "id"
    And "alice" should receive an SSE event with kind "entry" and event "created"
    Given I am authenticated as user "alice"
    When I archive the conversation
    Then the response status should be 200
    And "alice" should receive an SSE event with kind "conversation" and event "updated"
    Given I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":170,"conversationPatch":{"archived":false},"content":[{"role":"USER","text":"stored before archive"}]}
    """
    Then the response status should be 201
    And the response body "id" should be "${archivedRetryEntryId}"
    And "alice" should receive an SSE event with kind "conversation" and event "updated"
    And the SSE event data "archived" should be "false"
    And "alice" should not receive an SSE event with kind "conversation" and event "updated" within 2 seconds
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 2 seconds

  Scenario: archived false on an active conversation emits updates only for other patch changes
    Given I have a conversation with title "Active Conversation Patch Events"
    And "alice" is connected to the SSE event stream
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":180,"conversationPatch":{"archived":false},"content":[{"role":"USER","text":"already active"}]}
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created"
    And "alice" should not receive an SSE event with kind "conversation" and event "updated" within 2 seconds
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":181,"conversationPatch":{"archived":false,"title":"Active Conversation Renamed","metadata":{"retryState":"active"}},"content":[{"role":"USER","text":"patch active"}]}
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created"
    And "alice" should receive an SSE event with kind "conversation" and event "updated"
    Given I am authenticated as user "alice"
    When I get the conversation
    Then the response status should be 200
    And the response body should be json:
    """
    {"title":"Active Conversation Renamed","metadata":{"retryState":"active"}}
    """

  Scenario: Exact retry with unchanged nested metadata emits no duplicate updates
    Given I have a conversation with title "Nested Metadata Retry Events"
    And "alice" is connected to the SSE event stream
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":190,"conversationPatch":{"metadata":{"state":{"status":"done","steps":[1,2]},"reviewers":[{"name":"alice","roles":["owner","editor"]}]}},"content":[{"role":"USER","text":"complete"}]}
    """
    Then the response status should be 201
    And set "nestedMetadataRetryEntryId" to the json response field "id"
    And "alice" should receive an SSE event with kind "entry" and event "created"
    And "alice" should receive an SSE event with kind "conversation" and event "updated"
    Given I am authenticated as user "alice"
    When I get the conversation
    Then the response status should be 200
    And set "nestedMetadataUpdatedAt" to the json response field "updatedAt"
    Given I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","seq":190,"conversationPatch":{"metadata":{"state":{"steps":[1.0,2e0],"status":"done"},"reviewers":[{"roles":["owner","editor"],"name":"alice"}]}},"content":[{"role":"USER","text":"complete"}]}
    """
    Then the response status should be 201
    And the response body "id" should be "${nestedMetadataRetryEntryId}"
    And "alice" should not receive an SSE event with kind "conversation" and event "updated" within 2 seconds
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 2 seconds
    Given I am authenticated as user "alice"
    When I get the conversation
    Then the response status should be 200
    And the response body "updatedAt" should be "${nestedMetadataUpdatedAt}"

  Scenario: An exact sequenced retry does not emit another entry event
    Given I have a conversation with title "Sequenced Retry Events"
    And "alice" is connected to the SSE event stream with query "kinds=entry&entry_channels=history"
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "seq": 528,
      "content": [{"role": "USER", "text": "Commit once"}]
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "seq": 528,
      "content": [{"role": "USER", "text": "Commit once"}]
    }
    """
    Then the response status should be 201
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 2 seconds

  Scenario: Entry event stream defaults to history entries
    Given I have a conversation with title "Entry Filter Defaults"
    And "alice" is connected to the SSE event stream filtered to kinds "entry"
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "content": [{"type": "text", "text": "Internal context"}]
    }
    """
    Then the response status should be 201
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 2 seconds
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Visible history"}]
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created" where data "entry_channel" is "history"

  Scenario: Entry event stream can opt into context entries
    Given I have a conversation with title "Entry Context Filter"
    And "alice" is connected to the SSE event stream with query "kinds=entry&entry_channels=context"
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "test.v1",
      "content": [{"type": "text", "text": "Subscribed context"}]
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created" where data "entry_channel" is "context"

  Scenario: Entry event stream filters by content type and role
    Given I have a conversation with title "Entry Metadata Filter"
    And "alice" is connected to the SSE event stream with query "kinds=entry&entry_channels=history&entry_content_types=history/lc4j&entry_roles=AI"
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Filtered user history"}]
    }
    """
    Then the response status should be 201
    And "alice" should not receive an SSE event with kind "entry" and event "created" within 2 seconds
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history/lc4j",
      "content": [{"role": "AI", "text": "Matched AI history"}]
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created" where data "entry_role" is "AI"

  Scenario: Sync-created context entries are emitted and filterable
    Given I have a conversation with title "Sync Context Events"
    And "alice" is connected to the SSE event stream with query "kinds=entry&entry_channels=context"
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries/sync" with body:
    """
    {
      "channel": "CONTEXT",
      "contentType": "history/lc4j",
      "content": [{"type": "text", "text": "Synced context"}]
    }
    """
    Then the response status should be 200
    And "alice" should receive an SSE event with kind "entry" and event "created" where data "entry_channel" is "context"

  Scenario: Events are filtered by access — no leakage
    Given I have a conversation with title "Private Conversation"
    And "bob" is connected to the SSE event stream
    And I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Secret message"}]
    }
    """
    Then the response status should be 201
    And "bob" should not receive any SSE event within 2 seconds

  Scenario: Receive events after being granted access
    Given I have a conversation with title "Shared Conversation"
    And "bob" is connected to the SSE event stream
    When I share the conversation with user "bob" with request:
    """
    {
      "userId": "bob",
      "accessLevel": "reader"
    }
    """
    Then the response status should be 201
    And "bob" should receive an SSE event with kind "membership" and event "created"
    # Now bob should receive events for this conversation
    Given I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Shared message"}]
    }
    """
    Then the response status should be 201
    And "bob" should receive an SSE event with kind "entry" and event "created"

  Scenario: Stop receiving events after access revoked
    Given I have a conversation with title "Revoke Test"
    And I share the conversation with user "bob" and access level "reader"
    And "bob" is connected to the SSE event stream
    When I delete membership for user "bob"
    Then the response status should be 204
    And "bob" should receive an SSE event with kind "membership" and event "deleted"
    # Now bob should NOT receive events for this conversation
    Given I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "After revoke"}]
    }
    """
    Then the response status should be 201
    And "bob" should not receive any SSE event within 2 seconds

  Scenario: Filter events by kind
    Given I have a conversation with title "Kind Filter Test"
    And "alice" is connected to the SSE event stream filtered to kinds "entry"
    When I call PATCH "/v1/conversations/${conversationId}" with body:
    """
    {
      "title": "Updated Kind Filter"
    }
    """
    Then the response status should be 200
    # conversation update should be filtered out; entry append should come through
    Given I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {
      "channel": "HISTORY",
      "contentType": "history",
      "content": [{"role": "USER", "text": "Kind filtered"}]
    }
    """
    Then the response status should be 201
    And "alice" should receive an SSE event with kind "entry" and event "created"

  Scenario: Admin SSE endpoint streams without justification
    Given I am authenticated as admin user "alice"
    And "alice" is connected to the admin SSE event stream with query ""
    And I am authenticated as user "bob"
    And I have a conversation with title "Admin Visibility Without Justification"
    Then "alice" should receive an SSE event with kind "conversation" and event "created"

  Scenario: Admin SSE endpoint streams all events
    Given I am authenticated as admin user "alice"
    And "alice" is connected to the admin SSE event stream with justification "BDD test"
    And I am authenticated as user "bob"
    And I have a conversation with title "Admin Visibility"
    Then "alice" should receive an SSE event with kind "conversation" and event "created"

  Scenario: Membership updated event
    Given I have a conversation with title "Membership Update Test"
    And I share the conversation with user "bob" with request:
    """
    {
      "userId": "bob",
      "accessLevel": "reader"
    }
    """
    And "bob" is connected to the SSE event stream
    When I update membership for user "bob" with request:
    """
    {
      "accessLevel": "writer"
    }
    """
    Then the response status should be 200
    And "bob" should receive an SSE event with kind "membership" and event "updated"
    And the SSE event data should contain "role"
