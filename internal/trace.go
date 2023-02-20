package internal

import (
	"context"
	"fmt"
	"runtime/trace"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
