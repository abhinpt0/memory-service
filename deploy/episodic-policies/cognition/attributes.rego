package memories.attributes

# Optional cognition policy for deployments that want type-aware memory search.
# It preserves the built-in namespace/sub attributes and adds only safe, compact
# cognition metadata. Do not extract raw content, citations, prompts, provider IDs,
# or client metadata.
#
# Temporal fields (observed_at / effective_at) are read from input.value and
# promoted only when they parse as valid RFC3339 timestamps; malformed, empty,
# or non-string values are silently omitted. Promoted values are normalised to
# fixed-width nanosecond-precision UTC so lexicographic order equals
# chronological order on all store backends.

default attributes = {}

base_attributes = {"namespace": input.namespace[0], "sub": input.namespace[1]} if {
    count(input.namespace) >= 2
}

base_attributes = {} if {
    count(input.namespace) < 2
}

memory_kind = kind if {
    is_string(input.value.kind)
    kind := input.value.kind
}

memory_kind = kind if {
    not is_string(input.value.kind)
    count(input.namespace) > 0
    kind := input.namespace[count(input.namespace) - 1]
}

cognition_attributes["memoryKind"] = kind if {
    kind := memory_kind
}

cognition_attributes["runtimeId"] = runtime_id if {
    is_string(input.value.provenance.runtime_id)
    runtime_id := input.value.provenance.runtime_id
}

cognition_attributes["runtimeId"] = runtime_id if {
    not is_string(input.value.provenance.runtime_id)
    is_string(input.value.runtime.id)
    runtime_id := input.value.runtime.id
}

cognition_attributes["runtimeVersion"] = runtime_version if {
    is_string(input.value.provenance.runtime_version)
    runtime_version := input.value.provenance.runtime_version
}

cognition_attributes["runtimeVersion"] = runtime_version if {
    not is_string(input.value.provenance.runtime_version)
    is_string(input.value.runtime.version)
    runtime_version := input.value.runtime.version
}

cognition_attributes["confidence"] = "high" if {
    confidence := input.value.confidence
    is_number(confidence)
    confidence >= 0.8
}

cognition_attributes["confidence"] = "medium" if {
    confidence := input.value.confidence
    is_number(confidence)
    confidence >= 0.5
    confidence < 0.8
}

cognition_attributes["confidence"] = "low" if {
    confidence := input.value.confidence
    is_number(confidence)
    confidence < 0.5
}

cognition_attributes["conversationIds"] = conversation_id if {
    is_string(input.value.provenance.conversation_id)
    conversation_id := input.value.provenance.conversation_id
}

cognition_attributes["entryIds"] = entry_id if {
    is_array(input.value.provenance.entry_ids)
    count(input.value.provenance.entry_ids) > 0
    entry_id := input.value.provenance.entry_ids[0]
}

# Promote observed_at from the value struct as a fixed-width nanosecond-precision
# UTC string. The rule does not fire if the field is absent, non-string, or not
# valid RFC3339.
cognition_attributes["observedAt"] = ts_norm if {
    raw := input.value.observed_at
    is_string(raw)
    ns := time.parse_rfc3339_ns(raw)
    ts_norm := time.format([ns, "UTC", "2006-01-02T15:04:05.000000000Z"])
}

# Promote effective_at from the value struct as a fixed-width nanosecond-precision
# UTC string. The rule does not fire if the field is absent, non-string, or not
# valid RFC3339.
cognition_attributes["effectiveAt"] = ts_norm if {
    raw := input.value.effective_at
    is_string(raw)
    ns := time.parse_rfc3339_ns(raw)
    ts_norm := time.format([ns, "UTC", "2006-01-02T15:04:05.000000000Z"])
}

attributes = object.union(base_attributes, cognition_attributes)
