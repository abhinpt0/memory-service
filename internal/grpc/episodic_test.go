package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/episodic"
	pb "github.com/chirino/memory-service/internal/generated/pb/memory/v1"
	"github.com/chirino/memory-service/internal/model"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/chirino/memory-service/internal/security"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gogrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

type resolveCountingGRPCStore struct {
	registryepisodic.EpisodicStore
	resolveCalls int
	resolveErr   error
	putRequest   registryepisodic.PutMemoryRequest
}

func (s *resolveCountingGRPCStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *resolveCountingGRPCStore) ResolveKindForWrite(context.Context, string) (string, error) {
	s.resolveCalls++
	if s.resolveErr != nil {
		return "", s.resolveErr
	}
	if s.resolveCalls == 1 {
		return "resolved/v1", nil
	}
	return "changed/v1", nil
}

func TestGRPCPutSanitizesUnexpectedKindResolutionErrors(t *testing.T) {
	store := &resolveCountingGRPCStore{resolveErr: errors.New("dial postgres at secret.internal:5432")}
	_, err := executeGRPCPut(t, store)
	require.Equal(t, codes.Internal, status.Code(err))
	require.NotContains(t, status.Convert(err).Message(), "secret.internal")
}

func TestGRPCPutMapsKnownKindResolutionErrorsToInvalidArgument(t *testing.T) {
	store := &resolveCountingGRPCStore{resolveErr: registryepisodic.ErrMemoryKindNotFound}
	_, err := executeGRPCPut(t, store)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func executeGRPCPut(t *testing.T, store registryepisodic.EpisodicStore) (*pb.MemoryWriteResult, error) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Mode = config.ModeTesting
	cfg.LocalUserID = "alice"
	cfg.UnixSocketAuth = "local"
	resolver, err := security.NewTokenResolver(&cfg)
	require.NoError(t, err)
	value, err := structpb.NewStruct(map[string]interface{}{"note": "value"})
	require.NoError(t, err)
	req := &pb.PutMemoryRequest{Namespace: []string{"user", "alice", "resolve-error"}, Key: "key", Value: value}
	server := &MemoriesServer{Store: store, Config: &cfg}
	interceptor := security.GRPCUnaryInterceptorWithRateLimiter(resolver, nil)
	response, callErr := interceptor(context.Background(), req, &gogrpc.UnaryServerInfo{}, func(ctx context.Context, raw any) (any, error) {
		return server.PutMemory(ctx, raw.(*pb.PutMemoryRequest))
	})
	if callErr != nil {
		return nil, callErr
	}
	return response.(*pb.MemoryWriteResult), nil
}

func TestKindVersionToProtoOmitsRegoFromListShape(t *testing.T) {
	rego := "secret marker"
	version := &model.MemoryKindVersion{Name: "private/v1", AttributesRego: &rego}
	require.Empty(t, kindVersionToProto(version, false).GetProjectionRego())
	require.Equal(t, "secret marker", kindVersionToProto(version, true).GetProjectionRego())
}

func TestAdminMemoryKindLifecycleRequiresGRPCJustification(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Mode = config.ModeTesting
	cfg.LocalUserID = "alice"
	cfg.UnixSocketAuth = "local"
	cfg.AdminUsers = "alice"
	cfg.RequireJustification = true
	resolver, err := security.NewTokenResolver(&cfg)
	require.NoError(t, err)
	server := &AdminMemoryKindServer{Config: &cfg}
	interceptor := security.GRPCUnaryInterceptorWithRateLimiter(resolver, nil)
	operations := []string{
		"CreateMemoryKindVersion", "ListMemoryKindVersions", "GetMemoryKindVersion",
		"CreateMemoryKindMigration", "ListMemoryKindMigrations", "GetMemoryKindMigration", "CancelMemoryKindMigration",
	}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			_, err := interceptor(context.Background(), &emptypb.Empty{}, &gogrpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
				return nil, server.requireAdmin(ctx, operation, "")
			})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			_, err = interceptor(context.Background(), &emptypb.Empty{}, &gogrpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
				return nil, server.requireAdmin(ctx, operation, "approved")
			})
			require.NoError(t, err)
		})
	}
}

