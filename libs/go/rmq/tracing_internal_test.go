package rmq

import (
	"context"
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// installTestTracing swaps in an in-memory tracer provider and the W3C
// propagator, restoring the previous globals when the test ends.
func installTestTracing(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return exp
}

func findSpan(t *testing.T, exp *tracetest.InMemoryExporter, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range exp.GetSpans() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("span %q not recorded; got %d spans", name, len(exp.GetSpans()))
	return tracetest.SpanStub{}
}

func attrValue(attrs []attribute.KeyValue, key string) (string, bool) {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value.AsString(), true
		}
	}
	return "", false
}

func TestStartPublishSpan_InjectsProducerSpanIntoHeaders(t *testing.T) {
	exp := installTestTracing(t)

	parentCtx, parent := otel.Tracer("test").Start(context.Background(), "caller")
	_, span, headers := startPublishSpan(parentCtx, "manman", "command.host.1.start", "corr-1")
	endSpan(span, nil)
	parent.End()

	got := findSpan(t, exp, "command.host.1.start publish")
	if got.SpanKind != trace.SpanKindProducer {
		t.Errorf("span kind = %v, want producer", got.SpanKind)
	}
	if got.Parent.SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("producer span not parented by caller span")
	}
	for key, want := range map[string]string{
		"messaging.system":                           "rabbitmq",
		"messaging.destination.name":                 "manman",
		"messaging.rabbitmq.destination.routing_key": "command.host.1.start",
		"messaging.operation.type":                   "publish",
		"messaging.message.conversation_id":          "corr-1",
	} {
		if v, ok := attrValue(got.Attributes, key); !ok || v != want {
			t.Errorf("attr %s = %q (present=%v), want %q", key, v, ok, want)
		}
	}

	// The injected header must name the producer span, not the caller.
	extracted := trace.SpanContextFromContext(extractHeaders(context.Background(), headers))
	if extracted.SpanID() != got.SpanContext.SpanID() {
		t.Errorf("traceparent header span = %s, want producer span %s", extracted.SpanID(), got.SpanContext.SpanID())
	}
	if extracted.TraceID() != parent.SpanContext().TraceID() {
		t.Errorf("traceparent header trace = %s, want %s", extracted.TraceID(), parent.SpanContext().TraceID())
	}
}

func TestHandleMessage_ConsumerSpanParentedByHeaders(t *testing.T) {
	exp := installTestTracing(t)

	_, pubSpan, headers := startPublishSpan(context.Background(), "manman", "command.host.1.stop", "")
	endSpan(pubSpan, nil)
	producer := findSpan(t, exp, "command.host.1.stop publish")

	var handlerSC trace.SpanContext
	c := &Consumer{handlers: map[string]MessageHandler{
		"command.host.1.stop": func(ctx context.Context, msg Message) error {
			handlerSC = trace.SpanContextFromContext(ctx)
			return nil
		},
	}}
	c.handleMessage(context.Background(), amqp.Delivery{
		Acknowledger: &fakeAcknowledger{},
		Exchange:     "manman",
		RoutingKey:   "command.host.1.stop",
		Headers:      headers,
	})

	consumer := findSpan(t, exp, "command.host.1.stop process")
	if consumer.SpanKind != trace.SpanKindConsumer {
		t.Errorf("span kind = %v, want consumer", consumer.SpanKind)
	}
	if consumer.Parent.SpanID() != producer.SpanContext.SpanID() {
		t.Errorf("consumer parent = %s, want producer %s", consumer.Parent.SpanID(), producer.SpanContext.SpanID())
	}
	if consumer.SpanContext.TraceID() != producer.SpanContext.TraceID() {
		t.Errorf("consumer trace = %s, want %s", consumer.SpanContext.TraceID(), producer.SpanContext.TraceID())
	}
	if handlerSC.SpanID() != consumer.SpanContext.SpanID() {
		t.Errorf("handler ctx span = %s, want consumer span %s", handlerSC.SpanID(), consumer.SpanContext.SpanID())
	}
	if consumer.Status.Code == codes.Error {
		t.Errorf("successful handler recorded error status")
	}
}

func TestHandleMessage_HandlerErrorRecordedOnSpan(t *testing.T) {
	exp := installTestTracing(t)

	c := &Consumer{handlers: map[string]MessageHandler{
		"#": func(ctx context.Context, msg Message) error {
			return &PermanentError{Err: errors.New("boom")}
		},
	}}
	ack := &fakeAcknowledger{}
	c.handleMessage(context.Background(), amqp.Delivery{Acknowledger: ack, RoutingKey: "status.x"})

	consumer := findSpan(t, exp, "status.x process")
	if consumer.Status.Code != codes.Error {
		t.Errorf("status = %v, want Error", consumer.Status.Code)
	}
	if len(consumer.Events) == 0 {
		t.Errorf("expected an exception event from RecordError")
	}
	if !ack.nacked {
		t.Errorf("permanent error should still Nack the message")
	}
}
