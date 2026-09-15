package tracing_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chirino/memory-service/internal/operationevent"
	"github.com/chirino/memory-service/internal/tracing"
	"github.com/chirino/memory-service/internal/tracing/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// Helper dummy handler simulating a route execution making a downstream HTTP call.
func makeTestHandler(downstreamURL string, harness *testutil.Harness) http.HandlerFunc {
	client := &http.Client{
		Transport: otelhttp.NewTransport(
			http.DefaultTransport,
			otelhttp.WithTracerProvider(harness.DecoyProvider),
			otelhttp.WithPropagators(harness.Propagator),
		),
	}
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, downstreamURL, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(http.StatusOK)
	}
}

// Helper to find server span by name or kind
func findServerSpan(spans []tracetest.SpanStub) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].SpanKind == trace.SpanKindServer {
			return &spans[i]
		}
	}
	return nil
}

func TestHTTPInboundAbsentTraceparent(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	downstream := testutil.NewDownstreamRecorder()
	t.Cleanup(downstream.Close)

	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
	)
	router.GET("/v1/entries", func(c *gin.Context) {
		makeTestHandler(downstream.Server.URL, harness)(c.Writer, c.Request)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/entries", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	// Positive assertion: verify downstream was reached exactly once
	require.Equal(t, 1, downstream.CallCount(), "Expected downstream handler to be executed")

	// Negative assertions on executed path
	spans := harness.Exporter.GetSpans()
	require.Empty(t, spans, "Expected no spans to be exported for untraced request")

	lastHeader := downstream.LastHeader()
	require.NotNil(t, lastHeader)
	require.Empty(t, lastHeader.Get("Traceparent"), "Expected no outbound traceparent header")
}

func TestHTTPMiddlewarePropagatesTracerProviderToRequestContext(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	var requestProvider trace.TracerProvider
	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
	)
	router.GET("/attachments", func(c *gin.Context) {
		requestProvider = tracing.ProviderFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/attachments", nil)
	req.Header.Set("Traceparent", testutil.NewSampledTraceparent(
		"4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"))
	router.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, harness.Provider, requestProvider,
		"request-triggered attachment jobs must receive the server tracer provider")
}

func TestHTTPInboundInvalidGarbageTraceparent(t *testing.T) {
	testCases := []struct {
		name        string
		traceparent string
	}{
		{name: "truncated", traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736"},
		{name: "over-long", traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra-junk-data-beyond-spec-limit"},
		{name: "non-hex", traceparent: "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01"},
		{name: "bad-version-ff", traceparent: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "all-zero-traceid", traceparent: "00-00000000000000000000000000000000-00f067aa0ba902b7-01"},
		{name: "all-zero-spanid", traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			harness := testutil.NewTestHarness()
			t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

			downstream := testutil.NewDownstreamRecorder()
			t.Cleanup(downstream.Close)

			router := testutil.NewGinTestRouter(
				tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
			)
			router.GET("/v1/entries", func(c *gin.Context) {
				makeTestHandler(downstream.Server.URL, harness)(c.Writer, c.Request)
			})

			req := httptest.NewRequest(http.MethodGet, "/v1/entries", nil)
			req.Header.Set("Traceparent", tc.traceparent)
			rec := httptest.NewRecorder()

			require.NotPanics(t, func() {
				router.ServeHTTP(rec, req)
			})

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 1, downstream.CallCount(), "Expected downstream handler to be executed")

			spans := harness.Exporter.GetSpans()
			require.Empty(t, spans, "Expected no spans for malformed traceparent: "+tc.traceparent)

			lastHeader := downstream.LastHeader()
			require.NotNil(t, lastHeader)
			require.Empty(t, lastHeader.Get("Traceparent"), "Expected no outbound traceparent for malformed input")
		})
	}
}

func TestHTTPInboundValidUnsampledParent(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	downstream := testutil.NewDownstreamRecorder()
	t.Cleanup(downstream.Close)

	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
	)
	router.GET("/v1/entries", func(c *gin.Context) {
		makeTestHandler(downstream.Server.URL, harness)(c.Writer, c.Request)
	})

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inboundTraceparent := testutil.NewUnsampledTraceparent(traceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/v1/entries", nil)
	req.Header.Set("Traceparent", inboundTraceparent)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, downstream.CallCount(), "Expected downstream handler to be executed")

	// 1. Assert no spans exported locally
	spans := harness.Exporter.GetSpans()
	require.Empty(t, spans, "Expected zero spans exported for unsampled parent")

	// 2. Assert outbound traceparent IS injected with matching traceID and sampled=0
	lastHeader := downstream.LastHeader()
	require.NotNil(t, lastHeader)
	outboundTraceparent := lastHeader.Get("Traceparent")
	require.NotEmpty(t, outboundTraceparent, "Expected outbound traceparent to be propagated for unsampled trace")

	require.Contains(t, outboundTraceparent, traceID, "Outbound traceparent must carry caller's traceID")
	require.True(t, outboundTraceparent[len(outboundTraceparent)-2:] == "00", "Outbound traceparent flags must end in 00 (unsampled)")
}

