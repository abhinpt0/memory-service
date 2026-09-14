package infinispan

import (
	"context"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/tracing"
	"github.com/chirino/memory-service/internal/tracing/testutil"
	"github.com/stretchr/testify/require"
	b3prop "go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel/propagation"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
)

// TestInfinispanUntracedRequestNoTraceparent verifies that an untraced context
// (no MarkParticipating) does not inject a traceparent header into outbound
// Infinispan HTTP requests.
//
// The client is obtained from newInfinispanClient — the production constructor —
// so removing otelhttp.NewTransport from that constructor causes this test to fail.
func TestInfinispanUntracedRequestNoTraceparent(t *testing.T) {
	downstream := testutil.NewDownstreamRecorder()
	t.Cleanup(downstream.Close)

	cfg := &config.Config{InfinispanVectorURL: downstream.Server.URL}
	client, err := newInfinispanClient(cfg, nooptrace.NewTracerProvider(), propagation.TraceContext{})
	require.NoError(t, err)

	_, _ = client.Search(context.Background(), "cache", "from VectorItem1536 o where o.dummy = 1", 1)

	require.Equal(t, 1, downstream.CallCount(), "downstream must be called exactly once")
	require.Empty(t, downstream.LastHeader().Get("Traceparent"),
		"untraced request must not inject traceparent")
}

// TestInfinispanTracedRequestInjectsTraceparent verifies that a participating
// context causes the outbound Infinispan HTTP request to carry a traceparent
// header containing the caller's trace ID.
//
// The client is obtained from newInfinispanClient — the production constructor —
// so removing otelhttp.NewTransport from that constructor causes this test to fail.
func TestInfinispanTracedRequestInjectsTraceparent(t *testing.T) {
	downstream := testutil.NewDownstreamRecorder()
	t.Cleanup(downstream.Close)

	h := testutil.NewTestHarness()
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	cfg := &config.Config{InfinispanVectorURL: downstream.Server.URL}
	client, err := newInfinispanClient(cfg, h.Provider, h.Propagator)
	require.NoError(t, err)

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inbound := testutil.NewSampledTraceparent(traceID, parentSpanID)

	extractedCtx := h.Propagator.Extract(context.Background(),
		staticStringCarrier{"traceparent": inbound})
	ctx := tracing.MarkParticipating(extractedCtx)

	_, _ = client.Search(ctx, "cache", "from VectorItem1536 o where o.dummy = 1", 1)

	require.Equal(t, 1, downstream.CallCount(), "downstream must be called exactly once")

	outbound := downstream.LastHeader().Get("Traceparent")
	require.NotEmpty(t, outbound, "traced request must inject outbound traceparent")
	require.Contains(t, outbound, traceID, "outbound traceparent must carry the caller's trace ID")
}

// staticStringCarrier is a read-only TextMapCarrier for synthetic header injection in tests.
type staticStringCarrier map[string]string

func (c staticStringCarrier) Get(key string) string { return c[key] }
func (c staticStringCarrier) Set(_, _ string)       {}
func (c staticStringCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// TestInfinispanConfiguredPropagatorUsedOutbound verifies that newInfinispanClient
// wires the configured outbound propagator into the HTTP transport.
// When the propagator is B3 multi-header, outbound requests must carry
// X-B3-TraceId and must not carry a W3C traceparent.
//
// Mutation proof target: reverting otelhttp.WithPropagators(prop) in
// newInfinispanClient to a hardcoded propagation.TraceContext{} causes this
// test to fail because X-B3-TraceId is absent from the outbound request.
func TestInfinispanConfiguredPropagatorUsedOutbound(t *testing.T) {
	downstream := testutil.NewDownstreamRecorder()
	t.Cleanup(downstream.Close)

	h := testutil.NewTestHarness()
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })

	cfg := &config.Config{InfinispanVectorURL: downstream.Server.URL}

	// Build a B3 multi-header outbound propagator the same way buildOutboundPropagator
	// does when OTEL_PROPAGATORS=b3multi is set.
	b3Propagator := tracing.NewParticipatingPropagator(
		tracing.WithNoBaggage(b3prop.New(b3prop.WithInjectEncoding(b3prop.B3MultipleHeader))),
	)

	client, err := newInfinispanClient(cfg, h.Provider, b3Propagator)
	require.NoError(t, err)

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"

	// Extract using B3 multi-header (inbound side also uses B3).
	b3InboundProp := b3prop.New(b3prop.WithInjectEncoding(b3prop.B3MultipleHeader))
	extractedCtx := b3InboundProp.Extract(context.Background(),
		staticStringCarrier{"x-b3-traceid": traceID, "x-b3-spanid": parentSpanID, "x-b3-sampled": "1"})
	ctx := tracing.MarkParticipating(extractedCtx)

	_, _ = client.Search(ctx, "cache", "from VectorItem1536 o where o.dummy = 1", 1)

	require.Equal(t, 1, downstream.CallCount(), "downstream must be called exactly once")

	lastHeader := downstream.LastHeader()
	require.NotNil(t, lastHeader)
	require.NotEmpty(t, lastHeader.Get("X-B3-TraceId"),
		"B3 propagator must inject X-B3-TraceId on outbound request")
	require.Empty(t, lastHeader.Get("Traceparent"),
		"W3C traceparent must not be injected when B3 propagator is configured")
}
