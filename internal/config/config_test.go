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
