// Package observe adds OpenTelemetry tracing and metrics. Import the module
// and every route gets a server span named after its controller and handler
// ("users.Controller.Get") plus a request duration histogram:
//
//	Imports: []*dygo.Module{observe.Module(observe.Options{ServiceName: "users-api"})}
//
// Spans and metrics are exported over OTLP/HTTP when OTEL_EXPORTER_OTLP_ENDPOINT
// (or Options.Endpoint) is set, for example http://otel-collector:4318. The
// standard OTEL_* variables are honoured. Incoming W3C traceparent headers are
// continued. Providers are flushed in OnApplicationShutdown.
//
// It is a separate Go module so that the core stays free of dependencies.
package observe

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const scope = "github.com/dygoframework/dygo/observe"

// Options configure Module.
type Options struct {
	// ServiceName defaults to OTEL_SERVICE_NAME, else "dygo-app".
	ServiceName string
	// Endpoint is the OTLP/HTTP collector URL. Defaults to
	// OTEL_EXPORTER_OTLP_ENDPOINT; with neither, nothing is exported (spans
	// still propagate to downstream calls).
	Endpoint string
	// SpanExporter and MetricReader replace the OTLP exporters, for tests or
	// other backends.
	SpanExporter sdktrace.SpanExporter
	MetricReader sdkmetric.Reader
}

// Telemetry holds the providers. Inject *observe.Telemetry to create your own
// tracers and meters.
type Telemetry struct {
	Tracer         trace.Tracer
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	duration       metric.Float64Histogram
}

// New builds the providers and installs them as the OpenTelemetry globals.
func New(ctx context.Context, o Options) (*Telemetry, error) {
	if o.ServiceName == "" {
		o.ServiceName = os.Getenv("OTEL_SERVICE_NAME")
	}
	if o.ServiceName == "" {
		o.ServiceName = "dygo-app"
	}
	if o.Endpoint == "" {
		o.Endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName(o.ServiceName)))
	if err != nil {
		return nil, err
	}
	topts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	mopts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if o.SpanExporter == nil && o.Endpoint != "" {
		if o.SpanExporter, err = otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(o.Endpoint+"/v1/traces")); err != nil {
			return nil, err
		}
	}
	if o.MetricReader == nil && o.Endpoint != "" {
		exp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(o.Endpoint+"/v1/metrics"))
		if err != nil {
			return nil, err
		}
		o.MetricReader = sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(15*time.Second))
	}
	if o.SpanExporter != nil {
		topts = append(topts, sdktrace.WithBatcher(o.SpanExporter))
	}
	if o.MetricReader != nil {
		mopts = append(mopts, sdkmetric.WithReader(o.MetricReader))
	}
	t := &Telemetry{TracerProvider: sdktrace.NewTracerProvider(topts...), MeterProvider: sdkmetric.NewMeterProvider(mopts...)}
	otel.SetTracerProvider(t.TracerProvider)
	otel.SetMeterProvider(t.MeterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Tracer = t.TracerProvider.Tracer(scope)
	t.duration, err = t.MeterProvider.Meter(scope).Float64Histogram("http.server.request.duration",
		metric.WithUnit("s"), metric.WithDescription("Duration of HTTP server requests."))
	if err != nil {
		return nil, err
	}
	return t, nil
}

// OnApplicationShutdown flushes and stops the providers.
func (t *Telemetry) OnApplicationShutdown(ctx context.Context, _ string) error {
	return errors.Join(t.TracerProvider.Shutdown(ctx), t.MeterProvider.Shutdown(ctx))
}

// Module returns a global module providing *Telemetry and its interceptor in
// the dygo.AppInterceptors group, so every route is traced.
func Module(o Options) *dygo.Module {
	return &dygo.Module{
		Name:   "observe",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func(ctx context.Context) (*Telemetry, error) { return New(ctx, o) }),
			dygo.Provide(func(t *Telemetry) dygo.Interceptor { return t.Interceptor() }, dygo.Group(dygo.AppInterceptors)),
		},
		Exports: []dygo.Token{dygo.TypeOf[*Telemetry]()},
	}
}

// SpanName is the span name for a route: the controller type without its
// pointer star, a dot, and the handler method, like "users.Controller.Get".
func SpanName(ri dygo.RouteInfo) string {
	h := ri.Handler
	if i := strings.LastIndex(h, "."); i >= 0 {
		h = h[i+1:]
	}
	return strings.TrimPrefix(ri.Controller, "*") + "." + h
}

// Interceptor traces and measures each request. It continues a trace from the
// request's traceparent header and puts the span in the handler's context.
func (t *Telemetry) Interceptor() dygo.Interceptor {
	prop := otel.GetTextMapPropagator()
	return dygo.InterceptorFunc(func(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
		start := time.Now()
		r := ec.HTTP().Request()
		ri := ec.Route()
		ctx := prop.Extract(ec.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := t.Tracer.Start(ctx, SpanName(ri), trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.HTTPRoute(ri.Path),
				semconv.URLPath(r.URL.Path),
				attribute.String("dygo.controller", ri.Controller),
				attribute.String("dygo.module", ri.Module),
			))
		defer span.End()
		ec.SetContext(ctx)
		out, err := next.Handle()

		status := dygo.ResponseStatus(ctx)
		if status == 0 {
			status = ri.Status
		}
		if status == 0 {
			status = 200
			if r.Method == "POST" {
				status = 201
			}
		}
		if err != nil {
			status = errs.ToBody(err).StatusCode
			if status >= 500 {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
		}
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
		t.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
			semconv.HTTPRequestMethodKey.String(r.Method), semconv.HTTPRoute(ri.Path), semconv.HTTPResponseStatusCode(status)))
		return out, err
	})
}

// Start starts a span for your own code, such as a service method, using the
// global tracer:
//
//	ctx, span := observe.Start(ctx, "UserService.Create")
//	defer span.End()
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return otel.Tracer(scope).Start(ctx, name, opts...)
}
