package tracing

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chirino/memory-service/internal/operationevent"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.38.0"
	"go.opentelemetry.io/otel/trace"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type traceParticipationKey struct{}
type tracerProviderKey struct{}

type outboundPropagatorKey struct{}

// WithProviderContext stores the given TracerProvider in ctx so plugin loaders
// can retrieve it via ProviderFromContext.
func WithProviderContext(ctx context.Context, tp trace.TracerProvider) context.Context {
	return context.WithValue(ctx, tracerProviderKey{}, tp)
}

// ProviderFromContext retrieves the TracerProvider stored by WithProviderContext.
// Returns nil if none was stored.
func ProviderFromContext(ctx context.Context) trace.TracerProvider {
	tp, _ := ctx.Value(tracerProviderKey{}).(trace.TracerProvider)
	return tp
}

// ProviderFromContextOrNoop retrieves the TracerProvider stored by WithProviderContext.
// Returns a noop TracerProvider if ctx is nil or none was stored.
func ProviderFromContextOrNoop(ctx context.Context) trace.TracerProvider {
	if ctx == nil {
		return nooptrace.NewTracerProvider()
	}
	tp := ProviderFromContext(ctx)
	if tp == nil {
		return nooptrace.NewTracerProvider()
	}
	return tp
}

// WithOutboundPropagatorContext stores the outbound propagator in ctx so plugin
// loaders can retrieve it via OutboundPropagatorFromContext.  The outbound
// propagator is the configured inbound propagator with baggage stripped, wrapped
// in ParticipatingPropagator, so it honours OTEL_PROPAGATORS on outbound calls.
func WithOutboundPropagatorContext(ctx context.Context, prop propagation.TextMapPropagator) context.Context {
	return context.WithValue(ctx, outboundPropagatorKey{}, prop)
}

// OutboundPropagatorFromContext retrieves the outbound propagator stored by
// WithOutboundPropagatorContext.  Returns a TraceContext-only participating
// propagator if none was stored, preserving the pre-fix behaviour.
func OutboundPropagatorFromContext(ctx context.Context) propagation.TextMapPropagator {
	prop, _ := ctx.Value(outboundPropagatorKey{}).(propagation.TextMapPropagator)
	if prop == nil {
		// Fallback: pre-fix behaviour — W3C TraceContext only, no baggage.
		return NewParticipatingPropagator(propagation.TraceContext{})
	}
	return prop
}

// MarkParticipating marks the context as an active participant in an upstream trace.
func MarkParticipating(ctx context.Context) context.Context {
	return context.WithValue(ctx, traceParticipationKey{}, true)
}

// IsParticipating returns true if the context is participating in an upstream trace.
func IsParticipating(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	val, ok := ctx.Value(traceParticipationKey{}).(bool)
	return ok && val
}

// noBaggagePropagator wraps a TextMapPropagator and suppresses the baggage key
// on Inject so that internal baggage is never forwarded to third-party endpoints.
// Extract is unchanged — inbound baggage is still parsed for participation gating.
type noBaggagePropagator struct {
	base propagation.TextMapPropagator
}

// WithNoBaggage wraps prop so its Inject never writes the "baggage" key.
// Use this to thread a configured trace propagator into outbound clients that
// must not forward internal baggage to third-party services (OpenAI, Qdrant, etc.).
func WithNoBaggage(prop propagation.TextMapPropagator) propagation.TextMapPropagator {
	return &noBaggagePropagator{base: prop}
}

func (p *noBaggagePropagator) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	p.base.Inject(ctx, &filteredCarrier{TextMapCarrier: carrier})
}

func (p *noBaggagePropagator) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return p.base.Extract(ctx, carrier)
}

