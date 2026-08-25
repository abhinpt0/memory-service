package episodic

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"
)

// kindProjectionCache caches compiled Rego programs keyed by the source string.
// Programs are immutable once compiled so no eviction is needed.
var kindProjectionCache sync.Map // key: source string → *rego.PreparedEvalQuery

// Well-known schema name constants.
const (
	DefaultKindName   = "default/v1"
	DefaultKindFamily = "default"
)

// schemaNamePattern matches a single family or version component: lowercase DNS-label-like.
var schemaNameComponentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// attributeNamePattern matches declared attribute field names.
var attributeNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// AttributeType is one of the supported declared attribute types.
type AttributeType string

const (
	AttributeTypeString    AttributeType = "string"
	AttributeTypeNumber    AttributeType = "number"
	AttributeTypeBoolean   AttributeType = "boolean"
	AttributeTypeTimestamp AttributeType = "timestamp"
	AttributeTypeStringArr AttributeType = "string[]"
)

// canonicalTimestampLayout is the storage layout for timestamp attributes.
const canonicalTimestampLayout = "2006-01-02T15:04:05.000000000Z"

// ParseCanonicalKindName parses and validates "family/version".
// Returns family, version, and nil error on success.
func ParseCanonicalKindName(name string) (family, version string, err error) {
	parts := strings.SplitN(name, "/", 3)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("schema name must have exactly one '/' separator, got %q", name)
	}
	family, version = parts[0], parts[1]
	if !isValidSchemaComponent(family) {
		return "", "", fmt.Errorf("schema family %q must be 1–63 lowercase letters/digits/hyphens starting with a letter", family)
	}
	if !isValidSchemaComponent(version) {
		return "", "", fmt.Errorf("schema version %q must be 1–63 lowercase letters/digits/hyphens starting with a letter", version)
	}
	return family, version, nil
}

// ParseKindSelector parses a search/list kind selector:
//   - "family/version"   → exact canonical name (IsExact == true)
//   - "family"           → family selector (IsExact == false)
//   - ""                 → empty selector (callers interpret this as all kinds)
func ParseKindSelector(sel string) (family, canonicalName string, isExact bool) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return DefaultKindFamily, "", false
	}
	if strings.Contains(sel, "/") {
		f, v, err := ParseCanonicalKindName(sel)
		if err == nil {
			return f, sel, true
		}
		_ = v
		return DefaultKindFamily, sel, true // treat as exact even if invalid — store will reject it
	}
	return sel, "", false
}

func isValidSchemaComponent(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	if !unicode.IsLetter(rune(s[0])) || !unicode.IsLower(rune(s[0])) {
		return false
	}
	return schemaNameComponentPattern.MatchString(s)
}

// ValidateKindAttributeTypes checks that every key/value in the declared type map is valid.
func ValidateKindAttributeTypes(types map[string]string) error {
	for name, typ := range types {
		if !attributeNamePattern.MatchString(name) || len(name) > 63 {
			return fmt.Errorf("attribute name %q must match ^[A-Za-z][A-Za-z0-9_-]*$ and be 1–63 chars", name)
		}
		if strings.Contains(name, ".") || strings.HasPrefix(name, "$") {
			return fmt.Errorf("attribute name %q must not contain '.' or start with '$'", name)
		}
		switch AttributeType(typ) {
		case AttributeTypeString, AttributeTypeNumber, AttributeTypeBoolean, AttributeTypeTimestamp, AttributeTypeStringArr:
			// valid
		default:
			return fmt.Errorf("attribute type %q for %q is not supported; use string, number, boolean, timestamp, or string[]", typ, name)
		}
	}
	return nil
}

