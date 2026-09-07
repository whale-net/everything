// transport.go defines the narrow interface the board runtime (runner.go)
// publishes and subscribes through, decoupling it from the paho MQTT
// client so it is testable without a broker -- see fakeTransport in
// transport_test.go. pahoTransport is the real implementation main.go
// wires up.
package main

import (
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Transport is what a board runtime publishes and subscribes through.
// Subscribe is unused by this task, but belongs on the interface now so
// the config-apply task (which depends on this one) does not have to
// reshape it.
type Transport interface {
	Publish(topic string, qos byte, retained bool, payload []byte) error
	Subscribe(topic string, qos byte, cb func(topic string, payload []byte)) error
	Disconnect()
}

// pahoTransport implements Transport by wrapping a paho mqtt.Client. One
// pahoTransport is created per simulated board -- never shared -- so each
// board has its own connection identity (ClientID, LWT), exactly like N
// real boards. Connect-time options (ClientID, LWT, auto-reconnect,
// OnConnect) are constructed by NewPahoRunner (runner.go); Connect() itself
// is invoked by Runner.Start().
type pahoTransport struct {
	client mqtt.Client
}

// newPahoTransport wraps an already-configured (but not yet connected)
// paho client.
func newPahoTransport(client mqtt.Client) *pahoTransport {
	return &pahoTransport{client: client}
}

func (t *pahoTransport) Publish(topic string, qos byte, retained bool, payload []byte) error {
	token := t.client.Publish(topic, qos, retained, payload)
	token.Wait()
	return token.Error()
}

func (t *pahoTransport) Subscribe(topic string, qos byte, cb func(topic string, payload []byte)) error {
	token := t.client.Subscribe(topic, qos, func(_ mqtt.Client, msg mqtt.Message) {
		cb(msg.Topic(), msg.Payload())
	})
	token.Wait()
	return token.Error()
}

func (t *pahoTransport) Disconnect() {
	t.client.Disconnect(250)
}
