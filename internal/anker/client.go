package anker

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // gtoken is defined by the Anker API as md5(user_id)
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pathLogin     = "passport/login"
	pathMQTTInfo  = "app/devicemanage/get_user_mqtt_info"
	pathDevices   = "power_service/v1/app/get_relate_and_bind_devices"
	deviceModelPN = "A1761"
	maxBodyBytes  = 4 << 20
)

// MQTTInfo holds the AWS IoT connection details returned by the cloud API.
type MQTTInfo struct {
	Endpoint, ThingName, UserID, CertificateID, AppName, CertPEM, KeyPEM, RootCAPEM string
}

// Device is a bound device.
type Device struct{ SN, PN string }

// Client is an Anker cloud API client. Safe for concurrent use.
type Client struct {
	httpc    *http.Client
	base     string
	email    string
	password string
	country  string
	token    string
	gtoken   string
	mu       sync.Mutex
}

type envelope struct {
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
	Code int             `json:"code"`
}

// DefaultTimeout covers the login endpoint, which answered in about 22 s in testing.
const DefaultTimeout = 120 * time.Second

// APIError is a non-zero application code in an otherwise successful HTTP answer.
type APIError struct {
	Msg  string
	Code int
}

func (e *APIError) Error() string { return fmt.Sprintf("server code %d: %s", e.Code, e.Msg) }

var errExpired = errors.New("token rejected")

// NewClient builds a client for the API at baseURL. httpc may be nil.
func NewClient(baseURL, email, password, country string, httpc *http.Client) (*Client, error) {
	if baseURL == "" || email == "" || password == "" {
		return nil, errors.New("base URL, email and password are required")
	}
	if httpc == nil {
		httpc = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), email: email, password: password, country: country, httpc: httpc}, nil
}

// Login authenticates and stores the session token.
func (c *Client) Login(ctx context.Context) error {
	kx, err := NewKeyExchange(ServerPublicKeyHex)
	if err != nil {
		return err
	}
	enc, err := kx.EncryptPassword(c.password)
	if err != nil {
		return fmt.Errorf("encrypt password: %w", err)
	}
	now := time.Now()
	_, off := now.Zone()
	body := map[string]any{
		"ab":                 c.country,
		"client_secret_info": map[string]string{"public_key": kx.PublicKeyHex()},
		"enc":                0,
		"email":              c.email,
		"password":           enc,
		"time_zone":          off * 1000,
		"transaction":        strconv.FormatInt(now.UnixMilli(), 10),
	}
	env, err := c.do(ctx, pathLogin, body, false)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	var d struct {
		AuthToken string `json:"auth_token"`
		UserID    string `json:"user_id"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return fmt.Errorf("login: decode data: %w", err)
	}
	if d.AuthToken == "" || d.UserID == "" {
		return errors.New("login: response lacks auth_token or user_id")
	}
	sum := md5.Sum([]byte(d.UserID)) //nolint:gosec // see import
	c.mu.Lock()
	c.token, c.gtoken = d.AuthToken, hex.EncodeToString(sum[:])
	c.mu.Unlock()
	return nil
}

// MQTTInfo fetches the MQTT endpoint and client certificate.
func (c *Client) MQTTInfo(ctx context.Context) (MQTTInfo, error) {
	env, err := c.authed(ctx, pathMQTTInfo)
	if err != nil {
		return MQTTInfo{}, err
	}
	var d struct {
		Endpoint      string `json:"endpoint_addr"`
		ThingName     string `json:"thing_name"`
		UserID        string `json:"user_id"`
		CertificateID string `json:"certificate_id"`
		AppName       string `json:"app_name"`
		CertPEM       string `json:"certificate_pem"`
		KeyPEM        string `json:"private_key"`
		RootCAPEM     string `json:"aws_root_ca1_pem"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return MQTTInfo{}, fmt.Errorf("mqtt info: decode data: %w", err)
	}
	return MQTTInfo(d), nil
}

// Devices lists bound A1761 devices.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	env, err := c.authed(ctx, pathDevices)
	if err != nil {
		return nil, err
	}
	var d struct {
		Data []struct {
			SN string `json:"device_sn"`
			PN string `json:"product_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return nil, fmt.Errorf("devices: decode data: %w", err)
	}
	var out []Device
	for _, x := range d.Data {
		if x.PN == deviceModelPN {
			out = append(out, Device{SN: x.SN, PN: x.PN})
		}
	}
	return out, nil
}

// authed posts to path, logging in first when needed and once more on an expired token.
func (c *Client) authed(ctx context.Context, path string) (*envelope, error) {
	c.mu.Lock()
	have := c.token != ""
	c.mu.Unlock()
	if !have {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}
	env, err := c.do(ctx, path, map[string]any{}, true)
	if !errors.Is(err, errExpired) {
		return env, err
	}
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	return c.do(ctx, path, map[string]any{}, true)
}

func (c *Client) do(ctx context.Context, path string, body any, auth bool) (*envelope, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("model-type", "DESKTOP")
	req.Header.Set("app-name", "anker_power")
	req.Header.Set("os-type", "android")
	req.Header.Set("country", c.country)
	req.Header.Set("timezone", gmtString(time.Now()))
	if auth {
		c.mu.Lock()
		req.Header.Set("x-auth-token", c.token)
		req.Header.Set("gtoken", c.gtoken)
		c.mu.Unlock()
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// session.py re-authenticates on HTTP 401/403 only
	if auth && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		return nil, errExpired
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", path, err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("%s: %w", path, &APIError{Code: env.Code, Msg: env.Msg})
	}
	return &env, nil
}

func gmtString(t time.Time) string {
	_, off := t.Zone()
	sign := '+'
	if off < 0 {
		sign, off = '-', -off
	}
	return fmt.Sprintf("GMT%c%02d:%02d", sign, off/3600, off%3600/60)
}
