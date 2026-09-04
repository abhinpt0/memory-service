Feature: Mongo sequenced retry archive retention (gRPC)

  Background:
    Given I am authenticated as user "alice"
    And I am authenticated as agent with API key "test-agent-key"
    And I have a conversation with title "Mongo Retry Archive gRPC"

  Scenario: A conflicting unarchive retry preserves the original archive timestamp
    Given I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 200
      content {
        struct_value {
          fields { key: "role" value { string_value: "USER" } }
          fields { key: "text" value { string_value: "original" } }
        }
      }
    }
    """
    And the gRPC response should not have an error
    And the conversation was archived 100 days ago
    When I execute MongoDB query:
    """
    {"collection":"conversation_groups","operation":"find","filter":{"_id":"${conversationGroupId}"},"projection":{"_id":1,"archived_at":1}}
    """
    Then the MongoDB result should have 1 row
    And set "originalGroupArchivedAt" to the json response field "[0].archived_at"
    Given I am authenticated as agent with API key "test-agent-key"
    When I send gRPC request "EntriesService/AppendEntry" with body:
    """
    conversation_id: "${conversationId}"
    entry {
      channel: HISTORY
      content_type: "history"
      seq: 200
      content {
        struct_value {
          fields { key: "role" value { string_value: "USER" } }
          fields { key: "text" value { string_value: "changed" } }
        }
      }
    }
    conversation_patch { archived: false }
    """
    Then the gRPC response should have status "ABORTED"
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