// CompileKindProjection compiles and validates a projection Rego source.
// Returns a PreparedEvalQuery ready for evaluation.
// Results are cached by source text so repeated calls for the same immutable
// schema version do not recompile.
func CompileKindProjection(ctx context.Context, src string) (*rego.PreparedEvalQuery, error) {
	if len(src) > 256*1024 {
		return nil, fmt.Errorf("projection Rego source exceeds 256 KiB limit")
	}
	if cached, ok := kindProjectionCache.Load(src); ok {
		return cached.(*rego.PreparedEvalQuery), nil
	}
	module, err := ast.ParseModule("projection.rego", src)
	if err != nil {
		return nil, fmt.Errorf("parse projection Rego: %w", err)
	}
	if err := validateKindRegoModule(module); err != nil {
		return nil, err
	}
	pq, err := prepareQuery(ctx, src, "data.memories.attributes.attributes")
	if err != nil {
		return nil, fmt.Errorf("compile projection Rego: %w", err)
	}
	kindProjectionCache.Store(src, pq)
	return pq, nil
}

// allowedInputKeys is the set of permitted top-level input field names
// (i.e. the second element of an input.* ref).
var allowedInputKeys = map[ast.String]struct{}{
	ast.String("namespace"): {},
	ast.String("key"):       {},
	ast.String("value"):     {},
	ast.String("index"):     {},
}

// validateKindRegoModule checks that the parsed module:
//  1. Uses the correct package (memories.attributes).
//  2. Declares an "attributes" rule.
//  3. Does not use nondeterministic builtins anywhere (including rule heads and nested calls).
//  4. Does not reference input roots outside the allowed set anywhere.
func validateKindRegoModule(module *ast.Module) error {
	// Require package memories.attributes.
	const requiredPkg = "memories.attributes"
	if module.Package == nil || module.Package.Path.String() != "data."+requiredPkg {
		return fmt.Errorf("projection Rego must declare `package %s`", requiredPkg)
	}

	// Require at least one rule named "attributes".
	hasAttributes := false
	for _, rule := range module.Rules {
		if rule.Head != nil && rule.Head.Name == "attributes" {
			hasAttributes = true
			break
		}
	}
	if !hasAttributes {
		return fmt.Errorf("projection Rego must define an `attributes` rule")
	}

	var badBuiltins []string
	var badInputRefs []string

	// checkRef uses structural ast.Ref inspection to validate nondeterministic
	// builtins and disallowed input roots.  Item 9 fix: this replaces the old
	// string-split approach that missed bare `input` and dynamic `input[x]` refs.
	//
	// Rules:
	//   len==0             — no-op
	//   ref[0] != Var("input") — not an input ref; check for builtin nondeterminism only
	//   len==1             — bare `input` without field selection → reject
	//   ref[1] not ast.String — dynamic key (input[x]) → reject
	//   ref[1] is ast.String but not in allowedInputKeys → reject
	//   ref[1] is in allowedInputKeys (len>=2) → permitted (deeper indexing OK)
	checkRef := func(ref ast.Ref) {
		if len(ref) == 0 {
			return
		}
		// Nondeterministic builtin check: only possible if the root is a builtin name.
		name := ref.String()
		if b := ast.BuiltinMap[name]; b != nil && b.IsNondeterministic() {
			badBuiltins = append(badBuiltins, name)
		}
		// Input-root validation.
		rootVar, isVar := ref[0].Value.(ast.Var)
		if !isVar || rootVar != "input" {
			return
		}
		if len(ref) == 1 {
			// Bare `input` — no field selection; always reject.
			badInputRefs = append(badInputRefs, ref.String())
			return
		}
		// ref[1] must be a static string key.
		key, isString := ref[1].Value.(ast.String)
		if !isString {
			// Dynamic key e.g. input[some_var] — reject.
			badInputRefs = append(badInputRefs, ref.String())
			return
		}
		if _, allowed := allowedInputKeys[key]; !allowed {
			badInputRefs = append(badInputRefs, ref.String())
		}
		// len >= 2 with an allowed key: deeper indexing (input.namespace[0] etc.) is fine.
	}

	// Walk every term in the module — this covers rule heads, value terms,
	// nested function arguments, and anything WalkExprs might skip.
	ast.WalkTerms(module, func(term *ast.Term) bool {
		if ref, ok := term.Value.(ast.Ref); ok {
			checkRef(ref)
		}
		return false
	})

	if len(badBuiltins) > 0 {
		return fmt.Errorf("projection Rego uses nondeterministic builtins: %s", strings.Join(badBuiltins, ", "))
	}
	if len(badInputRefs) > 0 {
		return fmt.Errorf("projection Rego references disallowed input fields: %s (allowed: input.namespace, input.key, input.value, input.index)", strings.Join(badInputRefs, ", "))
	}
	return nil
}

