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
	"slices"
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
	onError func(error)
	lost    chan struct{}
	lostOne sync.Once
	mu      sync.Mutex
}

// Option tunes Connect.
type Option func(*Session)

// WithOnError reports failures that have no caller to return to, such as a resubscribe after a reconnect.
func WithOnError(f func(error)) Option { return func(s *Session) { s.onError = f } }

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
func Connect(ctx context.Context, info anker.MQTTInfo, handler func(topic string, payload []byte), options ...Option) (*Session, error) {
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
	s := &Session{handler: handler, lost: make(chan struct{})}
	for _, o := range options {
		o(s)
	}
	opts := mqtt.NewClientOptions().
		AddBroker("ssl://" + net.JoinHostPort(info.Endpoint, brokerPort)).
		SetClientID(id).
		SetTLSConfig(tlsCfg).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectTimeout(connectTimeout).
		SetConnectionLostHandler(func(mqtt.Client, error) { s.lostOne.Do(func() { close(s.lost) }) }).
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
		if err := wait(context.Background(), c.Subscribe(t, qosAtMostOnce, s.onMessage), opTimeout); err != nil && s.onError != nil {
			s.onError(fmt.Errorf("mqtt resubscribe %s: %w", t, err))
		}
	}
}

// Lost is closed when the broker connection drops.
func (s *Session) Lost() <-chan struct{} { return s.lost }

func (s *Session) onMessage(_ mqtt.Client, m mqtt.Message) { s.handler(m.Topic(), m.Payload()) }

func (s *Session) addTopic(topic string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.topics, topic) {
		s.topics = append(s.topics, topic)
	}
}

// Subscribe subscribes to topic and keeps it across reconnects.
func (s *Session) Subscribe(topic string) error {
	s.addTopic(topic)
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
