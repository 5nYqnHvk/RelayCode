package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestLoadConfigGeneratesDefaultRelaycodeYAMLAndContinues(t *testing.T) {
	t.Chdir(t.TempDir())
	var out bytes.Buffer

	cfg, err := loadConfig("relaycode.yaml", strings.NewReader("continue\n"), &out, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Host != "127.0.0.1" || len(cfg.Routes) == 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if _, err := os.Stat("relaycode.yaml"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Config file not found") || !strings.Contains(out.String(), "Continue now?") {
		t.Fatalf("prompt = %q", out.String())
	}
}

func TestLoadConfigGeneratesDefaultRelaycodeYAMLAndExits(t *testing.T) {
	t.Chdir(t.TempDir())
	var out bytes.Buffer

	_, err := loadConfig("relaycode.yaml", strings.NewReader("exit\n"), &out, true)
	if !errors.Is(err, errConfigGeneratedExit) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat("relaycode.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigGeneratesDefaultRelaycodeYAMLAndExitsNonInteractive(t *testing.T) {
	t.Chdir(t.TempDir())
	var out bytes.Buffer

	_, err := loadConfig("relaycode.yaml", strings.NewReader("continue\n"), &out, false)
	if !errors.Is(err, errConfigGeneratedExit) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat("relaycode.yaml"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Non-interactive stdin detected") {
		t.Fatalf("prompt = %q", out.String())
	}
}

func TestLoadConfigDoesNotGenerateCustomMissingConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := loadConfig("custom.yaml", strings.NewReader(""), &bytes.Buffer{}, true)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat("custom.yaml"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom config created unexpectedly: %v", err)
	}
}
