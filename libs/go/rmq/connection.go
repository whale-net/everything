package rmq

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

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

// TLSConfig holds TLS/SSL configuration for RabbitMQ connections
type TLSConfig struct {
	// Enabled determines if TLS should be used
	Enabled bool
	// InsecureSkipVerify controls whether to verify the server's certificate chain and host name
	// Setting this to true is insecure and should only be used for development/testing
	InsecureSkipVerify bool
	// CACertPath is the path to a custom CA certificate file for verifying the server certificate
	CACertPath string
	// ServerName is used to verify the hostname on the server certificate
	// Use this when the connection URL hostname differs from the certificate hostname
	// Example: connecting to internal k8s service but cert is for external domain
	ServerName string
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
// For amqps:// URLs, TLS configuration will be loaded from environment variables:
//   - RABBITMQ_SSL_VERIFY=false (optional): Disable certificate verification (insecure, dev only)
//   - RABBITMQ_CA_CERT_PATH=/path/to/ca.crt (optional): Custom CA certificate
//   - RABBITMQ_TLS_SERVER_NAME=rmq.example.com (optional): Server name for certificate verification
func NewConnectionFromURL(url string) (*Connection, error) {
	// Check if URL uses TLS (amqps://)
	if strings.HasPrefix(url, "amqps://") {
		tlsConfig := getTLSConfigFromEnv()
		return NewConnectionWithTLS(url, tlsConfig)
	}

	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	return &Connection{conn: conn}, nil
}

// NewConnectionWithTLS creates a new RabbitMQ connection with explicit TLS configuration
func NewConnectionWithTLS(url string, tlsConfig *TLSConfig) (*Connection, error) {
	config, err := buildTLSConfig(tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build TLS config: %w", err)
	}

	conn, err := amqp.DialTLS(url, config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ with TLS: %w", err)
	}

	return &Connection{conn: conn}, nil
}

// getTLSConfigFromEnv creates TLS configuration from environment variables
func getTLSConfigFromEnv() *TLSConfig {
	config := &TLSConfig{
		Enabled:            true,
		InsecureSkipVerify: false,
	}

	// Check if SSL verification should be disabled
	if verify := os.Getenv("RABBITMQ_SSL_VERIFY"); verify == "false" {
		config.InsecureSkipVerify = true
	}

	// Check for custom CA certificate path
	if caPath := os.Getenv("RABBITMQ_CA_CERT_PATH"); caPath != "" {
		config.CACertPath = caPath
	}

	// Check for custom server name for certificate verification
	if serverName := os.Getenv("RABBITMQ_TLS_SERVER_NAME"); serverName != "" {
		config.ServerName = serverName
	}

	return config
}

// buildTLSConfig creates a tls.Config from TLSConfig
func buildTLSConfig(config *TLSConfig) (*tls.Config, error) {
	if config == nil {
		return &tls.Config{}, nil
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: config.InsecureSkipVerify,
	}

	// Set server name for certificate verification if provided
	if config.ServerName != "" {
		tlsConfig.ServerName = config.ServerName
	}

	// Load custom CA certificate if provided
	if config.CACertPath != "" {
		caCert, err := os.ReadFile(config.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate from %s: %w", config.CACertPath, err)
		}

		caCertPool := x509.NewCertPool()
		if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
			return nil, fmt.Errorf("failed to parse CA certificate from %s", config.CACertPath)
		}

		tlsConfig.RootCAs = caCertPool
	}

	return tlsConfig, nil
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
