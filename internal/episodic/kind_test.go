package episodic

import (
	"encoding/json"
	"testing"

	"github.com/open-policy-agent/opa/v1/ast"
)

// --- Defect 3: toFloat64 must accept json.Number ---

func TestToFloat64AcceptsJsonNumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input interface{}
		want  float64
	}{
		{json.Number("42"), 42},
		{json.Number("3.14"), 3.14},
		{json.Number("-7"), -7},
		{float64(1.5), 1.5},
		{int(10), 10},
		{int64(100), 100},
	}
	for _, c := range cases {
		got, err := toFloat64(c.input)
		if err != nil {
			t.Errorf("toFloat64(%v %T) error: %v", c.input, c.input, err)
			continue
		}
		if got != c.want {
			t.Errorf("toFloat64(%v) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestToFloat64RejectsMalformedJsonNumber(t *testing.T) {
	t.Parallel()
	_, err := toFloat64(json.Number("not-a-number"))
	if err == nil {
		t.Fatal("expected error for malformed json.Number, got nil")
	}
}

func TestToFloat64RejectsUnsupportedTypes(t *testing.T) {
	t.Parallel()
	_, err := toFloat64("hello")
	if err == nil {
		t.Fatal("expected error for string, got nil")
	}
}

// --- Defect 9: validateKindRegoModule must catch nondeterministic builtins ---

func mustParseModule(t *testing.T, src string) *ast.Module {
	t.Helper()
	module, err := ast.ParseModule("projection.rego", src)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	return module
}

const validRegoSrc = `
package memories.attributes

attributes := {"score": 1.0}
`

const regoWithUUID = `
package memories.attributes

attributes := {"id": uuid.rfc4122("seed")}
`

const regoWithBadInput = `
package memories.attributes

attributes := {"val": input.hidden_field}
`

const regoWithNestedBadBuiltin = `
package memories.attributes

helper := uuid.rfc4122("x")
attributes := {"h": helper}
`

const regoWithAllowedInputs = `
package memories.attributes

attributes := {
  "ns":  input.namespace,
  "k":   input.key,
  "val": input.value,
  "idx": input.index,
}
`

func TestValidateKindRegoModuleAcceptsValidModule(t *testing.T) {
	t.Parallel()
	if err := validateKindRegoModule(mustParseModule(t, validRegoSrc)); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateKindRegoModuleAcceptsAllAllowedInputRoots(t *testing.T) {
	t.Parallel()
	if err := validateKindRegoModule(mustParseModule(t, regoWithAllowedInputs)); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateKindRegoModuleRejectsNondeterministicBuiltin(t *testing.T) {
	t.Parallel()
	if err := validateKindRegoModule(mustParseModule(t, regoWithUUID)); err == nil {
		t.Fatal("expected error for uuid.rfc4122, got nil")
	}
}

func TestValidateKindRegoModuleRejectsDisallowedInputRoot(t *testing.T) {
	t.Parallel()
	if err := validateKindRegoModule(mustParseModule(t, regoWithBadInput)); err == nil {
		t.Fatal("expected error for input.hidden_field, got nil")
	}
}

func TestValidateKindRegoModuleRejectsNestedNondeterministicBuiltin(t *testing.T) {
	t.Parallel()
	// uuid in a helper rule (not directly in attributes rule head) must still be caught.
	if err := validateKindRegoModule(mustParseModule(t, regoWithNestedBadBuiltin)); err == nil {
		t.Fatal("expected error for nested uuid.rfc4122, got nil")
	}
}

// --- Defect 7: ResolveSortFieldType across schema versions ---

func TestResolveSortFieldTypeExactVersion(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number", "label": "string"}},
	}
	typ, err := ResolveSortFieldType("score", "events/v1", versions)
	if err != nil || typ != "number" {
		t.Errorf("expected number, got %q err=%v", typ, err)
	}
}

func TestResolveSortFieldTypeMissingFieldInExactVersion(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
	}
	_, err := ResolveSortFieldType("missing", "events/v1", versions)
	if err == nil {
		t.Fatal("expected error for missing field in exact version")
	}
}

func TestResolveSortFieldTypeFamilyAllVersionsAgree(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
		{Name: "events/v2", AttributeTypes: map[string]string{"score": "number", "extra": "string"}},
	}
	typ, err := ResolveSortFieldType("score", "", versions)
	if err != nil || typ != "number" {
		t.Errorf("expected number, got %q err=%v", typ, err)
	}
}

func TestResolveSortFieldTypeFamilyConflictReturnsError(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
		{Name: "events/v2", AttributeTypes: map[string]string{"score": "string"}},
	}
	_, err := ResolveSortFieldType("score", "", versions)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
}

