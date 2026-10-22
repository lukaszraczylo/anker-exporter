// Package config reads the exporter settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"strings"
)

const (
	defaultRegion  = "eu"
	defaultCountry = "GB"
	redacted       = "[redacted]"
)

var apiHosts = map[string]string{
	"eu":  "https://ankerpower-api-eu.anker.com",
	"com": "https://ankerpower-api.anker.com",
}

// Config holds the Anker account settings.
type Config struct {
	Email    string
	Password string
	Region   string
	Country  string
	BaseURL  string
	DeviceSN string
}

// Load reads the settings through getenv.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Email:    getenv("ANKER_EMAIL"),
		Password: getenv("ANKER_PASSWORD"),
		Region:   strings.ToLower(orDefault(getenv("ANKER_REGION"), defaultRegion)),
		Country:  orDefault(getenv("ANKER_COUNTRY"), defaultCountry),
		DeviceSN: getenv("ANKER_DEVICE_SN"),
	}
	if c.Email == "" {
		return Config{}, errors.New("ANKER_EMAIL is required")
	}
	if c.Password == "" {
		return Config{}, errors.New("ANKER_PASSWORD is required")
	}
	base, ok := apiHosts[c.Region]
	if !ok {
		return Config{}, fmt.Errorf("ANKER_REGION %q must be eu or com", c.Region)
	}
	c.BaseURL = base
	return c, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// String omits the password so a logged Config leaks nothing.
func (c Config) String() string {
	return fmt.Sprintf("Config{Email:%s Password:%s Region:%s Country:%s DeviceSN:%s}", c.Email, redacted, c.Region, c.Country, MaskSN(c.DeviceSN))
}

// GoString covers %#v.
func (c Config) GoString() string { return c.String() }

// MaskSN keeps the last 4 characters of a serial.
func MaskSN(sn string) string {
	if len(sn) <= 4 {
		return strings.Repeat("*", len(sn))
	}
	return "..." + sn[len(sn)-4:]
}
