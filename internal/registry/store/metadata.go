package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// MetadataPatch is a decoded top-level metadata merge-patch. A present key with
// a nil value deletes that key; a non-nil value replaces the complete top-level
// value; absent keys remain unchanged.
type MetadataPatch map[string]interface{}

// ChangesAgainst removes patch keys that already have the requested value.
func (p MetadataPatch) ChangesAgainst(current map[string]interface{}) MetadataPatch {
	if len(p) == 0 {
		return nil
	}
	changes := make(MetadataPatch)
	for key, requested := range p {
		existing, found := current[key]
		if requested == nil {
			if found {
				changes[key] = nil
			}
			continue
		}
		if !found || !metadataValuesEqual(existing, requested) {
			changes[key] = requested
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return changes
}

// metadataValuesEqual compares JSON-like metadata semantically across datastore
// representations. MongoDB decodes nested documents and arrays into named BSON
// slice types, while JSON requests and SQL stores use maps and []interface{}.
func metadataValuesEqual(left, right interface{}) bool {
	leftValue, leftOK := normalizeMetadataJSONValue(reflect.ValueOf(left))
	rightValue, rightOK := normalizeMetadataJSONValue(reflect.ValueOf(right))
	return leftOK && rightOK && jsonSemanticEqual(leftValue, rightValue)
}

func normalizeMetadataJSONValue(value reflect.Value) (interface{}, bool) {
	if !value.IsValid() {
		return nil, true
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, true
		}
		value = value.Elem()
	}
	if value.CanInterface() {
		if number, ok := value.Interface().(json.Number); ok {
			return number, true
		}
	}

	switch value.Kind() {
	case reflect.Bool:
		return value.Bool(), true
	case reflect.String:
		return value.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return json.Number(strconv.FormatInt(value.Int(), 10)), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return json.Number(strconv.FormatUint(value.Uint(), 10)), true
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, false
		}
		return json.Number(strconv.FormatFloat(number, 'g', -1, value.Type().Bits())), true
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		result := make(map[string]interface{}, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			normalized, ok := normalizeMetadataJSONValue(iterator.Value())
			if !ok {
				return nil, false
			}
			result[iterator.Key().String()] = normalized
		}
		return result, true
	case reflect.Slice, reflect.Array:
		if document, recognized, ok := normalizeMetadataOrderedDocument(value); recognized {
			return document, ok
		}
		result := make([]interface{}, value.Len())
		for i := 0; i < value.Len(); i++ {
			normalized, ok := normalizeMetadataJSONValue(value.Index(i))
			if !ok {
				return nil, false
			}
			result[i] = normalized
		}
		return result, true
	default:
		return nil, false
	}
}

// normalizeMetadataOrderedDocument recognizes BSON's []E representation without
// coupling the datastore-neutral registry package to the MongoDB driver.
func normalizeMetadataOrderedDocument(value reflect.Value) (map[string]interface{}, bool, bool) {
	elementType := value.Type().Elem()
	if elementType.Kind() != reflect.Struct {
		return nil, false, false
	}
	keyField, hasKey := elementType.FieldByName("Key")
	valueField, hasValue := elementType.FieldByName("Value")
	if !hasKey || !hasValue || keyField.Type.Kind() != reflect.String {
		return nil, false, false
	}

	result := make(map[string]interface{}, value.Len())
	for i := 0; i < value.Len(); i++ {
		element := value.Index(i)
		key := element.FieldByIndex(keyField.Index).String()
		if _, duplicate := result[key]; duplicate {
			return nil, true, false
		}
		normalized, ok := normalizeMetadataJSONValue(element.FieldByIndex(valueField.Index))
		if !ok {
			return nil, true, false
		}
		result[key] = normalized
	}
	return result, true, true
}

