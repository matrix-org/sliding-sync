package internal

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"runtime/trace"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	otrace "go.opentelemetry.io/otel/trace"
)

const tracerName = "sliding-sync"

type TraceKey string

var (
	OTLPSpan TraceKey = "otlp"
)

type Task struct {
	t *trace.Task
	o otrace.Span
}

func (s *Task) End() {
	s.t.End()
	s.o.End()
}

// combined runtime/trace and OTLP span
type RuntimeTraceOTLPSpan struct {
	region *trace.Region
	span   otrace.Span
}

func (s *RuntimeTraceOTLPSpan) End() {
	s.region.End()
	s.span.End()
}

func Logf(ctx context.Context, category, format string, args ...interface{}) {
	trace.Logf(ctx, category, format, args...)
	s := otrace.SpanFromContext(ctx)
	s.AddEvent(fmt.Sprintf(format, args...), otrace.WithAttributes(
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

func ConfigureOTLP(otlpURL, otlpUser, otlpPass, version string) error {
	ctx := context.Background()
	parsedOTLPURL, err := url.Parse(otlpURL)
	if err != nil {
		return err
	}
	isInsecure := parsedOTLPURL.Scheme == "http" // e.g testing and development
	if parsedOTLPURL.Path != "" {
		return fmt.Errorf("OTLP URL %s cannot contain any path segments", otlpURL)
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(parsedOTLPURL.Host),
	}
	if isInsecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	fmt.Println("ConfigureOTLP: host=", parsedOTLPURL.Host, "insecure=", isInsecure)
	if otlpPass != "" && otlpUser != "" {
		opts = append(opts, otlptracehttp.WithHeaders(
			map[string]string{
				"Authorization": fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", otlpUser, otlpPass)))),
			},
		))
	}
	client := otlptracehttp.NewClient(opts...)
	// Create the OTLP exporter
	exp, err := otlptrace.New(ctx, client)
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
	// setup traceparent (TraceContext) handling, and pass through any Baggage
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.Baggage{}, propagation.TraceContext{},
	))
	return nil
}
