package observe_test

import (
	"context"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/observe"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type UsersController struct{}

func (c *UsersController) Routes(r dygo.Router) {
	r.Get("/users/{id}", dygo.Handle(c.Get))
}

func (c *UsersController) Get(ctx context.Context, in struct {
	ID string `path:"id"`
},
) (string, error) {
	if in.ID == "boom" {
		return "", errs.Internal("broken")
	}
	_, span := observe.Start(ctx, "UserService.Find")
	span.End()
	return trace.SpanContextFromContext(ctx).TraceID().String(), nil
}

func TestTracing(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	srv := dygotest.New(&dygo.Module{
		Name:        "app",
		Imports:     []*dygo.Module{observe.Module(observe.Options{ServiceName: "test", SpanExporter: spans, MetricReader: reader})},
		Controllers: []dygo.Provider{dygo.Value(&UsersController{})},
	}).CompileHTTP(t)

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	got := srv.GET("/users/42").Header("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01").Do(t).Status(t, 200).Text()
	if got != traceID {
		t.Fatalf("handler saw trace %s", got)
	}
	srv.GET("/users/boom").Do(t).Status(t, 500)
	tel := dygo.MustGet[*observe.Telemetry](srv.App)
	if err := tel.TracerProvider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	names := map[string]bool{}
	for _, s := range spans.GetSpans() {
		names[s.Name] = true
		if s.Name == "observe_test.UsersController.Get" && s.SpanKind != trace.SpanKindServer {
			t.Errorf("kind %v", s.SpanKind)
		}
	}
	if !names["observe_test.UsersController.Get"] || !names["UserService.Find"] || len(spans.GetSpans()) != 3 {
		t.Fatalf("spans %v", names)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	h := rm.ScopeMetrics[0].Metrics[0]
	if h.Name != "http.server.request.duration" || len(h.Data.(metricdata.Histogram[float64]).DataPoints) != 2 {
		t.Fatalf("metrics %+v", h)
	}
}