func TestResolveSortFieldTypeExactVersionUntypedField(t *testing.T) {
	t.Parallel()
	// A version with no attribute types returns an error when the field is requested.
	versions := KindVersionList{{Name: "test/noattr", AttributeTypes: map[string]string{}}}
	_, err := ResolveSortFieldType("anything", "test/noattr", versions)
	if err == nil {
		t.Errorf("expected error for undeclared field in exact version")
	}
}

func TestResolveSortFieldTypeOtherVersionFieldNotDeclared(t *testing.T) {
	t.Parallel()
	// A version with no relevant field alongside one that declares the field.
	versions := KindVersionList{
		{Name: "events/v0", AttributeTypes: map[string]string{}},
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
	}
	typ, err := ResolveSortFieldType("score", "", versions)
	if err != nil || typ != "number" {
		t.Errorf("expected number (versions with no declaration skipped), got %q err=%v", typ, err)
	}
}

func TestResolveSortFieldTypeEmptyVersionsReturnsEmpty(t *testing.T) {
	t.Parallel()
	typ, err := ResolveSortFieldType("anything", "", KindVersionList{})
	if err != nil || typ != "" {
		t.Errorf("expected empty for no versions, got %q err=%v", typ, err)
	}
}

// --- Defect 8: ValidateCallerFilterField ---

func TestValidateCallerFilterFieldUntypedVersionPermitsEqOp(t *testing.T) {
	t.Parallel()
	// A version with no declared types still can match $eq on a known field if exact-version is requested.
	// Here the field is NOT declared so it returns an error.
	versions := KindVersionList{{Name: "test/noattr", AttributeTypes: map[string]string{}}}
	err := ValidateCallerFilterField("anything", "$eq", "test/noattr", versions)
	if err == nil {
		t.Errorf("expected error for undeclared field in untyped version")
	}
}

func TestValidateCallerFilterFieldExactVersionUndeclaredField(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
	}
	if err := ValidateCallerFilterField("missing", "$eq", "events/v1", versions); err == nil {
		t.Fatal("expected error for undeclared field")
	}
}

func TestValidateCallerFilterFieldStringRejectsRangeOp(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"label": "string"}},
	}
	if err := ValidateCallerFilterField("label", "$gt", "events/v1", versions); err == nil {
		t.Fatal("expected error for $gt on string")
	}
}

func TestValidateCallerFilterFieldNumberPermitsAllOps(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
	}
	for _, op := range []string{"$eq", "$gt", "$lt", "$gte", "$lte"} {
		if err := ValidateCallerFilterField("score", op, "events/v1", versions); err != nil {
			t.Errorf("number should permit %s, got: %v", op, err)
		}
	}
}

func TestValidateCallerFilterFieldFamilyConflictReturnsError(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
		{Name: "events/v2", AttributeTypes: map[string]string{"score": "string"}},
	}
	if err := ValidateCallerFilterField("score", "$eq", "", versions); err == nil {
		t.Fatal("expected conflict error for mismatched types across versions")
	}
}

func TestValidateCallerFilterFieldFamilyUndeclaredField(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
	}
	if err := ValidateCallerFilterField("missing", "$eq", "", versions); err == nil {
		t.Fatal("expected error for undeclared field across family")
	}
}

// --- Item 5: ResolveSortFieldType returns error when no non-legacy version declares the field ---