func TestHTTPInboundValidSampledParent(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	downstream := testutil.NewDownstreamRecorder()
	t.Cleanup(downstream.Close)

	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
	)
	router.GET("/v1/entries", func(c *gin.Context) {
		makeTestHandler(downstream.Server.URL, harness)(c.Writer, c.Request)
	})

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inboundTraceparent := testutil.NewSampledTraceparent(traceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/v1/entries", nil)
	req.Header.Set("Traceparent", inboundTraceparent)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// Assert server span exported
	spans := harness.Exporter.GetSpans()
	serverSpan := findServerSpan(spans)
	require.NotNil(t, serverSpan, "Expected server span to be exported")

	require.Equal(t, traceID, serverSpan.SpanContext.TraceID().String())
	require.Equal(t, parentSpanID, serverSpan.Parent.SpanID().String())
	require.NotEqual(t, parentSpanID, serverSpan.SpanContext.SpanID().String(), "Server span ID must be newly generated")

	// Assert outbound traceparent propagated with matching trace ID
	lastHeader := downstream.LastHeader()
	require.NotNil(t, lastHeader)
	outboundTraceparent := lastHeader.Get("Traceparent")
	require.NotEmpty(t, outboundTraceparent)
	require.Contains(t, outboundTraceparent, traceID)

	// The client transport uses harness.DecoyProvider, so the client child span is
	// exported to harness.DecoyExporter, not harness.Exporter.  Look for it there.
	decoySpans := harness.DecoyExporter.GetSpans()
	var clientSpan *tracetest.SpanStub
	for i := range decoySpans {
		if decoySpans[i].SpanKind == trace.SpanKindClient {
			clientSpan = &decoySpans[i]
			break
		}
	}
	require.NotNil(t, clientSpan, "client span must be exported by the decoy provider")
	require.Equal(t, serverSpan.SpanContext.SpanID().String(), clientSpan.Parent.SpanID().String(), "Client span's parent must be the server span")
	require.Contains(t, outboundTraceparent, clientSpan.SpanContext.SpanID().String(), "Outbound traceparent carries client span ID")
	require.True(t, outboundTraceparent[len(outboundTraceparent)-2:] == "01", "Outbound flags must end in 01 (sampled)")
}

func TestHTTPInboundOperationEventTraceContext(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	var capturedSnapshot testutil.OperationSnapshotWrapper
	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
		testutil.OperationEventCaptureMiddleware(&capturedSnapshot),
	)
	router.GET("/v1/entries", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inboundTraceparent := testutil.NewSampledTraceparent(traceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/v1/entries", nil)
	req.Header.Set("Traceparent", inboundTraceparent)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, 1, capturedSnapshot.Count(), "Expected OperationEvent to emit exactly 1 terminal snapshot")
	spans := harness.Exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]

	snap := capturedSnapshot.GetSnapshot()
	require.Equal(t, traceID, snap.TraceID)
	require.Equal(t, span.SpanContext.SpanID().String(), snap.SpanID)
}

func TestHTTPInboundOperationEventUntracedOmitsTraceContext(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	var capturedSnapshot testutil.OperationSnapshotWrapper
	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
		testutil.OperationEventCaptureMiddleware(&capturedSnapshot),
	)
	router.GET("/v1/entries", func(c *gin.Context) {
		// Start an internal span to verify TraceContextFromContext does not extract SDK-minted non-participating trace ID
		tr := harness.Provider.Tracer("internal")
		ctx, span := tr.Start(c.Request.Context(), "internal.subtask")
		defer span.End()
		c.Request = c.Request.WithContext(ctx)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/entries", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, capturedSnapshot.Count(), "Expected OperationEvent to emit exactly 1 terminal snapshot")

	snap := capturedSnapshot.GetSnapshot()
	require.Empty(t, snap.TraceID, "Untraced request must have empty traceID in OperationEvent")
	require.Empty(t, snap.SpanID, "Untraced request must have empty spanID in OperationEvent")
}

func TestHTTPInboundHandlerError(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
	)
	router.GET("/v1/entries", func(c *gin.Context) {
		_ = c.Error(net.ErrClosed)
		c.Status(http.StatusInternalServerError)
	})

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inboundTraceparent := testutil.NewSampledTraceparent(traceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/v1/entries", nil)
	req.Header.Set("Traceparent", inboundTraceparent)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	spans := harness.Exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]

	require.Equal(t, codes.Error, span.Status.Code)
	require.NotEmpty(t, span.Events, "Expected span event (RecordError)")
}

