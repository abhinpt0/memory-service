package memories.filter

# Non-admin callers are constrained to their own user subtree.
namespace_prefix := input.namespace_prefix if {
    is_admin
}
namespace_prefix := input.namespace_prefix if {
    not is_admin
	 not is_bad_prefix_ns
    starts_with(input.namespace_prefix, user_prefix)
}
namespace_prefix := user_prefix if {
    not is_admin
	 not is_bad_prefix_ns
    not starts_with(input.namespace_prefix, user_prefix)
}

namespace_prefix := 42 if {
	 is_bad_prefix_ns
}

user_prefix := ["user", input.context.user_id]

starts_with(ns, prefix) if {
    count(prefix) == 0
}
starts_with(ns, prefix) if {
    count(ns) >= count(prefix)
    not mismatch(ns, prefix)
}
mismatch(ns, prefix) if {
    some i
    i < count(prefix)
    ns[i] != prefix[i]
}

is_admin if {
    "admin" in input.context.jwt_claims.roles
}

attribute_filter := {} if {
	 not is_bad_attributes_ns
	 not is_tenant_filter_ns
}

attribute_filter := {"tenant": "A"} if {
	 is_tenant_filter_ns
}

attribute_filter := "bad" if {
	 is_bad_attributes_ns
}

is_bad_prefix_ns if {
	 not is_admin
	 count(input.namespace_prefix) >= 3
	 input.namespace_prefix[2] == "filter-bad-prefix-test"
}

is_bad_attributes_ns if {
	 not is_admin
	 count(input.namespace_prefix) >= 3
	 input.namespace_prefix[2] == "filter-bad-attributes-test"
}

is_tenant_filter_ns if {
	 not is_admin
	 count(input.namespace_prefix) >= 3
	 input.namespace_prefix[2] == "filter-tenant-test"
}

# is_malformed_ns is true when the caller's namespace prefix is the dedicated
# malformed-test sub-namespace (3rd element == "filter-malformed-test").
is_malformed_ns if {
    not is_admin
    count(input.namespace_prefix) >= 3
    input.namespace_prefix[2] == "filter-malformed-test"
}

# is_empty_kind_ns is true when the caller's namespace prefix is the dedicated
# empty-kind-test sub-namespace (3rd element == "filter-empty-kind-test").
# This exercises the present-but-empty kind output validation path.
is_empty_kind_ns if {
    not is_admin
    count(input.namespace_prefix) >= 3
    input.namespace_prefix[2] == "filter-empty-kind-test"
}

# For the "filter-malformed-test" sub-namespace: return a non-string kind (integer).
# This exercises the malformed-output validation path in InjectFilterPartsWithKind.
kind := 42 if {
    is_malformed_ns
}

# For the "filter-empty-kind-test" sub-namespace: return an empty string kind.
# This exercises the present-but-empty validation path in InjectFilterPartsWithKind.
kind := "" if {
    is_empty_kind_ns
}

# For all other non-admin callers: output kind "default/v1" as a narrowing selector.
# This narrows "default" (family) to "default/v1" (exact within family),
# creates a disjoint intersection with any other family (e.g. "other/v1"),
# and is a no-op when caller already uses exact "default/v1".
kind := "default/v1" if {
    not is_admin
    not is_malformed_ns
    not is_empty_kind_ns
    not is_tenant_filter_ns
}

kind := "tenant/v1" if {
	 is_tenant_filter_ns
}
