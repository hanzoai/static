package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestToCamel(t *testing.T) {
	cases := map[string]string{
		"API_HOST":  "apiHost",
		"IAM_HOST":  "iamHost",
		"RPC_HOST":  "rpcHost",
		"ID_HOST":   "idHost",
		"CHAIN_ID":  "chainId",
		"ENV":       "env",
		"FEATURE_X": "featureX",
	}
	for in, want := range cases {
		if got := toCamel(in); got != want {
			t.Errorf("toCamel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseValue(t *testing.T) {
	if parseValue("true") != true {
		t.Error("true should parse to bool true")
	}
	if parseValue("false") != false {
		t.Error("false should parse to bool false")
	}
	if parseValue("8675310") != int64(8675310) {
		t.Error("8675310 should parse to int64")
	}
	if parseValue("https://api.test.satschel.com") != "https://api.test.satschel.com" {
		t.Error("URL should stay string")
	}
	if parseValue("v1.2.3") != "v1.2.3" {
		t.Error("version string should stay string")
	}
}

func TestWriteRuntimeConfig(t *testing.T) {
	dir := t.TempDir()
	// Seed placeholder file; code should overwrite it when SPA_* is present.
	placeholder := filepath.Join(dir, "config.json")
	if err := os.WriteFile(placeholder, []byte(`{"v":1,"env":"__TEMPLATE__"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SPA_API_HOST", "https://api.test.satschel.com")
	t.Setenv("SPA_IAM_HOST", "https://iam.test.satschel.com")
	t.Setenv("SPA_CHAIN_ID", "8675310")
	t.Setenv("SPA_ENV", "test")

	if err := writeRuntimeConfig(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(placeholder)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}

	if got["apiHost"] != "https://api.test.satschel.com" {
		t.Errorf("apiHost = %v", got["apiHost"])
	}
	if got["iamHost"] != "https://iam.test.satschel.com" {
		t.Errorf("iamHost = %v", got["iamHost"])
	}
	// JSON decodes numbers as float64 unless told otherwise.
	if got["chainId"].(float64) != 8675310 {
		t.Errorf("chainId = %v", got["chainId"])
	}
	if got["env"] != "test" {
		t.Errorf("env = %v", got["env"])
	}
	if got["v"].(float64) != 1 {
		t.Errorf("v should be 1, got %v", got["v"])
	}
}

func TestWriteRuntimeConfigNoEnv(t *testing.T) {
	dir := t.TempDir()
	placeholder := filepath.Join(dir, "config.json")
	original := []byte(`{"v":1,"env":"__TEMPLATE__"}`)
	if err := os.WriteFile(placeholder, original, 0o644); err != nil {
		t.Fatal(err)
	}

	// Clear anything SPA_* that the harness might have set.
	for _, e := range os.Environ() {
		if len(e) > 4 && e[:4] == "SPA_" {
			name := e[:len(e)-len(e[4:])]
			_ = name
		}
	}

	if err := writeRuntimeConfig(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(placeholder)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(original) {
		t.Errorf("placeholder should be untouched when no SPA_* vars set\ngot: %s\nwant: %s", raw, original)
	}
}
