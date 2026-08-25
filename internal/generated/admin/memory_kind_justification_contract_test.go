package admin_test

import (
	"reflect"
	"testing"

	generatedadmin "github.com/chirino/memory-service/internal/generated/admin"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
)

func TestMemoryKindOperationsExposeJustificationInOpenAPIAndGeneratedBindings(t *testing.T) {
	t.Parallel()
	spec, err := generatedadmin.GetSwagger()
	require.NoError(t, err)
	tests := []struct {
		method string
		path   string
		params any
	}{
		{"POST", "/admin/v1/memory-kinds", generatedadmin.AdminCreateMemoryKindVersionParams{}},
		{"GET", "/admin/v1/memory-kinds", generatedadmin.AdminListMemoryKindVersionsParams{}},
		{"GET", "/admin/v1/memory-kinds/{family}/{version}", generatedadmin.AdminGetMemoryKindVersionParams{}},
		{"POST", "/admin/v1/memory-kind-migrations", generatedadmin.AdminCreateMemoryKindMigrationParams{}},
		{"GET", "/admin/v1/memory-kind-migrations", generatedadmin.AdminListMemoryKindMigrationsParams{}},
		{"GET", "/admin/v1/memory-kind-migrations/{id}", generatedadmin.AdminGetMemoryKindMigrationParams{}},
		{"DELETE", "/admin/v1/memory-kind-migrations/{id}", generatedadmin.AdminCancelMemoryKindMigrationParams{}},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			pathItem := spec.Paths.Find(tc.path)
			require.NotNil(t, pathItem)
			operation := operationForMethod(pathItem, tc.method)
			require.NotNil(t, operation)
			found := false
			for _, parameter := range operation.Parameters {
				if parameter.Value != nil && parameter.Value.Name == "justification" && parameter.Value.In == openapi3.ParameterInQuery {
					found = true
					break
				}
			}
			require.True(t, found, "OpenAPI operation must expose the justification query parameter")
			field, ok := reflect.TypeOf(tc.params).FieldByName("Justification")
			require.True(t, ok, "generated binding must expose Justification")
			require.Equal(t, reflect.TypeOf((*string)(nil)), field.Type)
		})
	}
}

func operationForMethod(item *openapi3.PathItem, method string) *openapi3.Operation {
	switch method {
	case "GET":
		return item.Get
	case "POST":
		return item.Post
	case "PUT":
		return item.Put
	case "DELETE":
		return item.Delete
	default:
		return nil
	}
}
