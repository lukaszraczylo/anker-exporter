package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_Defaults(t *testing.T) {
	c, err := Load(env(map[string]string{"ANKER_EMAIL": "a@b.c", "ANKER_PASSWORD": "pw"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Region != "eu" || c.Country != "GB" || c.DeviceSN != "" {
		t.Fatalf("defaults wrong: %+v", c)
	}
	if c.BaseURL != "https://ankerpower-api-eu.anker.com" {
		t.Fatalf("base URL %q", c.BaseURL)
	}
}

func TestLoad_RegionCom(t *testing.T) {
	c, err := Load(env(map[string]string{"ANKER_EMAIL": "a", "ANKER_PASSWORD": "p", "ANKER_REGION": "COM", "ANKER_DEVICE_SN": "SN1"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Region != "com" || c.BaseURL != "https://ankerpower-api.anker.com" || c.DeviceSN != "SN1" {
		t.Fatalf("got %+v", c)
	}
}

func TestLoad_MissingVars(t *testing.T) {
	for _, m := range []map[string]string{
		{"ANKER_EMAIL": "a"},
		{"ANKER_PASSWORD": "p"},
		{},
	} {
		if _, err := Load(env(m)); err == nil {
			t.Fatalf("expected error for %v", m)
		}
	}
}

func TestLoad_BadRegion(t *testing.T) {
	_, err := Load(env(map[string]string{"ANKER_EMAIL": "a", "ANKER_PASSWORD": "p", "ANKER_REGION": "us"}))
	if err == nil || !strings.Contains(err.Error(), "ANKER_REGION") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigString_RedactsPassword(t *testing.T) {
	c, err := Load(env(map[string]string{"ANKER_EMAIL": "a@b.c", "ANKER_PASSWORD": "hunter2-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{c.String(), c.GoString()} {
		if strings.Contains(s, "hunter2-secret") {
			t.Fatalf("password leaked: %s", s)
		}
	}
}

func TestParsePricing(t *testing.T) {
	cases := []struct {
		name, price, currency string
		want                  Pricing
	}{
		{"off", "", "", Pricing{}},
		{"currency without price is a no-op", "", "usd", Pricing{}},
		{"default currency", "0.25", "", Pricing{PerKWh: 0.25, Currency: "GBP"}},
		{"currency upper-cased", "0.3", "eur", Pricing{PerKWh: 0.3, Currency: "EUR"}},
		{"whitespace trimmed", " 1.5 ", " usd ", Pricing{PerKWh: 1.5, Currency: "USD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePricing(tc.price, tc.currency)
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
			if got.Enabled() != (tc.want.PerKWh > 0) {
				t.Fatalf("Enabled() = %v", got.Enabled())
			}
		})
	}
}

func TestParsePricing_Invalid(t *testing.T) {
	cases := []struct{ name, price, currency, wantInErr string }{
		{"non-numeric", "cheap", "", "ANKER_PRICE_PER_KWH"},
		{"zero", "0", "", "ANKER_PRICE_PER_KWH"},
		{"negative", "-0.1", "", "ANKER_PRICE_PER_KWH"},
		{"NaN", "NaN", "", "ANKER_PRICE_PER_KWH"},
		{"Inf", "+Inf", "", "ANKER_PRICE_PER_KWH"},
		{"short currency", "0.25", "GB", "ANKER_CURRENCY"},
		{"long currency", "0.25", "GBPX", "ANKER_CURRENCY"},
		{"digit currency", "0.25", "G8P", "ANKER_CURRENCY"},
		{"non-ASCII currency", "0.25", "£££", "ANKER_CURRENCY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePricing(tc.price, tc.currency)
			if err == nil || !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("err = %v, want mention of %s", err, tc.wantInErr)
			}
		})
	}
}
