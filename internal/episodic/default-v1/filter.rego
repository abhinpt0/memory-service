package memories.filter

# Public API callers are constrained to their own user subtree, regardless of
# administrative roles. Cross-user administration uses the dedicated admin API.
# If the request is already narrower under user/<user>, keep it.
namespace_prefix := input.namespace_prefix if {
    starts_with(input.namespace_prefix, user_prefix)
}
namespace_prefix := user_prefix if {
    not starts_with(input.namespace_prefix, user_prefix)
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

# Authorization scoping is entirely expressed by namespace_prefix. Memory-kind
# projections are user-defined and are not required to contain security fields.
attribute_filter := {}
