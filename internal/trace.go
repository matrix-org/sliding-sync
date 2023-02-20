package internal

import (
	"context"
	"fmt"
	"runtime/trace"

	"github.com/go-logr/zerologr" // required for Jaeger errors during transmission to use zerolog
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	otlptrace "go.opentelemetry.io/otel/trace"
)

const tracerName = "sliding-sync"

type TraceKey string

var (
	OTLPSpan TraceKey = "otlp"
)

type Task struct {
	t *trace.Task
	o otlptrace.Span
}

func (s *Task) End() {
	s.t.End()
	s.o.End()
}

// combined runtime/trace and OTLP span
type RuntimeTraceOTLPSpan struct {
	region *trace.Region
	span   otlptrace.Span
}

func (s *RuntimeTraceOTLPSpan) End() {
	s.region.End()
	s.span.End()
}

func Logf(ctx context.Context, category, format string, args ...interface{}) {
	trace.Logf(ctx, category, format, args...)
	s := otlptrace.SpanFromContext(ctx)
	s.AddEvent(fmt.Sprintf(format, args...), otlptrace.WithAttributes(
		attribute.String("category", category),
	))
}

func StartSpan(ctx context.Context, name string) (newCtx context.Context, span *RuntimeTraceOTLPSpan) {
	region := trace.StartRegion(ctx, name)
	newCtx, ospan := otel.Tracer(tracerName).Start(ctx, name)
	// use the same api as NewTask to allow context nesting
	return newCtx, &RuntimeTraceOTLPSpan{
		region: region,
		span:   ospan,
	}
}

func StartTask(ctx context.Context, name string) (context.Context, *Task) {
	ctx, task := trace.NewTask(ctx, name)
	newCtx, ospan := otel.Tracer(tracerName).Start(ctx, name)
	return newCtx, &Task{
		t: task,
		o: ospan,
	}
}

func ConfigureJaeger(jaegerURL, version string) error {
	_ = zerologr.New(&logger) // TODO
	// Create the Jaeger exporter
	exp, err := jaeger.New(jaeger.WithCollectorEndpoint(
		jaeger.WithEndpoint(jaegerURL),
	))
	if err != nil {
		return err
	}
	tp := tracesdk.NewTracerProvider(
		// Always be sure to batch in production.
		tracesdk.WithBatcher(exp),
		// Record information about this application in a Resource.
		tracesdk.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("sliding-sync"),
			attribute.String("version", version),
		)),
	)
	otel.SetTracerProvider(tp)
	return nil
}