func TestResolveSortFieldTypeFamilyNoVersionDeclaresFieldReturnsError(t *testing.T) {
	t.Parallel()
	versions := KindVersionList{
		{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
		{Name: "events/v2", AttributeTypes: map[string]string{"score": "number", "label": "string"}},
	}
	// "missing" is not declared in any version — must return error, not ("", nil).
	_, err := ResolveSortFieldType("missing", "", versions)
	if err == nil {
		t.Fatal("expected error when no version declares the field, got nil")
	}
}

func TestResolveSortFieldTypeFamilyWithOnlyUntypedVersionReturnsError(t *testing.T) {
	t.Parallel()
	// Only a version with empty attribute types — no field can be resolved.
	// Per spec: if versions exist but no version declares the field, return error.
	versions := KindVersionList{{Name: "test/v1", AttributeTypes: map[string]string{}}}
	_, err := ResolveSortFieldType("anything", "", versions)
	if err == nil {
		t.Errorf("expected error for undeclared field across versions with no declared types")
	}
}

// --- Item 9: structural Rego ref check ---

const regoWithBareInput = `
package memories.attributes

attributes := input
`

const regoWithDynamicInputKey = `
package memories.attributes

somevar := "key"
attributes := {"val": input[somevar]}
`

const regoWithAllowedInputIndexing = `
package memories.attributes

attributes := {"first_ns": input.namespace[0]}
`

func TestValidateKindRegoModuleRejectsBareInput(t *testing.T) {
	t.Parallel()
	// `input` without a field selection is disallowed.
	if err := validateKindRegoModule(mustParseModule(t, regoWithBareInput)); err == nil {
		t.Fatal("expected error for bare input reference, got nil")
	}
}

func TestValidateKindRegoModuleRejectsDynamicInputKey(t *testing.T) {
	t.Parallel()
	// `input[somevar]` uses a dynamic key — disallowed.
	if err := validateKindRegoModule(mustParseModule(t, regoWithDynamicInputKey)); err == nil {
		t.Fatal("expected error for dynamic input key input[somevar], got nil")
	}
}

func TestValidateKindRegoModulePermitsDeepIndexingOnAllowedRoot(t *testing.T) {
	t.Parallel()
	// `input.namespace[0]` is deep indexing on an allowed root — must be permitted.
	if err := validateKindRegoModule(mustParseModule(t, regoWithAllowedInputIndexing)); err != nil {
		t.Fatalf("unexpected error for input.namespace[0]: %v", err)
	}
}

// --- Item 6: ValidateAndNormalizeCallerFilterValues ---

func TestValidateAndNormalizeCallerFilterValuesTimestampCanonicalization(t *testing.T) {
	t.Parallel()
	// RFC3339 timestamp should be parsed and rewritten to canonical UTC form.
	out, err := ValidateAndNormalizeCallerFilterValues("ts", "$eq", "timestamp", []interface{}{"2024-03-15T10:30:00Z"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 output, got %d", len(out))
	}
	s, ok := out[0].(string)
	if !ok {
		t.Fatalf("expected string output, got %T", out[0])
	}
	// Should be in canonical nanosecond UTC form.
	if s != "2024-03-15T10:30:00.000000000Z" {
		t.Errorf("canonical timestamp = %q, want 2024-03-15T10:30:00.000000000Z", s)
	}
}

func TestValidateAndNormalizeCallerFilterValuesExistsRejectsValues(t *testing.T) {
	t.Parallel()
	_, err := ValidateAndNormalizeCallerFilterValues("f", "$exists", "string", []interface{}{"unexpected"})
	if err == nil {
		t.Fatal("expected error for $exists with values, got nil")
	}
}

func TestValidateAndNormalizeCallerFilterValuesExistsNoValueOK(t *testing.T) {
	t.Parallel()
	_, err := ValidateAndNormalizeCallerFilterValues("f", "$exists", "string", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAndNormalizeCallerFilterValuesWrongTypeRejected(t *testing.T) {
	t.Parallel()
	_, err := ValidateAndNormalizeCallerFilterValues("score", "$eq", "number", []interface{}{"not-a-number"})
	if err == nil {
		t.Fatal("expected error for string value on number field, got nil")
	}
}

func TestValidateAndNormalizeCallerFilterValuesBooleanOK(t *testing.T) {
	t.Parallel()
	out, err := ValidateAndNormalizeCallerFilterValues("flag", "$eq", "boolean", []interface{}{true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != true {
		t.Errorf("expected [true], got %v", out)
	}
}

func TestValidateAndNormalizeCallerFilterValuesUntypedPassThrough(t *testing.T) {
	t.Parallel()
	// Empty fieldType = untyped (legacy) — values pass through unchanged.
	val := []interface{}{"anything", 42}
	out, err := ValidateAndNormalizeCallerFilterValues("f", "$eq", "", val)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("expected 2 values pass-through, got %d", len(out))
	}
}