func (s *resolveCountingGRPCStore) GetMemoryKindVersion(_ context.Context, name string) (*model.MemoryKindVersion, error) {
	return &model.MemoryKindVersion{Name: name, AttributeTypes: map[string]string{}, Writable: true}, nil
}

func (s *resolveCountingGRPCStore) PutMemory(_ context.Context, req registryepisodic.PutMemoryRequest) (*registryepisodic.MemoryWriteResult, error) {
	s.putRequest = req
	return &registryepisodic.MemoryWriteResult{
		ID: uuid.MustParse("9b141ab5-2e91-4ea3-8eb3-6c193062c6aa"), Namespace: req.Namespace, Key: req.Key,
		MemoryKind: req.MemoryKind, Revision: 1, CreatedAt: time.Now().UTC(),
	}, nil
}

func TestGRPCPutResolvesKindExactlyOnce(t *testing.T) {
	store := &resolveCountingGRPCStore{}
	cfg := config.DefaultConfig()
	cfg.Mode = config.ModeTesting
	cfg.LocalUserID = "alice"
	cfg.UnixSocketAuth = "local"
	resolver, err := security.NewTokenResolver(&cfg)
	require.NoError(t, err)
	value, err := structpb.NewStruct(map[string]interface{}{"note": "value"})
	require.NoError(t, err)
	req := &pb.PutMemoryRequest{
		Namespace: []string{"user", "alice", "single-resolve"}, Key: "key", Value: value,
	}
	server := &MemoriesServer{Store: store, Config: &cfg}

	interceptor := security.GRPCUnaryInterceptorWithRateLimiter(resolver, nil)
	response, err := interceptor(context.Background(), req, &gogrpc.UnaryServerInfo{}, func(ctx context.Context, raw any) (any, error) {
		return server.PutMemory(ctx, raw.(*pb.PutMemoryRequest))
	})
	require.NoError(t, err)
	require.Equal(t, "resolved/v1", response.(*pb.MemoryWriteResult).GetKind())
	require.Equal(t, 1, store.resolveCalls)
	require.Equal(t, "resolved/v1", store.putRequest.MemoryKind)
}

// --- Defect B: gRPC multi-query semantic search kind filtering ---

// grpcFakeEpisodicStore is a minimal EpisodicStore for gRPC multi-query unit tests.
// It returns configurable SearchMemoryVectors pages and identity GetMemoriesByIDs results
// keyed by kindByID.
type grpcFakeEpisodicStore struct {
	registryepisodic.EpisodicStore
	vectorPages [][]registryepisodic.MemoryVectorSearch
	callCount   int
	kindByID    map[uuid.UUID]string
}

func (f *grpcFakeEpisodicStore) SearchMemoryVectors(_ context.Context, _ string, _ []float32, _ registryepisodic.AttributeFilter, _ string, limit int, _ registryepisodic.ArchiveFilter) ([]registryepisodic.MemoryVectorSearch, error) {
	if f.callCount >= len(f.vectorPages) {
		return nil, nil
	}
	page := f.vectorPages[f.callCount]
	f.callCount++
	if len(page) > limit {
		page = page[:limit]
	}
	return page, nil
}

func (f *grpcFakeEpisodicStore) GetMemoriesByIDs(_ context.Context, ids []uuid.UUID, _ registryepisodic.ArchiveFilter) ([]registryepisodic.MemoryItem, error) {
	items := make([]registryepisodic.MemoryItem, 0, len(ids))
	for _, id := range ids {
		var kind string
		if f.kindByID != nil {
			kind = f.kindByID[id]
		}
		items = append(items, registryepisodic.MemoryItem{ID: id, MemoryKind: kind, Revision: 1})
	}
	return items, nil
}

