package config

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadAlertsDefaults(t *testing.T) {
	c, err := LoadAlerts(env(nil))
	if err != nil || c.Enabled || c.Tick != 5*time.Second || c.Lateness != time.Minute || c.MaxAttempts != 8 || c.KeyErr != nil {
		t.Fatalf("%+v %v", c, err)
	}
	if err := c.RequireKey(); err == nil {
		t.Fatal("disabled alerts must not start the evaluator")
	}
}

func TestLoadAlertsKey(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(make([]byte, 32))
	c, err := LoadAlerts(env(map[string]string{"ALERTS_ENABLED": "true", "ALERTS_SECRET_KEY": good}))
	if err != nil || len(c.SecretKey) != 32 || c.RequireKey() != nil {
		t.Fatalf("good key: %v %v", err, c.RequireKey())
	}
	for _, k := range []string{"", "short", base64.StdEncoding.EncodeToString(make([]byte, 16)), "not base64 !!"} {
		c, err := LoadAlerts(env(map[string]string{"ALERTS_ENABLED": "true", "ALERTS_SECRET_KEY": k}))
		if err != nil {
			t.Fatalf("a bad key is not a load error: %v", err)
		}
		if !errors.Is(c.RequireKey(), ErrAlertKey) || c.SecretKey != nil {
			t.Fatalf("key %q: %v", k, c.RequireKey())
		}
		if k != "" && strings.Contains(ErrAlertKey.Error(), k) {
			t.Fatal("the error shows the key")
		}
	}
	// Without ALERTS_ENABLED the key is not read.
	if c, _ := LoadAlerts(env(map[string]string{"ALERTS_SECRET_KEY": "short"})); c.KeyErr != nil {
		t.Fatal("key checked while disabled")
	}
}

func TestLoadAlertsBounds(t *testing.T) {
	for k, v := range map[string]string{
		"ALERTS_LATENESS":             "11m",
		"ALERTS_TICK":                 "0s",
		"ALERTS_CONCURRENCY":          "0",
		"ALERTS_MAX_ATTEMPTS":         "99",
		"ALERTS_ALLOWED_PORTS":        "443,x",
		"ALERTS_ENABLED":              "maybe",
		"PUBLIC_DASHBOARD_URL":        "javascript:alert(1)",
		"ALERTS_ALLOW_INSECURE_LOCAL": "yes please",
	} {
		if _, err := LoadAlerts(env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
	c, err := LoadAlerts(env(map[string]string{"ALERTS_ALLOWED_PORTS": "8443, 9443", "ALERTS_LATENESS": "0s", "PUBLIC_DASHBOARD_URL": "https://logsage.example.com/"}))
	if err != nil || len(c.AllowedPorts) != 2 || c.AllowedPorts[1] != 9443 || c.Lateness != 0 || !strings.HasPrefix(c.DashboardURL, "https://logsage.example.com") {
		t.Fatalf("%+v %v", c, err)
	}
}