func (p *noBaggagePropagator) Fields() []string {
	fields := p.base.Fields()
	filtered := fields[:0:len(fields)]
	for _, f := range fields {
		if strings.ToLower(f) != "baggage" {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// filteredCarrier wraps a TextMapCarrier and drops the "baggage" key on Set.
type filteredCarrier struct {
	propagation.TextMapCarrier
}

func (c *filteredCarrier) Set(key, val string) {
	if strings.ToLower(key) == "baggage" {
		return
	}
	c.TextMapCarrier.Set(key, val)
}

// ParticipatingPropagator wraps a TextMapPropagator, making Inject a no-op unless IsParticipating(ctx) is true.
type ParticipatingPropagator struct {
	base propagation.TextMapPropagator
}

// NewParticipatingPropagator creates a new ParticipatingPropagator.
func NewParticipatingPropagator(base propagation.TextMapPropagator) propagation.TextMapPropagator {
	return &ParticipatingPropagator{base: base}
}

// Inject writes the trace context if the context is participating in an upstream trace.
func (p *ParticipatingPropagator) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	if !IsParticipating(ctx) {
		return
	}
	p.base.Inject(ctx, carrier)
}

// Extract delegates directly to the underlying propagator.
func (p *ParticipatingPropagator) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return p.base.Extract(ctx, carrier)
}

// Fields returns the underlying propagator fields.
func (p *ParticipatingPropagator) Fields() []string {
	return p.base.Fields()
}

// TraceContextFromContext returns the active traceID and spanID only if the context is participating and valid.
func TraceContextFromContext(ctx context.Context) (traceID string, spanID string) {
	if !IsParticipating(ctx) {
		return "", ""
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}

// canonicalHTTPRoute converts a Gin route template (e.g. /v1/entries/:id) to the
// OTel http.route canonical form (e.g. /v1/entries/{id}).  Wildcard params
// (*param) are also converted.  This mirrors the same conversion in
// security.canonicalGinRoute without creating a cross-package dependency.
func canonicalHTTPRoute(ginPath string) string {
	parts := strings.Split(ginPath, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") && len(part) > 1 {
			parts[i] = "{" + part[1:] + "}"
		} else if strings.HasPrefix(part, "*") && len(part) > 1 {
			parts[i] = "{" + part[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

// HTTPMiddleware creates a Gin middleware for passive OpenTelemetry trace participation.
// outboundProp is stored on the request context so per-request handlers that make
// outbound calls (e.g. source-URL attachment downloads) can retrieve the configured
// propagator via OutboundPropagatorFromContext without touching the signature of those handlers.
func HTTPMiddleware(tp trace.TracerProvider, propagator propagation.TextMapPropagator, outboundProp propagation.TextMapPropagator) gin.HandlerFunc {
	tracer := tp.Tracer("memory-service/http")

	return func(c *gin.Context) {
		extractedCtx := propagator.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		spanCtx := trace.SpanContextFromContext(extractedCtx)

		// Branch 1: Absent or invalid traceparent
		if !spanCtx.IsValid() {
			c.Next()
			return
		}

		// Mark context as participating since we received a valid remote parent.
		// Also store the outbound propagator so per-request handlers that make
		// downstream calls inherit the configured format.
		participatingCtx := WithOutboundPropagatorContext(
			WithProviderContext(MarkParticipating(extractedCtx), tp),
			outboundProp,
		)

		// Branch 2: Valid but NOT sampled (traceparent flags != 01)
		if !spanCtx.IsSampled() {
			c.Request = c.Request.WithContext(participatingCtx)
			c.Next()
			return
		}

		// Branch 3: Valid and sampled (traceparent flags == 01)
		//
		// Use c.FullPath() (the matched route template, e.g. /v1/entries/:id)
		// converted to the canonical OTel form (e.g. /v1/entries/{id}).
		// The concrete request path is deliberately excluded from all attributes
		// to prevent signed tokens and user-supplied IDs from reaching exporters.
		ginRoute := c.FullPath()
		var spanName string
		var spanAttrs []attribute.KeyValue
		spanAttrs = append(spanAttrs, semconv.HTTPRequestMethodKey.String(c.Request.Method))
		if ginRoute == "" {
			// Unmatched route: omit http.route entirely; use a bounded span name.
			spanName = "HTTP " + c.Request.Method
		} else {
			canonicalRoute := canonicalHTTPRoute(ginRoute)
			spanName = c.Request.Method + " " + canonicalRoute
			spanAttrs = append(spanAttrs, semconv.HTTPRouteKey.String(canonicalRoute))
		}

		ctx, span := tracer.Start(
			participatingCtx,
			spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(spanAttrs...),
		)
		defer func() {
			status := c.Writer.Status()
			span.SetAttributes(semconv.HTTPResponseStatusCodeKey.Int(status))
			if len(c.Errors) > 0 && status >= 500 {
				// RecordError and SetStatus(Error) are skipped for 4xx for two reasons:
				// (1) OTel HTTP server semconv: 4xx is a client error, not a server error.
				// (2) Error strings can echo request-supplied input (e.g. resource IDs in
				//     NotFoundError.Error()), which must not appear in exported telemetry.
				span.RecordError(c.Errors.Last().Err)
				span.SetStatus(codes.Error, c.Errors.Last().Error())
			} else if status >= 500 {
				// OTel HTTP server semconv: 5xx → Error, 4xx → Unset (client error).
				span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", status))
			}
			if event := operationevent.FromContext(c.Request.Context()); event != nil {
				enrichSpanFromSnapshot(span, event.Snapshot())
			}
			span.End()
		}()

		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// grpcIsCallerError reports whether a gRPC status code represents a caller-side
// error: the server processed the request correctly but the caller supplied bad
// input or lacked permission.  Per OTel gRPC server semconv, these codes must
// NOT set span status to Error — the same principle that governs HTTP 4xx.
func grpcIsCallerError(c grpccodes.Code) bool {
	switch c {
	case grpccodes.InvalidArgument,
		grpccodes.NotFound,
		grpccodes.AlreadyExists,
		grpccodes.PermissionDenied,
		grpccodes.FailedPrecondition,
		grpccodes.Unauthenticated:
		return true
	}
	return false
}

// GRPCUnaryServerInterceptor returns a unary server interceptor for passive OpenTelemetry trace participation.
// outboundProp is stored on the request context so gRPC handlers that make outbound calls
// (e.g. StartSourceURLAttachmentDownload) can retrieve the configured propagator via
// OutboundPropagatorFromContext without touching those handlers' signatures.
func GRPCUnaryServerInterceptor(tp trace.TracerProvider, propagator propagation.TextMapPropagator, outboundProp propagation.TextMapPropagator) grpc.UnaryServerInterceptor {
	tracer := tp.Tracer("memory-service/grpc")

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, retErr error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			md = metadata.MD{}
		}
		extractedCtx := propagator.Extract(ctx, &metadataCarrier{md: md})
		spanCtx := trace.SpanContextFromContext(extractedCtx)

		// Branch 1: Absent or invalid traceparent
		if !spanCtx.IsValid() {
			return handler(ctx, req)
		}

		// Security owns the operation event; this reference lets the outer tracer
		// observe the event created by the downstream operation interceptor.
		ref := operationevent.NewEventRef()
		participatingCtx := WithOutboundPropagatorContext(
			WithProviderContext(
				operationevent.WithEventRef(MarkParticipating(extractedCtx), ref), tp),
			outboundProp)

		// Branch 2: Valid but NOT sampled (traceparent flags != 01)
		if !spanCtx.IsSampled() {
			return handler(participatingCtx, req)
		}

		// Branch 3: Valid and sampled (traceparent flags == 01)
		spanName := info.FullMethod
		ctx, span := tracer.Start(
			participatingCtx,
			spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.RPCSystemGRPC,
				semconv.RPCServiceKey.String(grpcServiceFromMethod(info.FullMethod)),
				semconv.RPCMethodKey.String(grpcMethodFromMethod(info.FullMethod)),
			),
		)
		defer func() {
			if retErr != nil {
				st, _ := status.FromError(retErr)
				span.SetAttributes(semconv.RPCGRPCStatusCodeKey.Int64(int64(st.Code())))
				// Caller errors (bad input, missing auth) leave span status Unset;
				// only server-side failures set it to Error.
				if !grpcIsCallerError(st.Code()) {
					span.RecordError(retErr)
					span.SetStatus(codes.Error, retErr.Error())
				}
			} else {
				span.SetAttributes(semconv.RPCGRPCStatusCodeKey.Int64(0))
			}
			if ref := operationevent.EventRefFromContext(ctx); ref != nil && ref.Get() != nil {
				enrichSpanFromSnapshot(span, ref.Get().Snapshot())
			} else if event := operationevent.FromContext(ctx); event != nil {
				enrichSpanFromSnapshot(span, event.Snapshot())
			}
			span.End()
		}()

		return handler(ctx, req)
	}
}

// GRPCStreamServerInterceptor returns a stream server interceptor for passive OpenTelemetry trace participation.
// outboundProp is stored on the stream context so gRPC stream handlers that make outbound calls
// can retrieve the configured propagator via OutboundPropagatorFromContext without touching
// those handlers' signatures.
func GRPCStreamServerInterceptor(tp trace.TracerProvider, propagator propagation.TextMapPropagator, outboundProp propagation.TextMapPropagator) grpc.StreamServerInterceptor {
	tracer := tp.Tracer("memory-service/grpc")

	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (retErr error) {
		ctx := stream.Context()
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			md = metadata.MD{}
		}
		extractedCtx := propagator.Extract(ctx, &metadataCarrier{md: md})
		spanCtx := trace.SpanContextFromContext(extractedCtx)

		// Branch 1: Absent or invalid traceparent
		if !spanCtx.IsValid() {
			return handler(srv, stream)
		}

		// Security owns the operation event; this reference lets the outer tracer
		// observe the event created by the downstream operation interceptor.
		ref := operationevent.NewEventRef()
		participatingCtx := WithOutboundPropagatorContext(
			WithProviderContext(
				operationevent.WithEventRef(MarkParticipating(extractedCtx), ref), tp),
			outboundProp)

		// Branch 2: Valid but NOT sampled
		if !spanCtx.IsSampled() {
			return handler(srv, &contextWrappedServerStream{ServerStream: stream, ctx: participatingCtx})
		}

		// Branch 3: Valid and sampled
		spanName := info.FullMethod
		ctx, span := tracer.Start(
			participatingCtx,
			spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.RPCSystemGRPC,
				semconv.RPCServiceKey.String(grpcServiceFromMethod(info.FullMethod)),
				semconv.RPCMethodKey.String(grpcMethodFromMethod(info.FullMethod)),
			),
		)
		defer func() {
			if retErr != nil {
				st, _ := status.FromError(retErr)
				span.SetAttributes(semconv.RPCGRPCStatusCodeKey.Int64(int64(st.Code())))
				// Caller errors (bad input, missing auth) leave span status Unset;
				// only server-side failures set it to Error.
				if !grpcIsCallerError(st.Code()) {
					span.RecordError(retErr)
					span.SetStatus(codes.Error, retErr.Error())
				}
			} else {
				span.SetAttributes(semconv.RPCGRPCStatusCodeKey.Int64(0))
			}
			if ref := operationevent.EventRefFromContext(ctx); ref != nil && ref.Get() != nil {
				enrichSpanFromSnapshot(span, ref.Get().Snapshot())
			} else if event := operationevent.FromContext(ctx); event != nil {
				enrichSpanFromSnapshot(span, event.Snapshot())
			}
			span.End()
		}()

		return handler(srv, &contextWrappedServerStream{ServerStream: stream, ctx: ctx})
	}
}

type contextWrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextWrappedServerStream) Context() context.Context {
	return s.ctx
}

type metadataCarrier struct {
	md metadata.MD
}

func (c *metadataCarrier) Get(key string) string {
	vals := c.md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (c *metadataCarrier) Set(key, val string) {
	c.md.Set(key, val)
}

func (c *metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.md))
	for k := range c.md {
		keys = append(keys, k)
	}
	return keys
}