// EvaluateKindProjection evaluates a projection Rego program over replay inputs.
// Returns the raw result map (not yet validated against declared types).
func EvaluateKindProjection(ctx context.Context, pq *rego.PreparedEvalQuery, namespace []string, key string, value map[string]interface{}, index map[string]string) (map[string]interface{}, error) {
	input := map[string]interface{}{
		"namespace": namespace,
		"key":       key,
		"value":     value,
		"index":     index,
	}
	results, err := pq.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return nil, fmt.Errorf("evaluate schema projection: %w", err)
	}
	if len(results) == 0 || len(results[0].Expressions) == 0 {
		return map[string]interface{}{}, nil
	}
	val := results[0].Expressions[0].Value
	if val == nil {
		return map[string]interface{}{}, nil
	}
	raw, ok := val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("projection Rego must evaluate to an object, got %T", val)
	}
	return raw, nil
}

// ValidateAndNormalizeKindProjection checks the projection result against declared types
// and normalizes timestamp fields to canonical UTC form.
func ValidateAndNormalizeKindProjection(result map[string]interface{}, declaredTypes map[string]string) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(result))
	for key, val := range result {
		if val == nil {
			return nil, fmt.Errorf("attribute %q has explicit null; null values are not supported", key)
		}
		typ, declared := declaredTypes[key]
		if !declared {
			return nil, fmt.Errorf("attribute %q is not declared", key)
		}
		normalized, err := normalizeAttributeValue(key, val, AttributeType(typ))
		if err != nil {
			return nil, err
		}
		out[key] = normalized
	}
	return out, nil
}

func normalizeAttributeValue(name string, val interface{}, typ AttributeType) (interface{}, error) {
	switch typ {
	case AttributeTypeString:
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("attribute %q declared as string but got %T", name, val)
		}
		return s, nil
	case AttributeTypeNumber:
		f, err := toFloat64(val)
		if err != nil {
			return nil, fmt.Errorf("attribute %q declared as number: %w", name, err)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("attribute %q is non-finite; only finite numbers are supported", name)
		}
		return f, nil
	case AttributeTypeBoolean:
		b, ok := val.(bool)
		if !ok {
			return nil, fmt.Errorf("attribute %q declared as boolean but got %T", name, val)
		}
		return b, nil
	case AttributeTypeTimestamp:
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("attribute %q declared as timestamp but got %T", name, val)
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t2, err2 := time.Parse(time.RFC3339, s)
			if err2 != nil {
				return nil, fmt.Errorf("attribute %q declared as timestamp: cannot parse %q as RFC3339: %w", name, s, err)
			}
			t = t2
		}
		return t.UTC().Format(canonicalTimestampLayout), nil
	case AttributeTypeStringArr:
		arr, ok := val.([]interface{})
		if !ok {
			return nil, fmt.Errorf("attribute %q declared as string[] but got %T", name, val)
		}
		out := make([]string, 0, len(arr))
		for i, item := range arr {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("attribute %q[%d] declared as string[] but element is %T", name, i, item)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported attribute type %q for %q", typ, name)
	}
}

