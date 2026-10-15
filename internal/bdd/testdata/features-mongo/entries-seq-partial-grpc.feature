Feature: Mongo partial append recovery (GRPC)

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"
    And I have a conversation with title "Partial batch"
    And I call PATCH "/v1/conversations/${conversationId}" with body:
    """
    {"metadata":{"keep":"preserved","remove":"old"}}
    """
    And the response status should be 200

  Scenario: Completing an interrupted batch restores its original conversation patch
    Given the conversation was archived 100 days ago
    When I send gRPC request "EntriesService/AppendEntries" with body:
    """
    conversation_id: "${conversationId}"
    entries { channel: JOURNAL content_type: "test" seq: 220 content { string_value: "anchor" } }
    entries { channel: HISTORY content_type: "history" content { struct_value { fields { key: "role" value { string_value: "USER" } } fields { key: "text" value { string_value: "suffix" } } } } }
    conversation_patch { archived: false title: "Original patch" metadata { fields { key: "stage" value { string_value: "finished" } } fields { key: "remove" value { null_value: NULL_VALUE } } } }
    """
    Then the gRPC response should not have an error
    And set "anchorId" to the gRPC response field "entries[0].id"
    And set "suffixId" to the gRPC response field "entries[1].id"
    Given the Mongo append batch stopped after anchor "${anchorId}"
    And I call PATCH "/v1/conversations/${conversationId}" with body:
    """
    {"title":"Before interruption","metadata":{"stage":"before","remove":"old"}}
    """
    And the response status should be 200
    And the conversation was archived 100 days ago
    When I send gRPC request "EntriesService/AppendEntries" with body:
    """
    conversation_id: "${conversationId}"
    entries { channel: JOURNAL content_type: "test" seq: 220 content { string_value: "anchor" } }
    entries { channel: HISTORY content_type: "history" content { struct_value { fields { key: "role" value { string_value: "USER" } } fields { key: "text" value { string_value: "suffix" } } } } }
    conversation_patch { archived: false title: "Retry must not win" metadata { fields { key: "stage" value { string_value: "retry" } } } }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "entries[0].id" should be "${anchorId}"
    And the gRPC response field "entries[1].id" should be "${suffixId}"
    When I get the conversation
    Then the response status should be 200
    And the response body "archived" should be "false"
    And the response body "title" should be "Original patch"
    And the response body "metadata.stage" should be "finished"
    And the response body "metadata.keep" should be "preserved"
    And the response body should not contain "remove"
    And I call PATCH "/v1/conversations/${conversationId}" with body:
    """
    {"title":"Later edit","metadata":{"stage":"later","remove":"later"}}
    """
    And the response status should be 200
    Given I am authenticated as agent with API key "test-agent-key"
    When I call POST "/v1/conversations/${conversationId}/entries" with body:
    """
    {"channel":"HISTORY","contentType":"history","content":[{"role":"USER","text":"later"}]}
    """
    Then the response status should be 201
    Given the conversation was archived 100 days ago
    And I am authenticated as agent with API key "test-agent-key"
    When I send gRPC request "EntriesService/AppendEntries" with body:
    """
    conversation_id: "${conversationId}"
    entries { channel: JOURNAL content_type: "test" seq: 220 content { string_value: "anchor" } }
    entries { channel: HISTORY content_type: "history" content { struct_value { fields { key: "role" value { string_value: "USER" } } fields { key: "text" value { string_value: "suffix" } } } } }
    conversation_patch { archived: false title: "Original patch" metadata { fields { key: "stage" value { string_value: "finished" } } fields { key: "remove" value { null_value: NULL_VALUE } } } }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "entries[0].id" should be "${anchorId}"
    And the gRPC response field "entries[1].id" should be "${suffixId}"
    When I get the conversation
    Then the response status should be 200
    And the response body "archived" should be "true"
    And the response body "title" should be "Later edit"
    And the response body "metadata.stage" should be "later"
    And the response body "metadata.remove" should be "later"
