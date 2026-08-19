Feature: Episodic Memory Kind Policy REST
  As an operator with a custom OPA policy
  I want the authz policy to see the resolved exact kind in input.kind
  And the filter policy to be able to narrow or error on the kind selector
  So that access control and search scoping honour the fresh-only kind invariant

  Background:
    Given I am authenticated as user "alice"

  # -----------------------------------------------------------------------
  # Authz: input.kind carries the resolved exact kind on every operation.
  #
  # Custom authz.rego rules:
  #   user/<uid>/authz-custom/... : requires input.kind == "authz/v1"
  #   user/<uid>/... (other)      : requires input.kind == "default/v1"
  #   user/<uid>/denied-ns/...    : always denied
  #
  # This proves input.kind is actually evaluated — a wrong/empty kind fails.
  # -----------------------------------------------------------------------

  Scenario: omitted-kind write resolves to default/v1 and is allowed by authz
    # Omitting kind always resolves to the fixed built-in "default/v1".
    # The authz.rego rule for ordinary namespaces requires input.kind == "default/v1".
    # If input.kind were empty or wrong, the rule would not fire and authz would deny.
    When I call PUT "/v1/memories" with body:
    """
    {
      "namespace": ["user", "alice", "authz-omit-test"],
      "key": "default-write",
      "value": {"note": "omitted kind resolves to default/v1"}
    }
    """
    Then the response status should be 200
    And the response body field "kind" should be "default/v1"

  Scenario: explicit default/v1 write is allowed by authz for ordinary namespace
    When I call PUT "/v1/memories" with body:
    """
    {
      "namespace": ["user", "alice", "authz-explicit-test"],
      "key": "explicit-write",
      "value": {"note": "explicit default/v1"}
      ,"kind": "default/v1"
    }
    """
    Then the response status should be 200
    And the response body field "kind" should be "default/v1"

  Scenario: authz-custom namespace requires authz/v1: write with authz/v1 is allowed
    # First create the "authz/v1" schema version via admin.
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
    # Switch back to regular user and write with authz/v1 to the authz-custom namespace.
    And I am authenticated as user "alice"
    When I call PUT "/v1/memories" with body:
    """
    {
      "namespace": ["user", "alice", "authz-custom"],
      "key": "custom-write",
      "value": {"note": "authz/v1 write"},
      "kind": "authz/v1"
    }
    """
    Then the response status should be 200
    And the response body field "kind" should be "authz/v1"

  Scenario: authz-custom namespace requires authz/v1: write with default/v1 is denied
    # Writing into authz-custom with default/v1 must be denied — proves the policy
    # check on input.kind is actually evaluated (not just kind format validation).
    # We need authz/v1 to exist for the write-resolution to succeed before authz.
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
    When I call PUT "/v1/memories" with body:
    """
    {
      "namespace": ["user", "alice", "authz-custom"],
      "key": "wrong-kind-write",
      "value": {"note": "wrong kind"},
      "kind": "default/v1"
    }
    """
    Then the response status should be 403

  Scenario: authz-custom row written with authz/v1 is readable with authz/v1
    # Admin writes authz/v1 memory to authz-custom; user reads it successfully.
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
    And I call PUT "/admin/v1/memories?justification=authz-custom-write" with body:
    """
    {
      "namespace": ["user", "alice", "authz-custom"],
      "key": "read-row",
      "value": {"note": "written with authz/v1"},
      "kind": "authz/v1"
    }
    """
    And the response status should be 200
    And I am authenticated as user "alice"
    When I call GET "/v1/memories?ns=user&ns=alice&ns=authz-custom&key=read-row"
    Then the response status should be 200
    And the response body field "kind" should be "authz/v1"

  Scenario: authz-custom row with default/v1 kind is denied to user (authz checks row kind)
    # Admin writes a memory with default/v1 into the authz-custom namespace.
    # User reads it and should be denied because the stored kind is default/v1,
    # but the authz-custom rule requires input.kind == "authz/v1".
    # This proves authz uses the actual stored row kind, not a default.
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
    And I call PUT "/admin/v1/memories?justification=authz-row-kind-check" with body:
    """
    {
      "namespace": ["user", "alice", "authz-custom"],
      "key": "default-kind-in-custom",
      "value": {"note": "default/v1 in authz-custom"},
      "kind": "default/v1"
    }
    """
    And the response status should be 200
    And I am authenticated as user "alice"
    When I call GET "/v1/memories?ns=user&ns=alice&ns=authz-custom&key=default-kind-in-custom"
    Then the response status should be 403

  Scenario: archive with correct kind is allowed by authz
    Given I call PUT "/v1/memories" with body:
    """
    {
      "namespace": ["user", "alice", "authz-archive-test"],
      "key": "archive-mem",
      "value": {"note": "will be archived"},
      "kind": "default/v1"
    }
    """
    And the response status should be 200
    When I call PATCH "/v1/memories?ns=user&ns=alice&ns=authz-archive-test&key=archive-mem" with body:
    """
    {"archived": true}
    """
    Then the response status should be 204

  Scenario: authz kind-deny policy blocks write for denied namespace
    When I call PUT "/v1/memories" with body:
    """
    {
      "namespace": ["user", "alice", "denied-ns"],
      "key": "denied-write",
      "value": {"note": "should be denied"}
    }
    """
    Then the response status should be 403

  Scenario: Put cannot replace an existing kind the caller may not modify
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=replace-authz" with body:
    """
    {"name":"authz/v1","attributes":{"note":"string"},"projectionRego":"package memories.attributes\nattributes := {\"note\": input.value.note}"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=replace-authz" with body:
    """
    {"namespace":["user","bob","replace-protected"],"key":"same-key","value":{"note":"protected"},"kind":"authz/v1"}
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I call PUT "/v1/memories" with body:
    """
    {"namespace":["user","bob","replace-protected"],"key":"same-key","value":{"note":"replacement"},"kind":"default/v1"}
    """
    Then the response status should be 403
    When I call GET "/v1/memories?ns=user&ns=bob&ns=replace-protected&key=same-key"
    Then the response status should be 403
    Given I am authenticated as admin user "alice"
    When I call GET "/admin/v1/memories?namespacePrefix=user&namespacePrefix=bob&namespacePrefix=replace-protected&justification=replace-authz"
    Then the response status should be 200
    And the response body should contain "protected"
    And the response body should contain "authz/v1"
    And the response body should not contain "replacement"

  Scenario: archived reads authorize the exact selected historical row kind
    Given I am authenticated as user "bob"
    And I call PUT "/v1/memories" with body:
    """
    {"namespace":["user","bob","archived-kind-read"],"key":"allowed","value":{"note":"allowed"},"kind":"default/v1"}
    """
    And the response status should be 200
    And I call PATCH "/v1/memories?ns=user&ns=bob&ns=archived-kind-read&key=allowed" with body:
    """
    {"archived":true}
    """
    And the response status should be 204
    When I call GET "/v1/memories?ns=user&ns=bob&ns=archived-kind-read&key=allowed&archived=only"
    Then the response status should be 200
    When I call GET "/v1/memories?ns=user&ns=bob&ns=archived-kind-read&key=allowed&archived=include"
    Then the response status should be 200

  # -----------------------------------------------------------------------
  # Filter: policy outputs kind "default/v1"; interaction with caller kind
  # The custom filter.rego outputs kind: "default/v1" for ordinary callers.
  # - caller "default" (family) ∩ policy "default/v1" (exact in family) → "default/v1"
  # - caller "other/v1" (exact, different family) ∩ policy "default/v1" → disjoint → 200 empty
  # - caller "" (all) ∩ policy "default/v1" → "default/v1"
  # -----------------------------------------------------------------------

  Scenario: non-admin search policy excludes an otherwise matching other-kind row
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=policy-contrast" with body:
    """
    {"name":"other/v1","attributes":{},"projectionRego":"package memories.attributes\nattributes := {}"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=policy-contrast" with body:
    """
    {"namespace":["user","bob","filter-contrast"],"key":"allowed-default","value":{"x":1},"kind":"default/v1"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=policy-contrast" with body:
    """
    {"namespace":["user","bob","filter-contrast"],"key":"excluded-other","value":{"x":2},"kind":"other/v1"}
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I call POST "/v1/memories/search" with body:
    """
    {"namespace_prefix":["user","bob","filter-contrast"],"limit":10}
    """
    Then the response status should be 200
    And the response body should contain "allowed-default"
    And the response body should not contain "excluded-other"
    When I call POST "/v1/memories/search" with body:
    """
    {"namespace_prefix":["user","bob","filter-contrast"],"kind":"other/v1","limit":10}
    """
    Then the response status should be 200
    And the response body "items" should have at most 0 items

  # -----------------------------------------------------------------------
  # Filter: malformed policy kind output → 500 Internal Server Error
  # filter.rego outputs kind := 42 (integer, not string) for namespace
  # prefix ending in "filter-malformed-test". InjectFilterPartsWithKind
  # validates the type and returns a policy error → REST returns 500.
  # Note: uses "bob" (non-admin user) because filter.rego only produces the
  # malformed output for non-admin callers; alice has admin role in BDD.
  # -----------------------------------------------------------------------

  Scenario: filter policy malformed kind output returns 500
    Given I am authenticated as user "bob"
    When I call POST "/v1/memories/search" with body:
    """
    {
      "namespace_prefix": ["user", "bob", "filter-malformed-test"],
      "limit": 10
    }
    """
    Then the response status should be 500

  Scenario Outline: malformed non-kind filter policy output fails closed
    Given I am authenticated as user "bob"
    When I call POST "/v1/memories/search" with body:
    """
    {
      "namespace_prefix": ["user", "bob", "<namespace>"],
      "limit": 10
    }
    """
    Then the response status should be 500

    Examples:
      | namespace                  |
      | filter-bad-prefix-test     |
      | filter-bad-attributes-test |

  Scenario: filter kind narrowing applies to namespace and event lists
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=list-kind-filter" with body:
    """
    {
      "name": "other/v1",
      "attributes": {},
      "projectionRego": "package memories.attributes\nattributes := {}"
    }
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=list-kind-filter" with body:
    """
    {"namespace":["user","bob","allowed-list-ns"],"key":"allowed-event","value":{"x":1},"kind":"default/v1"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=list-kind-filter" with body:
    """
    {"namespace":["user","bob","denied-list-ns"],"key":"denied-event","value":{"x":2},"kind":"other/v1"}
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I call GET "/v1/memories/namespaces?prefix=user&prefix=bob"
    Then the response status should be 200
    And the response body should contain "allowed-list-ns"
    And the response body should not contain "denied-list-ns"
    When I call GET "/v1/memories/events?ns=user&ns=bob&limit=10"
    Then the response status should be 200
    And the response body field "events[0].memoryKind" should be "default/v1"
    And the response body should contain "allowed-event"
    And the response body should contain "default/v1"
    And the response body should not contain "denied-event"

  Scenario: policy attribute filter protects REST search namespace and event lists
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=tenant-filter" with body:
    """
    {"name":"tenant/v1","attributes":{"tenant":"string"},"projectionRego":"package memories.attributes\nattributes := {\"tenant\": input.value.tenant}"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=tenant-filter" with body:
    """
    {"namespace":["user","bob","filter-tenant-test","tenant-a"],"key":"tenant-a-event","value":{"tenant":"A"},"kind":"tenant/v1"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=tenant-filter" with body:
    """
    {"namespace":["user","bob","filter-tenant-test","tenant-b"],"key":"tenant-b-event","value":{"tenant":"B"},"kind":"tenant/v1"}
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I call POST "/v1/memories/search" with body:
    """
    {"namespace_prefix":["user","bob","filter-tenant-test"],"limit":10}
    """
    Then the response status should be 200
    And the response body should contain "tenant-a-event"
    And the response body should not contain "tenant-b-event"
    When I call GET "/v1/memories/namespaces?prefix=user&prefix=bob&prefix=filter-tenant-test"
    Then the response status should be 200
    And the response body should contain "tenant-a"
    And the response body should not contain "tenant-b"
    When I call GET "/v1/memories/events?ns=user&ns=bob&ns=filter-tenant-test&limit=10"
    Then the response status should be 200
    And the response body should contain "tenant-a-event"
    And the response body should not contain "tenant-b-event"

  # -----------------------------------------------------------------------
  # AdminSearch: as_user_id applies policy; normal admin bypasses policy
  # -----------------------------------------------------------------------

  Scenario: AdminSearch as_user_id narrows contrasting rows while normal admin search returns both
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=as-user-test" with body:
    """
    {"name":"other/v1","attributes":{},"projectionRego":"package memories.attributes\nattributes := {}"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=as-user-test" with body:
    """
    {"namespace":["user","bob","asuid-contrast"],"key":"asuid-default","value":{"data":"allowed"},"kind":"default/v1"}
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=as-user-test" with body:
    """
    {"namespace":["user","bob","asuid-contrast"],"key":"asuid-other","value":{"data":"excluded"},"kind":"other/v1"}
    """
    And the response status should be 200
    When I call POST "/admin/v1/memories/search" with body:
    """
    {"namespace_prefix":["user","bob","asuid-contrast"],"as_user_id":"bob","limit":10}
    """
    Then the response status should be 200
    And the response body should contain "asuid-default"
    And the response body should not contain "asuid-other"
    When I call POST "/admin/v1/memories/search" with body:
    """
    {"namespace_prefix":["user","bob","asuid-contrast"],"limit":10}
    """
    Then the response status should be 200
    And the response body should contain "asuid-default"
    And the response body should contain "asuid-other"

  # -----------------------------------------------------------------------
  # AdminList combined kind + attribute filter
  # -----------------------------------------------------------------------

  Scenario: AdminList combined kind exact and attribute filter
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds" with body:
    """
    {
      "name": "combo/v1",
      "attributes": {"tag": "string"},
      "projectionRego": "package memories.attributes\nattributes := {\"tag\": input.value.tag}"
    }
    """
    And the response status should be 200
    And I call POST "/admin/v1/memory-kinds" with body:
    """
    {
      "name": "combo/v2",
      "attributes": {"tag": "string"},
      "projectionRego": "package memories.attributes\nattributes := {\"tag\": input.value.tag}"
    }
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=combo-test" with body:
    """
    {
      "namespace": ["user", "alice", "combo-test"],
      "key": "v1-tag-alpha",
      "value": {"tag": "alpha"},
      "kind": "combo/v1"
    }
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=combo-test" with body:
    """
    {
      "namespace": ["user", "alice", "combo-test"],
      "key": "v1-tag-beta",
      "value": {"tag": "beta"},
      "kind": "combo/v1"
    }
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=combo-test" with body:
    """
    {
      "namespace": ["user", "alice", "combo-test"],
      "key": "v2-tag-alpha",
      "value": {"tag": "alpha"},
      "kind": "combo/v2"
    }
    """
    And the response status should be 200
    When I call GET "/admin/v1/memories?namespacePrefix=user&namespacePrefix=alice&namespacePrefix=combo-test&kind=combo%2Fv1&filter=%7B%22tag%22%3A%22alpha%22%7D&limit=10"
    Then the response status should be 200
    And the response body should contain "v1-tag-alpha"
    And the response body should not contain "v1-tag-beta"
    And the response body should not contain "v2-tag-alpha"

  Scenario: AdminList omitted kind returns all memories
    Given I am authenticated as admin user "alice"
    And I call PUT "/admin/v1/memories?justification=omitted-test" with body:
    """
    {
      "namespace": ["user", "alice", "omitted-test"],
      "key": "omit-1",
      "value": {"x": 1}
    }
    """
    And the response status should be 200
    And I call PUT "/admin/v1/memories?justification=omitted-test" with body:
    """
    {
      "namespace": ["user", "alice", "omitted-test"],
      "key": "omit-2",
      "value": {"x": 2}
    }
    """
    And the response status should be 200
    When I call GET "/admin/v1/memories?namespacePrefix=user&namespacePrefix=alice&namespacePrefix=omitted-test&limit=10"
    Then the response status should be 200
    And the response body should contain "omit-1"
    And the response body should contain "omit-2"

  # -----------------------------------------------------------------------
  # Item 2: UpdateMemory nondisclosure — authz-before-404
  # Missing key with authz-custom policy must return 403, not 404,
  # to prevent existence leakage. A present key with wrong kind also returns 403.
  # -----------------------------------------------------------------------

  Scenario: archive missing key under authz-custom returns 403 not 404
    # bob uses the authz-custom policy which requires input.kind == "authz/v1".
    # The key does not exist but the policy denies (rowKind="" for missing row
    # does not satisfy the authz-custom rule).
    # Uses "bob" to avoid alice's admin bypass.
    Given I am authenticated as user "bob"
    When I call PATCH "/v1/memories?ns=user&ns=bob&ns=authz-custom&key=nonexistent-bob-key" with body:
    """
    {"archived": true}
    """
    Then the response status should be 403

  Scenario: archive existing key with wrong kind in authz-custom returns 403
    # Admin writes a default/v1 row to authz-custom for bob.
    # bob tries to archive it — authz sees stored kind "default/v1" but
    # authz-custom requires "authz/v1" → 403 without revealing the value.
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
    And I call PUT "/admin/v1/memories?justification=update-kind-test" with body:
    """
    {
      "namespace": ["user", "bob", "authz-custom"],
      "key": "bob-wrong-kind",
      "value": {"note": "default kind in authz-custom"},
      "kind": "default/v1"
    }
    """
    And the response status should be 200
    And I am authenticated as user "bob"
    When I call PATCH "/v1/memories?ns=user&ns=bob&ns=authz-custom&key=bob-wrong-kind" with body:
    """
    {"archived": true}
    """
    Then the response status should be 403

  # -----------------------------------------------------------------------
  # Item 3: AdminList malformed kind selector returns 400
  # -----------------------------------------------------------------------

  Scenario: AdminList malformed kind selector returns 400
    Given I am authenticated as admin user "alice"
    When I call GET "/admin/v1/memories?namespacePrefix=user&namespacePrefix=alice&kind=INVALID-Kind&limit=10"
    Then the response status should be 400

  Scenario: REST migration creation maps missing source and target kinds to 404
    Given I am authenticated as admin user "alice"
    And I call POST "/admin/v1/memory-kinds?justification=missing-source-setup" with body:
    """
    {"name":"missingfamilyrest/v2","attributes":{},"projectionRego":"package memories.attributes\nattributes := {}"}
    """
    And the response status should be 200
    When I call POST "/admin/v1/memory-kind-migrations?justification=missing-source" with body:
    """
    {"source":"missingfamilyrest/v1","target":"missingfamilyrest/v2"}
    """
    Then the response status should be 404
    When I call POST "/admin/v1/memory-kind-migrations?justification=missing-target" with body:
    """
    {"source":"default/v1","target":"default/v2"}
    """
    Then the response status should be 404

  Scenario: archived wrong-kind rows remain denied on direct GET
    Given I am authenticated as admin user "alice"
    And I call PUT "/admin/v1/memories?justification=archived-wrong-kind" with body:
    """
    {"namespace":["user","bob","authz-custom"],"key":"archived-wrong-kind","value":{"note":"denied"},"kind":"default/v1"}
    """
    And the response status should be 200
    And I call PATCH "/admin/v1/memories?ns=user&ns=bob&ns=authz-custom&key=archived-wrong-kind&justification=archived-wrong-kind" with body:
    """
    {"archived":true}
    """
    And the response status should be 204
    And I am authenticated as user "bob"
    When I call GET "/v1/memories?ns=user&ns=bob&ns=authz-custom&key=archived-wrong-kind&archived=only"
    Then the response status should be 403

  Scenario Outline: AdminList rejects invalid typed attribute filters
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
    When I call GET "/admin/v1/memories?kind=adminlisttyped%2Fv1&filter=<filter>&limit=10&justification=admin-list-validation"
    Then the response status should be 400

    Examples:
      | filter                                  |
      | %7B%22unknown%22%3A1%7D                 |
      | %7B%22score%22%3A%22not-a-number%22%7D |

  # -----------------------------------------------------------------------
  # Item 6: Projection validation — typed kind with invalid projected type
  # The projection returns a string for a declared number and must fail at the API boundary.
  # -----------------------------------------------------------------------

  Scenario: AdminPut rejects a value whose projection has the wrong declared type
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
    When I call PUT "/admin/v1/memories?justification=projection-validation" with body:
    """
    {
      "namespace": ["user", "alice", "proj-valid-test"],
      "key": "wrong-score",
      "kind": "typedscore/v1",
      "value": {"score": "not-a-number"}
    }
    """
    Then the response status should be 400

  # -----------------------------------------------------------------------
  # Item 7: Filter empty-kind policy output returns 500
  # filter.rego for "filter-empty-kind-test" prefix outputs kind := ""
  # (present but empty). InjectFilterPartsWithKind must reject this as a
  # policy error → REST returns 500.
  # Note: uses "bob" because filter.rego only emits the malformed output
  # for non-admin callers; alice has admin role in BDD.
  # -----------------------------------------------------------------------

  Scenario: filter policy empty-kind output returns 500
    Given I am authenticated as user "bob"
    When I call POST "/v1/memories/search" with body:
    """
    {
      "namespace_prefix": ["user", "bob", "filter-empty-kind-test"],
      "limit": 10
    }
    """
    Then the response status should be 500
