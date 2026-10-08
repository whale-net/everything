package rmq

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "libs/go/rmq"

// Attribute keys follow the OpenTelemetry messaging semantic conventions.
const (
	attrMessagingSystem     = "messaging.system"
	attrMessagingOperation  = "messaging.operation.type"
	attrMessagingDestName   = "messaging.destination.name"
	attrRabbitMQRoutingKey  = "messaging.rabbitmq.destination.routing_key"
	attrConversationID      = "messaging.message.conversation_id"
	messagingSystemRabbitMQ = "rabbitmq"
)

func messagingAttrs(operation, exchange, routingKey, correlationID string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(attrMessagingSystem, messagingSystemRabbitMQ),
		attribute.String(attrMessagingOperation, operation),
		attribute.String(attrMessagingDestName, exchange),
		attribute.String(attrRabbitMQRoutingKey, routingKey),
	}
	if correlationID != "" {
		attrs = append(attrs, attribute.String(attrConversationID, correlationID))
	}
	return attrs
}

// startPublishSpan starts a producer span for a publish to exchange/routingKey
// and returns AMQP headers carrying that span's W3C trace context.
func startPublishSpan(ctx context.Context, exchange, routingKey, correlationID string) (context.Context, trace.Span, amqp.Table) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, routingKey+" publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(messagingAttrs("publish", exchange, routingKey, correlationID)...),
	)
	return ctx, span, injectHeaders(ctx)
}

// injectHeaders serializes ctx's trace context into an AMQP header table.
func injectHeaders(ctx context.Context) amqp.Table {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	headers := amqp.Table{}
	for k, v := range carrier {
		headers[k] = v
	}
	return headers
}

// extractHeaders returns ctx with any trace context found in AMQP headers.
func extractHeaders(ctx context.Context, headers amqp.Table) context.Context {
	carrier := propagation.MapCarrier{}
	for k, v := range headers {
		if s, ok := v.(string); ok {
			carrier[k] = s
		}
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// startConsumeSpan starts a consumer span parented by the publisher's trace
// context carried in the delivery headers.
func startConsumeSpan(ctx context.Context, d amqp.Delivery) (context.Context, trace.Span) {
	ctx = extractHeaders(ctx, d.Headers)
	return otel.Tracer(tracerName).Start(ctx, d.RoutingKey+" process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(messagingAttrs("process", d.Exchange, d.RoutingKey, d.CorrelationId)...),
	)
}

// endSpan records err (if any) on span and ends it.
func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
