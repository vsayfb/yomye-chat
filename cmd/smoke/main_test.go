package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsProductionBeforeReadingCredentials(t *testing.T) {
	t.Parallel()
	err := run([]string{
		"smoke", "run", "--run-id", "sha", "--target", "https://chat.example.com",
		"--environment", "production", "--credentials-stdin",
	}, strings.NewReader("must-not-be-read"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("run() error = %v, want production rejection", err)
	}
}

func TestRunRequiresCredentialsStdin(t *testing.T) {
	t.Parallel()
	err := run([]string{
		"smoke", "run", "--run-id", "sha", "--target", "http://localhost:8081",
		"--environment", "local",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--credentials-stdin is required") {
		t.Fatalf("run() error = %v", err)
	}
}