func toFloat64(v interface{}) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case float32:
		return float64(t), nil
	case int:
		return float64(t), nil
	case int8:
		return float64(t), nil
	case int16:
		return float64(t), nil
	case int32:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case uint:
		return float64(t), nil
	case uint8:
		return float64(t), nil
	case uint16:
		return float64(t), nil
	case uint32:
		return float64(t), nil
	case uint64:
		return float64(t), nil
	case json.Number:
		// OPA v1.18.2+ returns json.Number for integer and fractional numeric
		// literals in Rego evaluation results.
		f, err := t.Float64()
		if err != nil {
			return 0, fmt.Errorf("cannot parse json.Number %q as float64: %w", t, err)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("cannot convert %T to number", v)
	}
}

// --- Shared sort / filter helpers used by REST and gRPC handlers ---

// KindVersionList is a slice of MemoryKindVersion-like structs used by sort/filter helpers.
// It is defined as a local type to avoid an import cycle with registry/episodic.
type KindVersionList []struct {
	Name           string
	AttributeTypes map[string]string
}

// ResolveSortFieldType resolves the declared attribute type for a sort field across the
// supplied list of regular schema versions.
//
// Rules per Enhancement 115 §Sort:
//
//   - versions == nil / empty → returns ("", nil) — callers use untyped sort.
//   - Exactly one version supplied (exact canonical selector): field must be declared,
//     returns its type; returns error if absent.
//   - Multiple versions (family/all selector): field must be declared in at least one
//     version; all declaring versions must agree on type; conflict → error.
//     Returns error when versions exist but none declares the field.
func ResolveSortFieldType(field string, exactVersion string, versions KindVersionList) (string, error) {
	if len(versions) == 0 {
		return "", nil
	}
	// Exact canonical selector (single version list).
	if exactVersion != "" {
		for _, v := range versions {
			if v.Name == exactVersion {
				t, ok := v.AttributeTypes[field]
				if !ok {
					return "", fmt.Errorf("field %q is not declared in schema version %q", field, exactVersion)
				}
				return t, nil
			}
		}
		return "", fmt.Errorf("schema version %q not found", exactVersion)
	}
	// Family or all-schemas selector — collect across all regular versions.
	var resolvedType string
	var resolvedFrom string
	for _, v := range versions {
		t, ok := v.AttributeTypes[field]
		if !ok {
			continue
		}
		if resolvedType == "" {
			resolvedType = t
			resolvedFrom = v.Name
		} else if resolvedType != t {
			return "", fmt.Errorf("field %q has conflicting types across schema versions: %q declares %q but %q declares %q; select an exact version to resolve",
				field, resolvedFrom, resolvedType, v.Name, t)
		}
	}
	if len(versions) > 0 && resolvedType == "" {
		return "", fmt.Errorf("field %q is not declared in any selected schema version", field)
	}
	return resolvedType, nil
}

// ValidateCallerFilterField validates that a single filter condition is legal for
// the supplied set of regular schema versions.
//
// Policy-injected security filters (policyFilter) must NOT be passed here — the
// built-in namespace/sub fields are always valid.
//
// Rules:
//   - exactVersion is a non-empty canonical name → field must be declared; operator
//     must be supported for the declared type.
//   - exactVersion == "" (family / all selector) → field must be declared in at least
//     one version; all must agree on type; conflict → error; string[] + range
//     operators → error.
func ValidateCallerFilterField(fieldName, op, exactVersion string, versions KindVersionList) error {
	if exactVersion != "" {
		// Exact canonical version.
		for _, v := range versions {
			if v.Name == exactVersion {
				declaredType, ok := v.AttributeTypes[fieldName]
				if !ok {
					return fmt.Errorf("field %q is not declared in schema version %q", fieldName, exactVersion)
				}
				return checkOperatorForType(fieldName, op, AttributeType(declaredType))
			}
		}
		return fmt.Errorf("schema version %q not found", exactVersion)
	}
	// Family or all-schemas selector.
	var resolvedType string
	var resolvedFrom string
	foundAny := false
	for _, v := range versions {
		t, ok := v.AttributeTypes[fieldName]
		if !ok {
			continue
		}
		foundAny = true
		if resolvedType == "" {
			resolvedType = t
			resolvedFrom = v.Name
		} else if resolvedType != t {
			return fmt.Errorf("field %q has conflicting types across schema versions: %q declares %q but %q declares %q; select an exact version",
				fieldName, resolvedFrom, resolvedType, v.Name, t)
		}
	}
	if !foundAny {
		return fmt.Errorf("field %q is not declared in any selected schema version", fieldName)
	}
	return checkOperatorForType(fieldName, op, AttributeType(resolvedType))
}

