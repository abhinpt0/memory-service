@requires-embedded
Feature: SSE event stream over Unix domain socket
  Applications embedding memory-service on a local Unix socket
  can receive real-time SSE events without a bearer token,
  because local-socket auth implicitly identifies the caller.

  Scenario: SSE events are delivered over the Unix domain socket
    Given I send gRPC request "ConversationsService/CreateConversation" with body:
    """
    title: "UDS SSE Test Conversation"
    """
    And the gRPC response should not have an error
    And set "conversationId" to the gRPC response field "id"
    And "local" is connected to the SSE event stream via the Unix socket
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      content {
        struct_value {
          fields {
            key: "role"
            value { string_value: "USER" }
          }
          fields {
            key: "text"
            value { string_value: "hello from UDS" }
          }
        }
      }
    }
    """
    Then the gRPC response should not have an error
    And "local" should receive an SSE event with kind "entry" and event "created" within 5 seconds

  Scenario: Multiple entries each produce an SSE event over the Unix domain socket
    Given I send gRPC request "ConversationsService/CreateConversation" with body:
    """
    title: "UDS SSE Multi-entry Conversation"
    """
    And the gRPC response should not have an error
    And set "conversationId" to the gRPC response field "id"
    And "local" is connected to the SSE event stream via the Unix socket
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      content {
        struct_value {
          fields {
            key: "role"
            value { string_value: "USER" }
          }
          fields {
            key: "text"
            value { string_value: "first message" }
          }
        }
      }
    }
    """
    Then the gRPC response should not have an error
    And "local" should receive an SSE event with kind "entry" and event "created" within 5 seconds
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      content {
        struct_value {
          fields {
            key: "role"
            value { string_value: "AI" }
          }
          fields {
            key: "text"
            value { string_value: "second message" }
          }
        }
      }
    }
    """
    Then the gRPC response should not have an error
    And "local" should receive an SSE event with kind "entry" and event "created" within 5 seconds
