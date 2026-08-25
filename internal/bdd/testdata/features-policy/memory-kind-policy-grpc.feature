Feature: Episodic Memory Kind Policy gRPC
  As an operator with a custom OPA policy
  I want the authz and filter policies to work correctly via gRPC
  So that kind-based access control and narrowing work on the gRPC surface

  Background:
    Given I am authenticated as user "alice"

  # -----------------------------------------------------------------------
  # Authz: input.kind carries resolved exact kind on gRPC write/read/update.
  #
  # Custom authz.rego rules:
  #   user/<uid>/authz-custom/... : requires input.kind == "authz/v1"
  #   user/<uid>/... (other)      : requires input.kind == "default/v1"
  #   user/<uid>/denied-ns/...    : always denied
  # -----------------------------------------------------------------------

  Scenario: gRPC PutMemory carries exact kind in response
    When I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-kind-test"
    key: "mem-with-kind"
    value {
      fields {
        key: "x"
        value { number_value: 1 }
      }
    }
    kind: "default/v1"
    """
    Then the gRPC response should not have an error
    And the gRPC response field "kind" should be "default/v1"

  Scenario: gRPC PutMemory without explicit kind resolves to non-empty default
    # Omitting kind resolves to "default/v1"; authz.rego requires input.kind == "default/v1"
    # for ordinary namespaces. If input.kind were empty, the rule would not fire.
    When I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-default-kind"
    key: "no-explicit-kind"
    value {
      fields {
        key: "y"
        value { number_value: 2 }
      }
    }
    """
    Then the gRPC response should not have an error
    And the gRPC response field "kind" should be "default/v1"

  Scenario: gRPC PutMemory rejects a family-only kind
    When I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-family-only-kind"
    key: "invalid-kind"
    value {
      fields {
        key: "y"
        value { number_value: 2 }
      }
    }
    kind: "default"
    """
    Then the gRPC response should have status "INVALID_ARGUMENT"

  Scenario: gRPC authz-deny blocks write for denied namespace
    When I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "denied-ns"
    key: "any-key"
    value {
      fields {
        key: "z"
        value { number_value: 3 }
      }
    }
    """
    Then the gRPC response should have status "PERMISSION_DENIED"

  Scenario: gRPC Put cannot replace an existing kind the caller may not modify
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=replace-authz" with body:
    """
    {"name":"authz/v1","attributes":{"note":"string"},"projectionRego":"package memories.attributes\nattributes := {\"note\": input.value.note}"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=replace-authz" with body:
    """
    {"namespace":["user","bob","grpc-replace-protected"],"key":"same-key","value":{"note":"protected"},"kind":"authz/v1"}
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "grpc-replace-protected"
    key: "same-key"
    kind: "default/v1"
    value { fields { key: "note" value { string_value: "replacement" } } }
    """
    Then the gRPC response should have status "PERMISSION_DENIED"
    Given I am authenticated as admin user "alice"
    When I call GET "/admin/v1/memories?namespacePrefix=user&namespacePrefix=bob&namespacePrefix=grpc-replace-protected&justification=replace-authz"
    Then the response status should be 200
    And the response body should contain "protected"
    And the response body should contain "authz/v1"
    And the response body should not contain "replacement"

  Scenario: gRPC archived reads authorize the exact selected historical row kind
    Given I am authenticated as user "bob"
    And I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "grpc-archived-kind-read"
    key: "allowed"
    kind: "default/v1"
    value { fields { key: "note" value { string_value: "allowed" } } }
    """
    And the gRPC response should not have an error
    And I send gRPC request "MemoriesService/UpdateMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "grpc-archived-kind-read"
    key: "allowed"
    archived: true
    """
    And the gRPC response should not have an error
    When I send gRPC request "MemoriesService/GetMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "grpc-archived-kind-read"
    key: "allowed"
    archived: ARCHIVE_FILTER_ONLY
    """
    Then the gRPC response should not have an error
    And the gRPC response field "kind" should be "default/v1"

  Scenario: gRPC authz-custom namespace: write with authz/v1 is allowed
    # Create "authz/v1" schema via admin REST, then write to authz-custom namespace
    # with that kind — the authz rule fires for authz-custom + authz/v1.
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds" with body:
    """
    {
      "name": "authz/v1",
      "attributes": {"note": "string"},
      "projectionRego": "package memories.attributes\nattributes := {\"note\": input.value.note}"
    }
    """
    And the response status should be 200
    And I am authenticated as user "alice"
    When I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "authz-custom"
    key: "grpc-custom-write"
    value {
      fields {
        key: "note"
        value { string_value: "authz/v1 write" }
      }
    }
    kind: "authz/v1"
    """
    Then the gRPC response should not have an error
    And the gRPC response field "kind" should be "authz/v1"

  Scenario: gRPC authz-custom namespace: write with wrong kind is denied
    # Trying to write to authz-custom with default/v1 is denied because the
    # authz-custom rule in authz.rego requires input.kind == "authz/v1".
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds" with body:
    """
    {
      "name": "authz/v1",
      "attributes": {"note": "string"},
      "projectionRego": "package memories.attributes\nattributes := {\"note\": input.value.note}"
    }
    """
    And the response status should be 200
    And I am authenticated as user "alice"
    When I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "authz-custom"
    key: "wrong-kind-key"
    value {
      fields {
        key: "note"
        value { string_value: "wrong kind" }
      }
    }
    kind: "default/v1"
    """
    Then the gRPC response should have status "PERMISSION_DENIED"

  Scenario: gRPC GetMemory carries exact row kind
    Given I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-read-kind"
    key: "read-mem"
    value {
      fields {
        key: "n"
        value { number_value: 5 }
      }
    }
    kind: "default/v1"
    """
    And the gRPC response should not have an error
    When I send gRPC request "MemoriesService/GetMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-read-kind"
    key: "read-mem"
    """
    Then the gRPC response should not have an error
    And the gRPC response field "kind" should be "default/v1"

  Scenario: gRPC GetMemory with wrong row kind in authz-custom is denied to user
    # Admin writes a default/v1 memory to authz-custom; user read is denied
    # because GetMemoryRowKind returns "default/v1" but authz-custom requires "authz/v1".
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds" with body:
    """
    {
      "name": "authz/v1",
      "attributes": {"note": "string"},
      "projectionRego": "package memories.attributes\nattributes := {\"note\": input.value.note}"
    }
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=grpc-row-kind-check" with body:
    """
    {
      "namespace": ["user", "alice", "authz-custom"],
      "key": "grpc-default-in-custom",
      "value": {"note": "default/v1 in authz-custom"},
      "kind": "default/v1"
    }
    """
    And the response status should be 200
    And I am authenticated as user "alice"
    When I send gRPC request "MemoriesService/GetMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "authz-custom"
    key: "grpc-default-in-custom"
    """
    Then the gRPC response should have status "PERMISSION_DENIED"

  Scenario: gRPC UpdateMemory (archive) with correct kind succeeds
    Given I send gRPC request "MemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-archive-kind"
    key: "to-archive"
    value {
      fields {
        key: "n"
        value { number_value: 6 }
      }
    }
    kind: "default/v1"
    """
    And the gRPC response should not have an error
    When I send gRPC request "MemoriesService/UpdateMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-archive-kind"
    key: "to-archive"
    archived: true
    """
    Then the gRPC response should not have an error

  # -----------------------------------------------------------------------
  # Filter: kind narrowing via gRPC SearchMemories
  # The custom filter.rego narrows "default" family to "default/v1" exact.
  # -----------------------------------------------------------------------

  Scenario: gRPC non-admin search policy excludes an otherwise matching other-kind row
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=grpc-policy-contrast" with body:
    """
    {"name":"other/v1","attributes":{},"projectionRego":"package memories.attributes\nattributes := {}"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=grpc-policy-contrast" with body:
    """
    {"namespace":["user","bob","grpc-filter-contrast"],"key":"grpc-allowed-default","value":{"x":1},"kind":"default/v1"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=grpc-policy-contrast" with body:
    """
    {"namespace":["user","bob","grpc-filter-contrast"],"key":"grpc-excluded-other","value":{"x":2},"kind":"other/v1"}
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "bob"
    namespace_prefix: "grpc-filter-contrast"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 1
    And the gRPC response field "items[0].key" should be "grpc-allowed-default"
    When I send gRPC request "MemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "bob"
    namespace_prefix: "grpc-filter-contrast"
    kind: "other/v1"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 0

  # -----------------------------------------------------------------------
  # Filter: malformed policy kind output → gRPC INTERNAL status
  # filter.rego outputs kind := 42 (integer, not string) for namespace
  # prefix ending in "filter-malformed-test". InjectFilterPartsWithKind
  # validates the type and returns a policy error → gRPC returns INTERNAL.
  # Note: uses "bob" (non-admin user) because filter.rego only produces the
  # malformed output for non-admin callers; alice has admin role in BDD.
  # -----------------------------------------------------------------------

  Scenario: gRPC SearchMemories filter policy malformed kind output returns INTERNAL
    Given I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "bob"
    namespace_prefix: "filter-malformed-test"
    limit: 10
    """
    Then the gRPC response should have status "INTERNAL"

  Scenario Outline: gRPC malformed non-kind filter policy output fails closed
    Given I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "bob"
    namespace_prefix: "<namespace>"
    limit: 10
    """
    Then the gRPC response should have status "INTERNAL"

    Examples:
      | namespace                  |
      | filter-bad-prefix-test     |
      | filter-bad-attributes-test |

  Scenario: gRPC filter kind narrowing applies to namespace and event lists
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=list-kind-filter" with body:
    """
    {"name":"other/v1","attributes":{},"projectionRego":"package memories.attributes\nattributes := {}"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=list-kind-filter" with body:
    """
    {"namespace":["user","bob","grpc-allowed-list"],"key":"grpc-allowed-event","value":{"x":1},"kind":"default/v1"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=list-kind-filter" with body:
    """
    {"namespace":["user","bob","grpc-denied-list"],"key":"grpc-denied-event","value":{"x":2},"kind":"other/v1"}
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/ListMemoryNamespaces" with body:
    """
    prefix: "user"
    prefix: "bob"
    """
    Then the gRPC response should not have an error
    And the gRPC response field "namespaces" should have size 1
    When I send gRPC request "MemoriesService/ListMemoryEvents" with body:
    """
    namespace: "user"
    namespace: "bob"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "events" should have size 1
    And the gRPC response field "events[0].key" should be "grpc-allowed-event"
    And the gRPC response field "events[0].memoryKind" should be "default/v1"

  Scenario: policy attribute filter protects gRPC namespace and event lists
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=tenant-filter" with body:
    """
    {"name":"tenant/v1","attributes":{"tenant":"string"},"projectionRego":"package memories.attributes\nattributes := {\"tenant\": input.value.tenant}"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=tenant-filter" with body:
    """
    {"namespace":["user","bob","filter-tenant-test","grpc-tenant-a"],"key":"grpc-tenant-a-event","value":{"tenant":"A"},"kind":"tenant/v1"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=tenant-filter" with body:
    """
    {"namespace":["user","bob","filter-tenant-test","grpc-tenant-b"],"key":"grpc-tenant-b-event","value":{"tenant":"B"},"kind":"tenant/v1"}
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/ListMemoryNamespaces" with body:
    """
    prefix: "user"
    prefix: "bob"
    prefix: "filter-tenant-test"
    """
    Then the gRPC response should not have an error
    And the gRPC response field "namespaces" should have size 1
    And the gRPC response field "namespaces[0].segments[3]" should be "grpc-tenant-a"
    When I send gRPC request "MemoriesService/ListMemoryEvents" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "filter-tenant-test"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "events" should have size 1
    And the gRPC response field "events[0].key" should be "grpc-tenant-a-event"

  # -----------------------------------------------------------------------
  # AdminSearch gRPC: exact/family/omitted; as_user_id applies policy
  # -----------------------------------------------------------------------

  Scenario: gRPC AdminSearch kind exact filter
    Given I am authenticated as admin user "alice"
    And I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-asearch-test"
    key: "srch-v1"
    value {
      fields {
        key: "label"
        value { string_value: "first" }
      }
    }
    kind: "default/v1"
    justification: "grpc-admin-search-kind"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "alice"
    namespace_prefix: "grpc-asearch-test"
    kind: "default/v1"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 1
    And the gRPC response field "items[0].key" should be "srch-v1"
    And the gRPC response field "items[0].kind" should be "default/v1"

  Scenario: gRPC AdminSearch kind family filter returns all versions
    Given I am authenticated as admin user "alice"
    And I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-famfilt-test"
    key: "fam-v1"
    value {
      fields {
        key: "n"
        value { number_value: 10 }
      }
    }
    kind: "default/v1"
    justification: "grpc-fam-filter"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "alice"
    namespace_prefix: "grpc-famfilt-test"
    kind: "default"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 1
    And the gRPC response field "items[0].key" should be "fam-v1"

  Scenario: gRPC AdminSearch omitted kind returns all memories
    Given I am authenticated as admin user "alice"
    And I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-omitted-test"
    key: "omit-1"
    value {
      fields {
        key: "n"
        value { number_value: 1 }
      }
    }
    kind: "default/v1"
    justification: "grpc-omitted"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "alice"
    namespace_prefix: "grpc-omitted-test"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 1

  Scenario: gRPC AdminSearch as_user_id narrows contrasting rows while normal admin search returns both
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=grpc-as-user" with body:
    """
    {"name":"other/v1","attributes":{},"projectionRego":"package memories.attributes\nattributes := {}"}
    """
    And the response status should be 200
    And I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "grpc-asuid-contrast"
    key: "grpc-asuid-default"
    value {
      fields {
        key: "data"
        value { string_value: "allowed" }
      }
    }
    kind: "default/v1"
    justification: "grpc-as-user"
    """
    And the gRPC response should not have an error
    And I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "grpc-asuid-contrast"
    key: "grpc-asuid-other"
    value {
      fields {
        key: "data"
        value { string_value: "excluded" }
      }
    }
    kind: "other/v1"
    justification: "grpc-as-user"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "bob"
    namespace_prefix: "grpc-asuid-contrast"
    as_user_id: "bob"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 1
    And the gRPC response field "items[0].key" should be "grpc-asuid-default"
    When I send gRPC request "AdminMemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "bob"
    namespace_prefix: "grpc-asuid-contrast"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 2

  Scenario: gRPC AdminSearch without as_user_id bypasses user filter policy
    Given I am authenticated as admin user "alice"
    And I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "grpc-bypass-test"
    key: "bob-mem"
    value {
      fields {
        key: "data"
        value { string_value: "cross-user" }
      }
    }
    kind: "default/v1"
    justification: "grpc-bypass"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "bob"
    namespace_prefix: "grpc-bypass-test"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 1
    And the gRPC response field "items[0].key" should be "bob-mem"

  # -----------------------------------------------------------------------
  # AdminListMemories gRPC: kind exact/family/omitted
  # -----------------------------------------------------------------------

  Scenario: gRPC AdminListMemories kind exact filter
    Given I am authenticated as admin user "alice"
    And I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-alist-test"
    key: "grpc-alist-1"
    value {
      fields {
        key: "n"
        value { number_value: 1 }
      }
    }
    kind: "default/v1"
    justification: "grpc-alist"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoriesService/ListMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "alice"
    namespace_prefix: "grpc-alist-test"
    kind: "default/v1"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 1
    And the gRPC response field "items[0].key" should be "grpc-alist-1"
    And the gRPC response field "items[0].kind" should be "default/v1"

  Scenario: gRPC AdminListMemories kind family filter returns all
    Given I am authenticated as admin user "alice"
    And I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-alist-fam-test"
    key: "grpc-alist-fam-1"
    value {
      fields {
        key: "n"
        value { number_value: 1 }
      }
    }
    kind: "default/v1"
    justification: "grpc-alist-fam"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoriesService/ListMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "alice"
    namespace_prefix: "grpc-alist-fam-test"
    kind: "default"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 1

  Scenario: gRPC AdminListMemories omitted kind returns all
    Given I am authenticated as admin user "alice"
    And I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-alist-omit-test"
    key: "grpc-alist-omit-1"
    value {
      fields {
        key: "n"
        value { number_value: 1 }
      }
    }
    kind: "default/v1"
    justification: "grpc-alist-omit"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoriesService/ListMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "alice"
    namespace_prefix: "grpc-alist-omit-test"
    limit: 10
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 1

  # -----------------------------------------------------------------------
  # Item 2: gRPC UpdateMemory nondisclosure — authz-before-404
  # -----------------------------------------------------------------------

  Scenario: gRPC UpdateMemory archive missing key under authz-custom returns PERMISSION_DENIED
    # bob's authz-custom rule requires kind == "authz/v1"; missing row has kind=""
    # which does not satisfy that → PERMISSION_DENIED, not NOT_FOUND.
    Given I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/UpdateMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "authz-custom"
    key: "grpc-nonexistent-bob-key"
    archived: true
    """
    Then the gRPC response should have status "PERMISSION_DENIED"

  Scenario: gRPC UpdateMemory archive wrong-kind row in authz-custom returns PERMISSION_DENIED
    # Admin writes default/v1 to authz-custom; bob's authz requires authz/v1 → denied.
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds" with body:
    """
    {
      "name": "authz/v1",
      "attributes": {"note": "string"},
      "projectionRego": "package memories.attributes\nattributes := {\"note\": input.value.note}"
    }
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=grpc-update-kind-test" with body:
    """
    {
      "namespace": ["user", "bob", "authz-custom"],
      "key": "grpc-bob-wrong-kind",
      "value": {"note": "default/v1 in authz-custom"},
      "kind": "default/v1"
    }
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/UpdateMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "authz-custom"
    key: "grpc-bob-wrong-kind"
    archived: true
    """
    Then the gRPC response should have status "PERMISSION_DENIED"

  # -----------------------------------------------------------------------
  # Item 3: gRPC AdminListMemories malformed kind selector returns INVALID_ARGUMENT
  # -----------------------------------------------------------------------

  Scenario: gRPC AdminListMemories malformed kind selector returns INVALID_ARGUMENT
    Given I am authenticated as admin user "alice"
    When I send gRPC request "AdminMemoriesService/ListMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "alice"
    kind: "INVALID-Kind"
    limit: 10
    """
    Then the gRPC response should have status "INVALID_ARGUMENT"

  Scenario: gRPC archived wrong-kind rows remain denied on direct GetMemory
    Given I am authenticated as admin user "alice"
    And I call PUT "/admin/v1/memories?justification=grpc-archived-wrong-kind" with body:
    """
    {"namespace":["user","bob","authz-custom"],"key":"grpc-archived-wrong-kind","value":{"note":"denied"},"kind":"default/v1"}
    """
    And the response status should be 200
    And I call PATCH "/admin/v1/memories?ns=user&ns=bob&ns=authz-custom&key=grpc-archived-wrong-kind&justification=grpc-archived-wrong-kind" with body:
    """
    {"archived":true}
    """
    And the response status should be 204
    And I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/GetMemory" with body:
    """
    namespace: "user"
    namespace: "bob"
    namespace: "authz-custom"
    key: "grpc-archived-wrong-kind"
    archived: ARCHIVE_FILTER_ONLY
    """
    Then the gRPC response should have status "PERMISSION_DENIED"

  Scenario: gRPC AdminListMemories rejects undeclared attribute filter
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=admin-list-validation" with body:
    """
    {
      "name": "adminlisttyped/v1",
      "attributes": {"score": "number"},
      "projectionRego": "package memories.attributes\nattributes := {\"score\": input.value.score}"
    }
    """
    And the response status should be 200
    When I send gRPC request "AdminMemoriesService/ListMemories" with body:
    """
    kind: "adminlisttyped/v1"
    justification: "admin-list-validation"
    filter { fields { key: "unknown" value { number_value: 1 } } }
    limit: 10
    """
    Then the gRPC response should have status "INVALID_ARGUMENT"

  Scenario: gRPC AdminListMemories rejects wrong-typed attribute filter
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=admin-list-validation" with body:
    """
    {
      "name": "adminlisttyped/v1",
      "attributes": {"score": "number"},
      "projectionRego": "package memories.attributes\nattributes := {\"score\": input.value.score}"
    }
    """
    And the response status should be 200
    When I send gRPC request "AdminMemoriesService/ListMemories" with body:
    """
    kind: "adminlisttyped/v1"
    justification: "admin-list-validation"
    filter { fields { key: "score" value { string_value: "not-a-number" } } }
    limit: 10
    """
    Then the gRPC response should have status "INVALID_ARGUMENT"

  Scenario: gRPC user search rejects an invalid sort direction
    Given I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "bob"
    kind: "default/v1"
    sort { field: "namespace" direction: "sideways" }
    limit: 10
    """
    Then the gRPC response should have status "INVALID_ARGUMENT"

  Scenario: gRPC admin search rejects an invalid sort direction
    Given I am authenticated as admin user "alice"
    When I send gRPC request "AdminMemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    kind: "default/v1"
    sort { field: "namespace" direction: "sideways" }
    justification: "sort validation"
    limit: 10
    """
    Then the gRPC response should have status "INVALID_ARGUMENT"

  # -----------------------------------------------------------------------
  # Item 6: gRPC projection validation returns INVALID_ARGUMENT
  # -----------------------------------------------------------------------

  Scenario: gRPC AdminPut rejects a value whose projection has the wrong declared type
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=projection-validation" with body:
    """
    {
      "name": "typedscore/v1",
      "attributes": {"score": "number"},
      "projectionRego": "package memories.attributes\nattributes := {\"score\": input.value.score}"
    }
    """
    And the response status should be 200
    When I send gRPC request "AdminMemoriesService/PutMemory" with body:
    """
    namespace: "user"
    namespace: "alice"
    namespace: "grpc-proj-valid-test"
    key: "wrong-score"
    kind: "typedscore/v1"
    justification: "projection-validation"
    value {
      fields {
        key: "score"
        value { string_value: "not-a-number" }
      }
    }
    """
    Then the gRPC response should have status "INVALID_ARGUMENT"

  # -----------------------------------------------------------------------
  # Item 7: gRPC SearchMemories filter empty-kind output returns INTERNAL
  # -----------------------------------------------------------------------

  Scenario: gRPC SearchMemories filter policy empty-kind output returns INTERNAL
    Given I am authenticated as user "bob"
    When I send gRPC request "MemoriesService/SearchMemories" with body:
    """
    namespace_prefix: "user"
    namespace_prefix: "bob"
    namespace_prefix: "filter-empty-kind-test"
    limit: 10
    """
    Then the gRPC response should have status "INTERNAL"

  Scenario: gRPC admin memory-kind lifecycle covers all operations
    Given I am authenticated as admin user "alice"
    When I send gRPC request "AdminMemoryKindService/CreateMemoryKindVersion" with body:
    """
    name: "grpclifecycle/v1"
    attributes { key: "tag" value: "string" }
    projection_rego: "package memories.attributes\nattributes := {\"tag\": input.value.tag}"
    justification: "kind lifecycle"
    """
    Then the gRPC response should not have an error
    And the gRPC response field "name" should be "grpclifecycle/v1"

    When I send gRPC request "AdminMemoryKindService/CreateMemoryKindVersion" with body:
    """
    name: "grpclifecycle/v1"
    attributes { key: "tag" value: "string" }
    projection_rego: "package memories.attributes\nattributes := {\"tag\": input.value.tag}"
    justification: "idempotent create"
    """
    Then the gRPC response should not have an error
    When I send gRPC request "AdminMemoryKindService/CreateMemoryKindVersion" with body:
    """
    name: "grpclifecycle/v2"
    attributes { key: "tag" value: "string" }
    projection_rego: "package memories.attributes\nattributes := {\"tag\": input.value.tag}"
    justification: "kind lifecycle"
    """
    Then the gRPC response should not have an error
    When I send gRPC request "AdminMemoryKindService/ListMemoryKindVersions" with body:
    """
    family: "grpclifecycle"
    justification: "kind lifecycle"
    """
    Then the gRPC response should not have an error
    And the gRPC response field "items" should have size 2
    When I send gRPC request "AdminMemoryKindService/GetMemoryKindVersion" with body:
    """
    family: "grpclifecycle"
    version: "v1"
    justification: "kind lifecycle"
    """
    Then the gRPC response should not have an error
    And the gRPC response field "name" should be "grpclifecycle/v1"
    When I send gRPC request "AdminMemoryKindService/CreateMemoryKindMigration" with body:
    """
    source: "grpclifecycle/v1"
    target: "grpclifecycle/v2"
    namespace_prefix: "user"
    namespace_prefix: "alice"
    justification: "kind lifecycle"
    """
    Then the gRPC response should not have an error
    And set "migrationId" to the gRPC response field "id"
    When I send gRPC request "AdminMemoryKindService/ListMemoryKindMigrations" with body:
    """
    justification: "kind lifecycle"
    """
    Then the gRPC response should not have an error
    When I send gRPC request "AdminMemoryKindService/GetMemoryKindMigration" with body:
    """
    id: "${migrationId}"
    justification: "kind lifecycle"
    """
    Then the gRPC response should not have an error
    When I send gRPC request "AdminMemoryKindService/CancelMemoryKindMigration" with body:
    """
    id: "${migrationId}"
    justification: "kind lifecycle"
    """
    Then the gRPC response should not have an error

  Scenario: gRPC immutable kind creation detects a writable-only conflict
    Given I am authenticated as admin user "alice"
    When I send gRPC request "AdminMemoryKindService/CreateMemoryKindVersion" with body:
    """
    name: "grpcwritableconflict/v1"
    projection_rego: "package memories.attributes\nattributes := {}"
    writable: false
    justification: "writable conflict"
    """
    Then the gRPC response should not have an error
    When I send gRPC request "AdminMemoryKindService/CreateMemoryKindVersion" with body:
    """
    name: "grpcwritableconflict/v1"
    projection_rego: "package memories.attributes\nattributes := {}"
    writable: true
    justification: "writable conflict"
    """
    Then the gRPC response should have status "ALREADY_EXISTS"

  Scenario: gRPC immutable kind conflict preserves INVALID_ARGUMENT and ALREADY_EXISTS statuses
    Given I am authenticated as admin user "alice"
    And I send gRPC request "AdminMemoryKindService/CreateMemoryKindVersion" with body:
    """
    name: "grpcconflict/v1"
    attributes { key: "tag" value: "string" }
    projection_rego: "package memories.attributes\nattributes := {\"tag\": input.value.tag}"
    justification: "conflict test"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoryKindService/CreateMemoryKindVersion" with body:
    """
    name: "grpcconflict/v1"
    attributes { key: "score" value: "number" }
    projection_rego: "package memories.attributes\nattributes := {\"score\": input.value.score}"
    justification: "conflict test"
    """
    Then the gRPC response should have status "ALREADY_EXISTS"
    When I send gRPC request "AdminMemoryKindService/GetMemoryKindVersion" with body:
    """
    family: "missing"
    version: "v1"
    justification: "missing test"
    """
    Then the gRPC response should have status "NOT_FOUND"

  Scenario: gRPC migration creation maps missing source and target kinds to NOT_FOUND
    Given I am authenticated as admin user "alice"
    And I send gRPC request "AdminMemoryKindService/CreateMemoryKindVersion" with body:
    """
    name: "missingfamily/v2"
    projection_rego: "package memories.attributes\nattributes := {}"
    justification: "missing source setup"
    """
    And the gRPC response should not have an error
    When I send gRPC request "AdminMemoryKindService/CreateMemoryKindMigration" with body:
    """
    source: "missingfamily/v1"
    target: "missingfamily/v2"
    justification: "missing source"
    """
    Then the gRPC response should have status "NOT_FOUND"
    When I send gRPC request "AdminMemoryKindService/CreateMemoryKindMigration" with body:
    """
    source: "default/v1"
    target: "default/v2"
    justification: "missing target"
    """
    Then the gRPC response should have status "NOT_FOUND"