// checkOperatorForType rejects operators that are not supported for the given type.
func checkOperatorForType(field, op string, typ AttributeType) error {
	switch typ {
	case AttributeTypeString:
		switch op {
		case "$eq", "$in", "$exists":
			return nil
		default:
			return fmt.Errorf("operator %q is not supported for string field %q", op, field)
		}
	case AttributeTypeNumber:
		return nil // all operators supported
	case AttributeTypeBoolean:
		switch op {
		case "$eq", "$in", "$exists":
			return nil
		default:
			return fmt.Errorf("operator %q is not supported for boolean field %q", op, field)
		}
	case AttributeTypeTimestamp:
		return nil // all operators supported
	case AttributeTypeStringArr:
		switch op {
		case "$eq", "$in", "$exists":
			return nil
		default:
			return fmt.Errorf("operator %q is not supported for string[] field %q", op, field)
		}
	}
	return nil
}

// ValidateAndNormalizeCallerFilterValues validates and normalizes the values for a single
// caller filter condition against the resolved attribute type and operator.
//
// Rules per Enhancement 115 §CallerFilter:
//   - $exists: values must be empty (no value expected).
//   - string: each value must be a string.
//   - number: each value must be a finite float64 or json.Number convertible to one.
//   - boolean: each value must be a bool.
//   - timestamp $eq/$in: each value must be parseable as RFC3339/RFC3339Nano and is rewritten
//     to canonicalTimestampLayout ("2006-01-02T15:04:05.000000000Z") in UTC.
//   - string[]: values are treated as strings (same rule as string).
//
// Returns the normalized values (a new slice) or an error.
// When fieldType == "" (untyped — legacy or no schema), returns values unchanged.
func ValidateAndNormalizeCallerFilterValues(fieldName, op, fieldType string, rawValues []interface{}) ([]interface{}, error) {
	if op == "$exists" {
		if len(rawValues) > 0 {
			return nil, fmt.Errorf("field %q: $exists does not take a value", fieldName)
		}
		return nil, nil
	}
	if fieldType == "" {
		// Untyped — no validation, return as-is.
		return rawValues, nil
	}
	out := make([]interface{}, 0, len(rawValues))
	for i, v := range rawValues {
		switch AttributeType(fieldType) {
		case AttributeTypeString, AttributeTypeStringArr:
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("field %q[%d]: expected string value, got %T", fieldName, i, v)
			}
			out = append(out, s)
		case AttributeTypeNumber:
			f, err := toFloat64(v)
			if err != nil {
				return nil, fmt.Errorf("field %q[%d]: expected finite number value: %w", fieldName, i, err)
			}
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, fmt.Errorf("field %q[%d]: non-finite number not allowed", fieldName, i)
			}
			out = append(out, f)
		case AttributeTypeBoolean:
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("field %q[%d]: expected boolean value, got %T", fieldName, i, v)
			}
			out = append(out, b)
		case AttributeTypeTimestamp:
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("field %q[%d]: expected RFC3339 timestamp string, got %T", fieldName, i, v)
			}
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				t2, err2 := time.Parse(time.RFC3339, s)
				if err2 != nil {
					return nil, fmt.Errorf("field %q[%d]: cannot parse %q as RFC3339 timestamp: %w", fieldName, i, s, err)
				}
				t = t2
			}
			out = append(out, t.UTC().Format(canonicalTimestampLayout))
		default:
			// Unknown type — pass through.
			out = append(out, v)
		}
	}
	return out, nil
}
