// Package mqttsession wraps the Paho client for the Anker AWS IoT broker.
package mqttsession

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
)

const (
	brokerPort     = "8883"
	connectTimeout = 15 * time.Second
	opTimeout      = 10 * time.Second
	qosAtMostOnce  = 0
	clientIDDigits = 100000
)

// Session is a connected MQTT client. Subscriptions are restored after a reconnect.
type Session struct {
	client  mqtt.Client
	handler func(topic string, payload []byte)
	topics  []string
	mu      sync.Mutex
}

// ClientID returns {thingName}_{5 random digits}.
func ClientID(thingName string) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(clientIDDigits))
	if err != nil {
		return "", fmt.Errorf("client id: %w", err)
	}
	return fmt.Sprintf("%s_%05d", thingName, n.Int64()), nil
}

// TLSConfig builds the mutual TLS config from the PEM fields. The error never contains key material.
func TLSConfig(info anker.MQTTInfo) (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(info.CertPEM), []byte(info.KeyPEM))
	if err != nil {
		return nil, errors.New("mqtt: client certificate and key do not form a valid pair")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(info.RootCAPEM)) {
		return nil, errors.New("mqtt: root CA PEM holds no certificate")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS12,
		ServerName:   info.Endpoint,
	}, nil
}

// Connect opens the session. handler runs for every message on a subscribed topic.
func Connect(ctx context.Context, info anker.MQTTInfo, handler func(topic string, payload []byte)) (*Session, error) {
	if info.Endpoint == "" {
		return nil, errors.New("mqtt: empty endpoint")
	}
	tlsCfg, err := TLSConfig(info)
	if err != nil {
		return nil, err
	}
	id, err := ClientID(info.ThingName)
	if err != nil {
		return nil, err
	}
	s := &Session{handler: handler}
	opts := mqtt.NewClientOptions().
		AddBroker("ssl://" + net.JoinHostPort(info.Endpoint, brokerPort)).
		SetClientID(id).
		SetTLSConfig(tlsCfg).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectTimeout(connectTimeout).
		SetOnConnectHandler(func(c mqtt.Client) { s.resubscribe(c) })
	s.client = mqtt.NewClient(opts)
	tok := s.client.Connect()
	if err := wait(ctx, tok, connectTimeout+opTimeout); err != nil {
		return nil, fmt.Errorf("mqtt connect: %w", err)
	}
	return s, nil
}

func (s *Session) resubscribe(c mqtt.Client) {
	s.mu.Lock()
	topics := append([]string(nil), s.topics...)
	s.mu.Unlock()
	for _, t := range topics {
		c.Subscribe(t, qosAtMostOnce, s.onMessage)
	}
}

func (s *Session) onMessage(_ mqtt.Client, m mqtt.Message) { s.handler(m.Topic(), m.Payload()) }

// Subscribe subscribes to topic and keeps it across reconnects.
func (s *Session) Subscribe(topic string) error {
	s.mu.Lock()
	s.topics = append(s.topics, topic)
	s.mu.Unlock()
	if err := wait(context.Background(), s.client.Subscribe(topic, qosAtMostOnce, s.onMessage), opTimeout); err != nil {
		return fmt.Errorf("mqtt subscribe %s: %w", topic, err)
	}
	return nil
}

// Publish sends payload to topic.
func (s *Session) Publish(topic string, payload []byte) error {
	if err := wait(context.Background(), s.client.Publish(topic, qosAtMostOnce, false, payload), opTimeout); err != nil {
		return fmt.Errorf("mqtt publish %s: %w", topic, err)
	}
	return nil
}

// Close disconnects.
func (s *Session) Close() { s.client.Disconnect(250) }

func wait(ctx context.Context, tok mqtt.Token, d time.Duration) error {
	select {
	case <-tok.Done():
		return tok.Error()
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return errors.New("timed out")
	}
}
