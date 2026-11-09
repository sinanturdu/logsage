package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sinanturdu/logsage/backend/internal/config"
)

// The evaluator refuses to start without a usable key, before it connects
// to anything, and never prints the key.
func TestRefusesToStartWithoutKey(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{}, "alerts are disabled"},
		{map[string]string{"ALERTS_ENABLED": "true"}, "ALERTS_SECRET_KEY"},
		{map[string]string{"ALERTS_ENABLED": "true", "ALERTS_SECRET_KEY": "c2hvcnQta2V5"}, "ALERTS_SECRET_KEY"},
	} {
		err := run(context.Background(), func(k string) string { return c.env[k] }, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.env, err)
		}
		if c.env["ALERTS_SECRET_KEY"] != "" && strings.Contains(err.Error(), c.env["ALERTS_SECRET_KEY"]) {
			t.Error("the error shows the key")
		}
	}
	err := run(context.Background(), func(k string) string { return map[string]string{"ALERTS_ENABLED": "true"}[k] }, nil)
	if !errors.Is(err, config.ErrAlertKey) {
		t.Fatalf("%v", err)
	}
}
