package main

import (
	"os"
	"testing"
)

// TestRegistrationConfiguration проверяет безопасное значение по умолчанию и приоритет настроек.
func TestRegistrationConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EXAMINER_ALLOW_REGISTRATION", "")
	if err := os.WriteFile("examiner.yaml", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if viperForCmd(serveCmd()).GetBool("allow-registration") {
		t.Fatal("registration enabled by default")
	}
	if err := os.WriteFile("examiner.yaml", []byte("allow-registration: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !viperForCmd(serveCmd()).GetBool("allow-registration") {
		t.Fatal("YAML setting was ignored")
	}
	t.Setenv("EXAMINER_ALLOW_REGISTRATION", "false")
	if viperForCmd(serveCmd()).GetBool("allow-registration") {
		t.Fatal("environment did not override YAML")
	}
	cmd := rootCmd()
	if err := cmd.ParseFlags([]string{"--allow-registration"}); err != nil {
		t.Fatal(err)
	}
	if !viperForCmd(cmd).GetBool("allow-registration") {
		t.Fatal("flag did not override environment")
	}
}