func grpcServiceFromMethod(fullMethod string) string {
	if len(fullMethod) > 0 && fullMethod[0] == '/' {
		fullMethod = fullMethod[1:]
	}
	for i := 0; i < len(fullMethod); i++ {
		if fullMethod[i] == '/' {
			return fullMethod[:i]
		}
	}
	return ""
}

func grpcMethodFromMethod(fullMethod string) string {
	for i := len(fullMethod) - 1; i >= 0; i-- {
		if fullMethod[i] == '/' {
			return fullMethod[i+1:]
		}
	}
	return fullMethod
}

// enrichSpanFromSnapshot sets memoryservice.* span attributes from an OperationEvent snapshot.
// OTel-owned fields (duration, result, status, phase, traceID, spanID) are omitted.
// errorDetails entries are recorded as span events rather than flat attributes.
func enrichSpanFromSnapshot(span trace.Span, s operationevent.Snapshot) {
	set := func(key, val string) {
		if val != "" {
			span.SetAttributes(attribute.String("memoryservice."+key, val))
		}
	}
	setInt := func(key string, val int) {
		if val != 0 {
			span.SetAttributes(attribute.String("memoryservice."+key, strconv.Itoa(val)))
		}
	}
	setInt64 := func(key string, val int64) {
		if val != 0 {
			span.SetAttributes(attribute.String("memoryservice."+key, strconv.FormatInt(val, 10)))
		}
	}
	set("requestID", s.RequestID)
	set("userID", s.UserID)
	set("clientID", s.ClientID)
	set("agentID", s.AgentID)
	set("conversationID", s.ConversationID)
	set("entryID", s.EntryID)
	set("attachmentID", s.AttachmentID)
	set("memoryID", s.MemoryID)
	set("taskID", s.TaskID)
	set("connectionID", s.ConnectionID)
	set("cursor", s.Cursor)
	set("reason", s.Reason)
	set("errorCode", s.ErrorCode)
	set("errorType", s.ErrorType)
	set("rateLimiter", s.RateLimiter)
	set("providerName", s.ProviderName)
	setInt("providerStatusCode", s.ProviderStatusCode)
	set("providerErrorCode", s.ProviderErrorCode)
	set("providerTransactionID", s.ProviderTransactionID)
	setInt("retryAttempt", s.RetryAttempt)
	setInt64("workCount", s.WorkCount)
	setInt64("failureCount", s.FailureCount)

	// errorDetails are recorded as structured span events rather than flat key=value attributes.
	for i, d := range s.ErrorDetails {
		evAttrs := []attribute.KeyValue{
			attribute.String("index", strconv.Itoa(i)),
			attribute.String("errorType", d.ErrorType),
			attribute.String("errorCode", d.ErrorCode),
			attribute.String("reason", d.Reason),
		}
		if d.Provider != nil {
			evAttrs = append(evAttrs,
				attribute.String("provider.name", d.Provider.Name),
				attribute.Int("provider.statusCode", d.Provider.StatusCode),
				attribute.String("provider.transactionID", d.Provider.TransactionID),
			)
		}
		span.AddEvent("memoryservice.errorDetail", trace.WithAttributes(evAttrs...))
	}
}