// DecodeMetadataPatch converts a JSON top-level metadata patch into a map
// where explicit JSON null values are represented as Go nil, and absent keys are omitted.
// This enables store-level atomic merge-patch operations.
func DecodeMetadataPatch(patch []byte) (MetadataPatch, error) {
	if len(patch) == 0 || string(bytes.TrimSpace(patch)) == "null" {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(patch)
	if len(trimmed) > 0 && trimmed[0] != '{' {
		return nil, &BadRequestError{Message: "metadata must be an object"}
	}
	var patchMap map[string]json.RawMessage
	if err := json.Unmarshal(patch, &patchMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata patch: %w", err)
	}
	result := make(MetadataPatch, len(patchMap))
	for k, v := range patchMap {
		trimmed := bytes.TrimSpace(v)
		if string(trimmed) == "null" {
			result[k] = nil
		} else {
			var val interface{}
			if err := json.Unmarshal(v, &val); err != nil {
				return nil, fmt.Errorf("failed to unmarshal metadata value for key %q: %w", k, err)
			}
			result[k] = val
		}
	}
	return result, nil
}

// MetadataFilterFromOptionalPair constructs a metadata filter from optional key/value inputs.
// Presence, not content, determines whether a field was supplied. Neither present returns nil.
// Exactly one present is invalid. Empty keys are invalid. Explicitly present empty values are valid.
func MetadataFilterFromOptionalPair(key, value *string) (*MetadataKeyFilter, error) {
	keyPresent := key != nil
	valuePresent := value != nil

	switch {
	case !keyPresent && !valuePresent:
		return nil, nil
	case !keyPresent:
		return nil, fmt.Errorf("metadata filter key is required when metadata filter value is set")
	case !valuePresent:
		return nil, fmt.Errorf("metadata filter value is required when metadata filter key is set")
	}

	if !IsValidMetadataKey(*key) {
		return nil, fmt.Errorf("invalid metadata filter key")
	}

	return &MetadataKeyFilter{
		Key:   *key,
		Value: *value,
	}, nil
}

// ParseMetadataFilterQuery parses a REST deepObject metadata[key]=value filter.
// rawQuery is required because url.Values represents both metadata[key] and
// metadata[key]= as an empty string, while only the latter is a supplied value.
func ParseMetadataFilterQuery(rawQuery string) (*MetadataKeyFilter, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid metadata filter query: %w", err)
	}
	hasEquals, err := metadataQueryEqualsPresence(rawQuery)
	if err != nil {
		return nil, err
	}

	var filter *MetadataKeyFilter
	for rawKey, vals := range values {
		if !strings.HasPrefix(rawKey, "metadata") {
			continue
		}
		if rawKey == "metadata" || !strings.HasPrefix(rawKey, "metadata[") || !strings.HasSuffix(rawKey, "]") {
			return nil, fmt.Errorf("invalid metadata filter parameter %q", rawKey)
		}

		key := rawKey[len("metadata[") : len(rawKey)-1]
		if strings.ContainsAny(key, "[]") || !IsValidMetadataKey(key) {
			return nil, fmt.Errorf("invalid metadata filter key")
		}
		if len(vals) != 1 {
			return nil, fmt.Errorf("metadata filter must have exactly one value")
		}
		if present, ok := hasEquals[rawKey]; !ok || !present {
			return nil, fmt.Errorf("metadata filter value is required when metadata filter key is set")
		}
		if filter != nil {
			return nil, fmt.Errorf("metadata filter must specify exactly one key")
		}

		filter = &MetadataKeyFilter{Key: key, Value: vals[0]}
	}

	return filter, nil
}

func metadataQueryEqualsPresence(rawQuery string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, fragment := range strings.Split(rawQuery, "&") {
		if fragment == "" {
			continue
		}
		name := fragment
		present := false
		if index := strings.IndexByte(fragment, '='); index >= 0 {
			name = fragment[:index]
			present = true
		}
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			return nil, fmt.Errorf("invalid metadata filter parameter %q: %w", name, err)
		}
		result[decoded] = present
	}
	return result, nil
}
