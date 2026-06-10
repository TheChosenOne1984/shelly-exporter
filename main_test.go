package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestJoinKey(t *testing.T) {
	tests := []struct {
		a, b, want string
	}{
		{"", "foo", "foo"},
		{"a", "b", "a.b"},
		{"a.b", "c", "a.b.c"},
	}
	for _, tt := range tests {
		if got := joinKey(tt.a, tt.b); got != tt.want {
			t.Errorf("joinKey(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSanitizeKeys(t *testing.T) {
	in := map[string]float64{
		"valid.key": 1,
		"has space": 2,
		"has/slash": 3,
		"ok:label":  4,
	}
	out := sanitizeKeys(in)
	if out["valid.key"] != 1 {
		t.Error("valid.key should pass through unchanged")
	}
	if _, ok := out["has space"]; ok {
		t.Error("original key with space should not exist in output")
	}
	if out["has_space"] != 2 {
		t.Error("space should be replaced by underscore")
	}
	if out["has_slash"] != 3 {
		t.Error("slash should be replaced by underscore")
	}
	if out["ok:label"] != 4 {
		t.Error("colon is allowed and should not be replaced")
	}
}

func TestEndpointKeyPrefix(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"rpc/EM.GetStatus?id=0", "rpc.EM.GetStatus.id_0"},
		{"/rpc/Sys.GetStatus", "rpc.Sys.GetStatus"},
		{"rpc/EM.GetStatus?id=0&foo=bar", "rpc.EM.GetStatus.id_0.foo_bar"},
	}
	for _, tt := range tests {
		got := endpointKeyPrefix(tt.input)
		if got != tt.want {
			t.Errorf("endpointKeyPrefix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFlattenJSON(t *testing.T) {
	input := map[string]any{
		"a": json.Number("1.5"),
		"b": map[string]any{
			"c": json.Number("2"),
		},
		"d": []any{json.Number("3"), json.Number("4")},
	}
	out := map[string]float64{}
	flattenJSON("", input, out)

	if out["a"] != 1.5 {
		t.Errorf("expected a=1.5, got %v", out["a"])
	}
	if out["b.c"] != 2 {
		t.Errorf("expected b.c=2, got %v", out["b.c"])
	}
	if out["d.0"] != 3 {
		t.Errorf("expected d.0=3, got %v", out["d.0"])
	}
	if out["d.1"] != 4 {
		t.Errorf("expected d.1=4, got %v", out["d.1"])
	}
}

func TestGuessTotalActivePower(t *testing.T) {
	t.Run("exact match on total_act_power", func(t *testing.T) {
		m := map[string]float64{"total_act_power": 42.0, "total_act_power_a": 10.0}
		v, ok := guessTotalActivePower(m)
		if !ok || v != 42.0 {
			t.Errorf("expected 42.0 ok=true, got %v ok=%v", v, ok)
		}
	})
	t.Run("no partial match on per-phase keys", func(t *testing.T) {
		// Only phase-level keys present — must NOT match (substring match was a bug)
		m := map[string]float64{"total_act_power_a": 10.0, "total_act_power_b": 20.0}
		_, ok := guessTotalActivePower(m)
		if ok {
			t.Error("partial key total_act_power_a must not trigger a match")
		}
	})
	t.Run("no match", func(t *testing.T) {
		m := map[string]float64{"something_else": 1.0}
		_, ok := guessTotalActivePower(m)
		if ok {
			t.Error("expected no match")
		}
	})
	t.Run("exact match on total_active_power", func(t *testing.T) {
		m := map[string]float64{"total_active_power": 100.0}
		v, ok := guessTotalActivePower(m)
		if !ok || v != 100.0 {
			t.Errorf("expected 100.0 ok=true, got %v ok=%v", v, ok)
		}
	})
}

func TestLoadConfigDefaults(t *testing.T) {
	content := `
devices:
  - name: test-device
    address: http://192.168.0.1
    username: user
    password: pass
`
	f, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cfg, err := loadConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.ListenAddress != ":9905" {
		t.Errorf("expected default listen address :9905, got %q", cfg.Server.ListenAddress)
	}
	if cfg.Server.RequestTimeoutSeconds != 5 {
		t.Errorf("expected default timeout 5, got %d", cfg.Server.RequestTimeoutSeconds)
	}
	if len(cfg.Devices[0].Endpoints) != 1 {
		t.Errorf("expected 1 default endpoint, got %d", len(cfg.Devices[0].Endpoints))
	}
}

func TestLoadConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			"no devices",
			"devices: []\n",
		},
		{
			"missing address",
			"devices:\n  - name: x\n    username: u\n    password: p\n",
		},
		{
			"missing credentials",
			"devices:\n  - name: x\n    address: http://a\n    username: u\n",
		},
		{
			"duplicate name",
			"devices:\n" +
				"  - name: x\n    address: http://a\n    username: u\n    password: p\n" +
				"  - name: x\n    address: http://b\n    username: u\n    password: p\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.CreateTemp("", "config-*.yaml")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(f.Name())
			if _, err := f.WriteString(tc.content); err != nil {
				t.Fatal(err)
			}
			f.Close()

			_, err = loadConfig(f.Name())
			if err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}
