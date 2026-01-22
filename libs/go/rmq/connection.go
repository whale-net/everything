package rmq

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Connection wraps a RabbitMQ connection
type Connection struct {
	conn *amqp.Connection
}

// Config holds RabbitMQ connection configuration
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	VHost    string
}

// NewConnection creates a new RabbitMQ connection
func NewConnection(config Config) (*Connection, error) {
	url := fmt.Sprintf("amqp://%s:%s@%s:%d%s",
		config.Username,
		config.Password,
		config.Host,
		config.Port,
		config.VHost,
	)

	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	return &Connection{conn: conn}, nil
}

// NewConnectionFromURL creates a new RabbitMQ connection from a URL
func NewConnectionFromURL(url string) (*Connection, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	return &Connection{conn: conn}, nil
}

// Close closes the RabbitMQ connection
func (c *Connection) Close() error {
	if c.conn != nil && !c.conn.IsClosed() {
		return c.conn.Close()
	}
	return nil
}

// GetConnection returns the underlying AMQP connection
func (c *Connection) GetConnection() *amqp.Connection {
	return c.conn
}

// Channel creates a new channel from the connection
func (c *Connection) Channel() (*amqp.Channel, error) {
	return c.conn.Channel()
}

// NotifyClose returns a channel that will be closed when the connection is closed
func (c *Connection) NotifyClose(receiver chan *amqp.Error) chan *amqp.Error {
	return c.conn.NotifyClose(receiver)
}
