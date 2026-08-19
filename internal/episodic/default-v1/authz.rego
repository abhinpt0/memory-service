package memories.authz

default decision = {"allow": false, "reason": "access denied"}

# Users may access their own namespace subtree.
# For writes, also enforce a max index field count.
decision = {"allow": true} if {
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    input.operation != "write"
}

decision = {"allow": true} if {
    input.operation == "write"
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    count(object.keys(input.index)) <= 8
}

decision = {"allow": false, "reason": "too many index fields (max 8)"} if {
    input.operation == "write"
    input.namespace[0] == "user"
    input.namespace[1] == input.context.user_id
    count(object.keys(input.index)) > 8
}
