package htmxsse

import (
	"context"

	"github.com/whale-net/everything/libs/go/rmq"
)

// DefaultAttachFunc creates an attach function that uses rmq.NewConsumerWithOpts
// with the arguments specified in the spec:
// - queueName="" (server-generated)
// - durable=false
// - autoDelete=true (ephemeral queue)
// - messageTTL=0 (no TTL)
// - maxMessages=0 (no length limit)
//
// This ensures every process replica receives every event independently.
// The exchangeName parameter must match the exchange name configured in the Hub's Config.
func DefaultAttachFunc(exchangeName string, conn *rmq.Connection) AttachFunc {
	return func(ctx context.Context) (Transport, error) {
		// Declare the exchange as part of the attach path (FR0.7)
		ch, err := conn.Channel()
		if err != nil {
			return nil, err
		}
		defer ch.Close()

		err = ch.ExchangeDeclare(exchangeName, "topic", true, false, false, false, nil)
		if err != nil {
			return nil, err
		}

		// Create an ephemeral consumer as specified
		return rmq.NewConsumerWithOpts(conn, "", false, true, 0, 0)
	}
}