// TestHTTPInboundServerErrorWithCError verifies that a 5xx response that also sets c.Error
// DOES mark the span as Error and DOES record an error event.  This is the companion to
// TestHTTPInboundClientErrorWithCError: the two together pin both sides of the
// `if len(c.Errors) > 0 && status >= 500` condition so neither side can be inverted
// without a test going red.
func TestHTTPInboundServerErrorWithCError(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
	)
	router.GET("/v1/entries/:id", func(c *gin.Context) {
		_ = c.Error(net.ErrClosed)
		c.Status(http.StatusInternalServerError)
	})

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inboundTraceparent := testutil.NewSampledTraceparent(traceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/v1/entries/some-id", nil)
	req.Header.Set("Traceparent", inboundTraceparent)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	// Positive assertion: code path executed — span must be exported.
	spans := harness.Exporter.GetSpans()
	require.Len(t, spans, 1, "Expected server span to be exported for sampled 5xx request")
	span := spans[0]

	// 5xx with c.Error must set span status to Error.
	require.Equal(t, codes.Error, span.Status.Code,
		"5xx with c.Error must set span status to Error per OTel HTTP server semconv")
	// RecordError must have been called — the error event must be present.
	require.NotEmpty(t, span.Events,
		"5xx with c.Error must record an error event on the span")
}

// TestHTTPInboundClientErrorWithCError verifies that a 4xx response that also sets c.Error
// does NOT mark the span as Error and does NOT record an error event.  Per OTel HTTP server
// semconv, 4xx is a client error; span status must remain Unset.  This matters because every
// handleError call site calls c.Error before writing the status, so NotFoundError,
// ValidationError, ForbiddenError and BadRequestError would otherwise pollute the span.
func TestHTTPInboundClientErrorWithCError(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
	)
	router.GET("/v1/entries/:id", func(c *gin.Context) {
		_ = c.Error(net.ErrClosed)
		c.Status(http.StatusNotFound)
	})

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inboundTraceparent := testutil.NewSampledTraceparent(traceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/v1/entries/some-resource-id", nil)
	req.Header.Set("Traceparent", inboundTraceparent)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)

	// Positive assertion: code path executed — span must be exported.
	spans := harness.Exporter.GetSpans()
	require.Len(t, spans, 1, "Expected server span to be exported for sampled 4xx request")
	span := spans[0]

	// 4xx with c.Error must leave span status Unset — client errors do not indicate server failure.
	require.Equal(t, codes.Unset, span.Status.Code,
		"4xx with c.Error must leave span status Unset per OTel HTTP server semconv")
	// RecordError must not be called — error strings can echo request-supplied input (e.g. resource IDs).
	require.Empty(t, span.Events,
		"4xx with c.Error must not record an error event on the span")
}

func TestHTTPInboundAuthRateLimitRejection(t *testing.T) {
	// OTel HTTP server semconv: 4xx is a client error — span status must be
	// left Unset.  Only 5xx sets the span status to Error.
	testStatuses := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests}

	for _, status := range testStatuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			harness := testutil.NewTestHarness()
			t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

			router := testutil.NewGinTestRouter(
				tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
			)
			router.GET("/v1/entries", func(c *gin.Context) {
				c.Status(status)
			})

			traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
			parentSpanID := "00f067aa0ba902b7"
			inboundTraceparent := testutil.NewSampledTraceparent(traceID, parentSpanID)

			req := httptest.NewRequest(http.MethodGet, "/v1/entries", nil)
			req.Header.Set("Traceparent", inboundTraceparent)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)
			require.Equal(t, status, rec.Code)

			spans := harness.Exporter.GetSpans()
			require.Len(t, spans, 1)
			span := spans[0]

			require.Equal(t, codes.Unset, span.Status.Code,
				"4xx responses are client errors; span status must be Unset per OTel HTTP server semconv")
		})
	}
}