func (f *grpcFakeEpisodicStore) InReadTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (f *grpcFakeEpisodicStore) InWriteTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (f *grpcFakeEpisodicStore) ListMemoryKindVersions(_ context.Context, _ string) ([]model.MemoryKindVersion, error) {
	return nil, nil
}
func (f *grpcFakeEpisodicStore) GetMemoryKindVersion(_ context.Context, _ string) (*model.MemoryKindVersion, error) {
	return nil, nil
}

// fakeGRPCEmbedder returns a fixed non-empty embedding.
type fakeGRPCEmbedder struct{}

func (f *fakeGRPCEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}
func (f *fakeGRPCEmbedder) Dimension() int    { return 3 }
func (f *fakeGRPCEmbedder) ModelName() string { return "fake" }

// TestGRPCMqCandidateEligibleKindAlwaysChecked verifies that grpcMqCandidateEligible
// always checks kind, regardless of PrimaryValidated.
func TestGRPCMqCandidateEligibleKindAlwaysChecked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		vectorSchema     string
		vectorRevision   int64
		primaryValidated bool
		primaryKind      string
		primaryRevision  int64
		memoryKind       string
		want             bool
	}{
		{
			name:             "validated correct kind",
			primaryValidated: true, primaryKind: "events/v1", memoryKind: "events/v1",
			want: true,
		},
		{
			name:             "validated wrong kind — must be excluded",
			primaryValidated: true, primaryKind: "other/v1", memoryKind: "events/v1",
			want: false,
		},
		{
			name:             "not validated, missing revision rejected",
			primaryValidated: false,
			vectorSchema:     "events/v1", vectorRevision: 0,
			primaryKind: "events/v1", primaryRevision: 0,
			memoryKind: "events/v1",
			want:       false,
		},
		{
			name:             "not validated, stale revision",
			primaryValidated: false,
			vectorSchema:     "events/v1", vectorRevision: 2,
			primaryKind: "events/v1", primaryRevision: 1,
			memoryKind: "events/v1",
			want:       false,
		},
		{
			name:             "not validated, corrupt missing kind rejected",
			primaryValidated: false,
			vectorSchema:     "", vectorRevision: 0,
			primaryKind: "", primaryRevision: 0,
			memoryKind: "",
			want:       false,
		},
		{
			name:             "not validated, legacy vector, migrated primary",
			primaryValidated: false,
			vectorSchema:     "", vectorRevision: 0,
			primaryKind: "events/v1", primaryRevision: 1,
			memoryKind: "",
			want:       false,
		},
		{
			name:             "empty selector matches all kinds",
			primaryValidated: true, primaryKind: "events/v1", memoryKind: "",
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grpcMqCandidateEligible(tt.vectorSchema, tt.vectorRevision, tt.primaryValidated, tt.primaryKind, tt.primaryRevision, tt.memoryKind)
			if got != tt.want {
				t.Errorf("grpcMqCandidateEligible(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGRPCMultiQuerySQLValidatedWrongKindExcluded verifies Defect B fix for gRPC:
// PrimaryValidated=true only skips stale freshness; kind must still be checked.
func TestGRPCMultiQuerySQLValidatedWrongKindExcluded(t *testing.T) {
	t.Parallel()

	wrongKindID := uuid.New()
	rightKindID := uuid.New()

	// wrongKindID: PrimaryValidated=true, but primary row has "other/v1" → excluded.
	// rightKindID: PrimaryValidated=false, primary row has "events/v1" → included.
	page := []registryepisodic.MemoryVectorSearch{
		{MemoryID: wrongKindID, Score: 0.99, MemoryKind: "events/v1", MemoryRevision: 1, PrimaryValidated: true},
		{MemoryID: rightKindID, Score: 0.80, MemoryKind: "events/v1", MemoryRevision: 1, PrimaryValidated: false},
	}
	store := &grpcFakeEpisodicStore{
		vectorPages: [][]registryepisodic.MemoryVectorSearch{page},
		kindByID: map[uuid.UUID]string{
			wrongKindID: "other/v1",
			rightKindID: "events/v1",
		},
	}

	queries := []memorySearchQuerySpec{{Text: "q", Purpose: "p"}}
	ctx := context.Background()
	results, err := multiQuerySemanticSearchMemories(ctx, store, &fakeGRPCEmbedder{}, nil,
		registryepisodic.AttributeFilter{}, queries, 5, 5,
		registryepisodic.ArchiveFilterInclude, "events/v1")
	require.NoError(t, err)

	for _, r := range results {
		if r.ID == wrongKindID {
			t.Error("PrimaryValidated=true must NOT bypass kind check: wrong-kind item should be excluded")
		}
	}
	found := false
	for _, r := range results {
		if r.ID == rightKindID {
			found = true
		}
	}
	if !found {
		t.Errorf("right-kind item should be included; got %d results", len(results))
	}
}

// --- Defect C: gRPC filter normalization parity ---

// grpcFakeKindStore provides configurable GetMemoryKindVersion / ListMemoryKindVersions
// for filter normalization tests.
type grpcFakeKindStore struct {
	registryepisodic.EpisodicStore
	versions []model.MemoryKindVersion
}

func (s *grpcFakeKindStore) InReadTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (s *grpcFakeKindStore) InWriteTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (s *grpcFakeKindStore) ListMemoryKindVersions(_ context.Context, _ string) ([]model.MemoryKindVersion, error) {
	return s.versions, nil
}
func (s *grpcFakeKindStore) GetMemoryKindVersion(_ context.Context, name string) (*model.MemoryKindVersion, error) {
	for _, v := range s.versions {
		if v.Name == name {
			cp := v
			return &cp, nil
		}
	}
	return nil, nil
}

// TestGRPCValidateAndNormalizeCallerFilterTimestamp verifies that a timestamp filter
// value is canonicalized to nanosecond UTC form (parity with REST).
func TestGRPCValidateAndNormalizeCallerFilterTimestamp(t *testing.T) {
	t.Parallel()
	store := &grpcFakeKindStore{
		versions: []model.MemoryKindVersion{
			{Name: "events/v1", AttributeTypes: map[string]string{"created_at": "timestamp"}},
		},
	}
	callerFilter := registryepisodic.AttributeFilter{
		Conditions: []registryepisodic.AttributeFilterCondition{
			{
				Field: "created_at",
				Op:    registryepisodic.AttributeFilterOpEq,
				Values: []registryepisodic.AttributeFilterValue{
					{Raw: "2024-03-15T10:30:00Z", Text: "2024-03-15T10:30:00Z"},
				},
			},
		},
	}
	ctx := context.Background()
	normalized, err := grpcValidateAndNormalizeCallerFilter(ctx, store, "events/v1", callerFilter)
	require.NoError(t, err)
	require.Len(t, normalized.Conditions, 1)
	// Value must be canonicalized (stored as time.Time-derived string, not raw input).
	rawVal := normalized.Conditions[0].Values[0].Raw
	require.NotEmpty(t, rawVal, "normalized Raw value must not be empty")
	// RangeKind must be set to time for timestamp fields.
	require.Equal(t, registryepisodic.AttributeFilterRangeTime, normalized.Conditions[0].RangeKind,
		"timestamp field must have RangeKind=time")
}

// TestGRPCValidateAndNormalizeCallerFilterNumericFloat64 verifies that a numeric filter
// value is stored as float64 and RangeKind is set to number (parity with REST).
func TestGRPCValidateAndNormalizeCallerFilterNumericFloat64(t *testing.T) {
	t.Parallel()
	store := &grpcFakeKindStore{
		versions: []model.MemoryKindVersion{
			{Name: "metrics/v1", AttributeTypes: map[string]string{"score": "number"}},
		},
	}
	callerFilter := registryepisodic.AttributeFilter{
		Conditions: []registryepisodic.AttributeFilterCondition{
			{
				Field: "score",
				Op:    registryepisodic.AttributeFilterOpGte,
				Values: []registryepisodic.AttributeFilterValue{
					{Raw: float64(42), Text: "42"},
				},
			},
		},
	}
	ctx := context.Background()
	normalized, err := grpcValidateAndNormalizeCallerFilter(ctx, store, "metrics/v1", callerFilter)
	require.NoError(t, err)
	require.Len(t, normalized.Conditions, 1)
	val, ok := normalized.Conditions[0].Values[0].Raw.(float64)
	require.True(t, ok, "numeric value must be float64 after normalization, got %T", normalized.Conditions[0].Values[0].Raw)
	require.Equal(t, float64(42), val)
	require.Equal(t, registryepisodic.AttributeFilterRangeNumber, normalized.Conditions[0].RangeKind,
		"number field must have RangeKind=number")
}

// TestGRPCValidateAndNormalizeCallerFilterWrongTypeRejected verifies that a filter
// value of the wrong type for a declared field is rejected.
func TestGRPCValidateAndNormalizeCallerFilterWrongTypeRejected(t *testing.T) {
	t.Parallel()
	store := &grpcFakeKindStore{
		versions: []model.MemoryKindVersion{
			{Name: "events/v1", AttributeTypes: map[string]string{"count": "number"}},
		},
	}
	callerFilter := registryepisodic.AttributeFilter{
		Conditions: []registryepisodic.AttributeFilterCondition{
			{
				Field: "count",
				Op:    registryepisodic.AttributeFilterOpEq,
				Values: []registryepisodic.AttributeFilterValue{
					{Raw: "not-a-number", Text: "not-a-number"},
				},
			},
		},
	}
	ctx := context.Background()
	_, err := grpcValidateAndNormalizeCallerFilter(ctx, store, "events/v1", callerFilter)
	require.Error(t, err, "filter with wrong type for number field must be rejected")
}

// TestGRPCValidateAndNormalizeCallerFilterUndeclaredFieldRejected verifies that
// a filter on a field not declared in the schema is rejected.
func TestGRPCValidateAndNormalizeCallerFilterUndeclaredFieldRejected(t *testing.T) {
	t.Parallel()
	store := &grpcFakeKindStore{
		versions: []model.MemoryKindVersion{
			{Name: "events/v1", AttributeTypes: map[string]string{"name": "string"}},
		},
	}
	callerFilter := registryepisodic.AttributeFilter{
		Conditions: []registryepisodic.AttributeFilterCondition{
			{
				Field:  "undeclared_field",
				Op:     registryepisodic.AttributeFilterOpEq,
				Values: []registryepisodic.AttributeFilterValue{{Raw: "x", Text: "x"}},
			},
		},
	}
	ctx := context.Background()
	_, err := grpcValidateAndNormalizeCallerFilter(ctx, store, "events/v1", callerFilter)
	require.Error(t, err, "filter on undeclared field must be rejected")
}

// TestGRPCValidateAndNormalizeCallerFilterSchemaInferredRangeKind verifies that
// RangeKind is derived from the schema field type (number→number, timestamp→time).
func TestGRPCValidateAndNormalizeCallerFilterSchemaInferredRangeKind(t *testing.T) {
	t.Parallel()
	store := &grpcFakeKindStore{
		versions: []model.MemoryKindVersion{
			{Name: "metrics/v1", AttributeTypes: map[string]string{
				"score":      "number",
				"created_at": "timestamp",
			}},
		},
	}
	tests := []struct {
		field     string
		op        registryepisodic.AttributeFilterOp
		rawValue  interface{}
		wantRange registryepisodic.AttributeFilterRangeKind
	}{
		{
			field:     "score",
			op:        registryepisodic.AttributeFilterOpGte,
			rawValue:  float64(10),
			wantRange: registryepisodic.AttributeFilterRangeNumber,
		},
		{
			field:     "created_at",
			op:        registryepisodic.AttributeFilterOpGte,
			rawValue:  "2024-01-01T00:00:00Z",
			wantRange: registryepisodic.AttributeFilterRangeTime,
		},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			callerFilter := registryepisodic.AttributeFilter{
				Conditions: []registryepisodic.AttributeFilterCondition{
					{
						Field:  tt.field,
						Op:     tt.op,
						Values: []registryepisodic.AttributeFilterValue{{Raw: tt.rawValue, Text: "v"}},
					},
				},
			}
			ctx := context.Background()
			normalized, err := grpcValidateAndNormalizeCallerFilter(ctx, store, "metrics/v1", callerFilter)
			require.NoError(t, err)
			require.Equal(t, tt.wantRange, normalized.Conditions[0].RangeKind,
				"RangeKind for %s must be %v", tt.field, tt.wantRange)
		})
	}
}

// TestGRPCValidateAndNormalizeCallerFilterNilStorePassThrough verifies that when
// the episodic store is nil, filters are passed through unchanged (no panic).
func TestGRPCValidateAndNormalizeCallerFilterNilStorePassThrough(t *testing.T) {
	t.Parallel()
	callerFilter := registryepisodic.AttributeFilter{
		Conditions: []registryepisodic.AttributeFilterCondition{
			{Field: "x", Op: registryepisodic.AttributeFilterOpEq, Values: []registryepisodic.AttributeFilterValue{{Raw: "v"}}},
		},
	}
	ctx := context.Background()
	out, err := grpcValidateAndNormalizeCallerFilter(ctx, nil, "events/v1", callerFilter)
	require.NoError(t, err)
	require.Equal(t, callerFilter, out, "nil store should pass filter through unchanged")
}

// TestGRPCMatchesKindSelectorParity verifies matchesGRPCKindSelector behavior
// mirrors the REST matchesKindSelector for all selector types.
func TestGRPCMatchesKindSelectorParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		schema   string
		selector string
		want     bool
	}{
		// Empty selector matches all.
		{"events/v1", "", true},
		{"default/v1", "", true},
		// Exact match.
		{"events/v1", "events/v1", true},
		{"events/v2", "events/v1", false},
		{"other/v1", "events/v1", false},
		// Family selector.
		{"events/v1", "events", true},
		{"events/v2", "events", true},
		{"eventstore/v1", "events", false}, // must not match prefix with different separator
		{"default/v1", "events", false},
		// Exact match with default kind.
		{"default/v1", "default/v1", true},
		{"default/v1", "default", true},
	}
	for _, tt := range tests {
		t.Run(tt.schema+"|"+tt.selector, func(t *testing.T) {
			got := matchesGRPCKindSelector(tt.schema, tt.selector)
			if got != tt.want {
				t.Errorf("matchesGRPCKindSelector(%q, %q) = %v, want %v", tt.schema, tt.selector, got, tt.want)
			}
		})
	}
}

// TestGRPCNormalizedRangeKindForType verifies the helper returns correct RangeKind.
func TestGRPCNormalizedRangeKindForType(t *testing.T) {
	t.Parallel()

	require.Equal(t, registryepisodic.AttributeFilterRangeNumber,
		grpcNormalizedRangeKindForType(string(episodic.AttributeTypeNumber), ""))
	require.Equal(t, registryepisodic.AttributeFilterRangeTime,
		grpcNormalizedRangeKindForType(string(episodic.AttributeTypeTimestamp), ""))
	// Unknown type returns whatever was passed (preserve caller's setting).
	existing := registryepisodic.AttributeFilterRangeKind("custom")
	require.Equal(t, existing, grpcNormalizedRangeKindForType("string", existing))
}
