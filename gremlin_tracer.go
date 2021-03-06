package gremlin

import (
	"context"
	"time"

	"github.com/opentracing/opentracing-go"
)

type GremlinTracer struct {
	next   Gremlin_i
	tracer opentracing.Tracer
}

func NewGremlinTracer(next Gremlin_i, tracer opentracing.Tracer) GremlinTracer {
	return GremlinTracer{
		next:   next,
		tracer: tracer,
	}
}

func StartSpanFromParent(ctx context.Context, tracer opentracing.Tracer, method string, opts ...opentracing.StartSpanOption) (opentracing.Span, context.Context) {
	parent := opentracing.SpanFromContext(ctx)
	if parent != nil {
		opts = append(opts, opentracing.ChildOf(parent.Context()))
	}
	span := tracer.StartSpan(method, opts...)
	return span, opentracing.ContextWithSpan(ctx, span)
}

func (g GremlinTracer) ExecQueryF(ctx context.Context, gremlinQuery GremlinQuery) (response string, err error) {
	method := CoalesceStrings(OpNameFromContext(ctx), "Gremlin.ExecQueryF")
	span, _ := StartSpanFromParent(ctx, g.tracer, method, opentracing.Tags{"type": "gremlin"})
	defer span.Finish()
	return g.next.ExecQueryF(ctx, gremlinQuery)
}

func (g GremlinTracer) StartMonitor(ctx context.Context, interval time.Duration) (err error) {
	method := CoalesceStrings(OpNameFromContext(ctx), "Gremlin.StartMonitor")
	span, _ := StartSpanFromParent(ctx, g.tracer, method, opentracing.Tags{"type": "gremlin"})
	defer span.Finish()
	return g.next.StartMonitor(ctx, interval)
}

func (g GremlinTracer) Close(ctx context.Context) (err error) {
	method := CoalesceStrings(OpNameFromContext(ctx), "Gremlin.Close")
	span, _ := StartSpanFromParent(ctx, g.tracer, method, opentracing.Tags{"type": "gremlin"})
	defer span.Finish()
	return g.next.Close(ctx)
}