func TestHTTPInboundUnmatchedRouteSpanName(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
	)
	router.GET("/v1/entries", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inboundTraceparent := testutil.NewSampledTraceparent(traceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/v1/nonexistent-route-path", nil)
	req.Header.Set("Traceparent", inboundTraceparent)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)

	spans := harness.Exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]

	require.Equal(t, "HTTP GET", span.Name, "Unmatched route span name must be bounded (HTTP <METHOD>)")
	// 404 is a client error; span status must be Unset per OTel HTTP server semconv.
	require.Equal(t, codes.Unset, span.Status.Code)

	// http.route must be absent for unmatched routes — there is no template to report.
	// Falling back to the concrete path would expose user-supplied values.
	attrs := make(map[string]any)
	for _, kv := range span.Attributes {
		attrs[string(kv.Key)] = kv.Value.AsInterface()
	}
	_, hasRoute := attrs["http.route"]
	require.False(t, hasRoute, "http.route must not be set when the route is unmatched")
	_, hasURLPath := attrs["url.path"]
	require.False(t, hasURLPath, "url.path must not be set on any span")
}

func TestHTTPInboundSemconvAttributes(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
	)
	// Route with a path parameter: the concrete value must NOT appear in any attribute.
	// The signed-token case (/v1/attachments/download/:token/:filename) is the
	// motivating example; the principle is tested here with a simpler route.
	router.GET("/v1/entries/:id", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inboundTraceparent := testutil.NewSampledTraceparent(traceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/v1/entries/123", nil)
	req.Header.Set("Traceparent", inboundTraceparent)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	spans := harness.Exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]

	// Span name uses the canonical route, not the concrete path.
	require.Equal(t, "GET /v1/entries/{id}", span.Name)

	attrs := make(map[string]any)
	for _, kv := range span.Attributes {
		attrs[string(kv.Key)] = kv.Value.AsInterface()
	}

	require.Equal(t, "GET", attrs["http.request.method"])
	// http.route must carry the parameterised template, not the concrete value.
	require.Equal(t, "/v1/entries/{id}", attrs["http.route"],
		"http.route must be the parameterised route template, never the concrete path")
	require.Equal(t, int64(http.StatusOK), attrs["http.response.status_code"])
	// url.path must be absent — it would expose signed tokens and user-supplied IDs.
	_, hasURLPath := attrs["url.path"]
	require.False(t, hasURLPath, "url.path must not be set on matched routes")
}

func TestHTTPInboundSpanHasMemoryServiceAttributes(t *testing.T) {
	harness := testutil.NewTestHarness()
	t.Cleanup(func() { _ = harness.Shutdown(context.Background()) })

	var capturedSnapshot testutil.OperationSnapshotWrapper

	router := testutil.NewGinTestRouter(
		tracing.HTTPMiddleware(harness.Provider, harness.Propagator, harness.Propagator),
		// Simulate security.OperationEventMiddleware: create an event, populate it, emit on the way out.
		testutil.OperationEventCaptureMiddleware(&capturedSnapshot),
	)
	router.GET("/v1/entries/:id", func(c *gin.Context) {
		event := operationevent.FromContext(c.Request.Context())
		if event != nil {
			event.SetUserID("user-abc")
			event.SetConversationID("conv-xyz")
			event.SetEntryID("entry-123")
		}
		c.Status(http.StatusOK)
	})

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	inboundTraceparent := testutil.NewSampledTraceparent(traceID, parentSpanID)

	req := httptest.NewRequest(http.MethodGet, "/v1/entries/123", nil)
	req.Header.Set("Traceparent", inboundTraceparent)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	// Positive assertion: server span was recorded.
	spans := harness.Exporter.GetSpans()
	serverSpan := findServerSpan(spans)
	require.NotNil(t, serverSpan)

	// OperationEvent was emitted once.
	require.Equal(t, 1, capturedSnapshot.Count())

	// The server span must carry memoryservice.* attributes populated from the OperationEvent.
	attrs := make(map[string]any)
	for _, kv := range serverSpan.Attributes {
		attrs[string(kv.Key)] = kv.Value.AsInterface()
	}

	require.Equal(t, "user-abc", attrs["memoryservice.userID"],
		"span must carry memoryservice.userID from OperationEvent")
	require.Equal(t, "conv-xyz", attrs["memoryservice.conversationID"],
		"span must carry memoryservice.conversationID from OperationEvent")
	require.Equal(t, "entry-123", attrs["memoryservice.entryID"],
		"span must carry memoryservice.entryID from OperationEvent")
}
