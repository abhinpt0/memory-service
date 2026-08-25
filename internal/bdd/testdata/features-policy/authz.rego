package memories.authz

default decision = {"allow": false, "reason": "access denied"}

# ---------------------------------------------------------------------------
# Allowed access patterns
#
# "authz-custom" sub-namespace: requires input.kind == "authz/v1"
# All other user sub-namespaces: requires input.kind == "default/v1"
# "denied-ns" is always blocked (see below).
#
# Both read and write rules check the kind so that an empty or wrong kind
# causes the default deny to fire.
# ---------------------------------------------------------------------------

# Read access for authz-custom sub-namespace: exact kind "authz/v1" required.
decision = {"allow": true} if {
    input.operation != "write"
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    input.namespace[2] == "authz-custom"
    input.kind == "authz/v1"
}

# Write access for authz-custom sub-namespace: exact kind "authz/v1" required.
decision = {"allow": true} if {
    input.operation == "write"
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    input.namespace[2] == "authz-custom"
    input.kind == "authz/v1"
    count(object.keys(input.index)) <= 8
}

# Read access for ordinary user sub-namespaces: requires input.kind == "default/v1".
decision = {"allow": true} if {
    input.operation != "write"
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    input.namespace[2] == "filter-tenant-test"
    input.kind == "tenant/v1"
}

decision = {"allow": true} if {
    input.operation == "write"
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    input.namespace[2] == "filter-tenant-test"
    input.kind == "tenant/v1"
    count(object.keys(input.index)) <= 8
}

decision = {"allow": true} if {
    input.operation != "write"
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    input.namespace[2] != "denied-ns"
    input.namespace[2] != "authz-custom"
    input.namespace[2] != "filter-tenant-test"
    input.kind == "default/v1"
}

# Write access for ordinary user sub-namespaces: requires input.kind == "default/v1".
decision = {"allow": true} if {
    input.operation == "write"
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    input.namespace[2] != "denied-ns"
    input.namespace[2] != "authz-custom"
    input.namespace[2] != "filter-tenant-test"
    input.kind == "default/v1"
    count(object.keys(input.index)) <= 8
}

# "denied-ns" is explicitly blocked regardless of kind.
decision = {"allow": false, "reason": "namespace 'denied-ns' is blocked by policy"} if {
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    input.namespace[2] == "denied-ns"
}
