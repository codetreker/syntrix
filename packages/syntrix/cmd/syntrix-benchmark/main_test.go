package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestPrintUsage(t *testing.T) {
	// Capture stdout
	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printUsage()

	w.Close()
	os.Stdout = oldStdout
	io.Copy(&buf, r)
	output := buf.String()

	// Verify output contains expected strings
	expectedStrings := []string{
		"Syntrix Benchmark Tool",
		"Usage:",
		"Commands:",
		"run",
		"version",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(output, expected) {
			t.Errorf("Expected output to contain %q, but it didn't", expected)
		}
	}
}

func TestVersion(t *testing.T) {
	if Version == "" {
		t.Error("Version should not be empty")
	}

	// Version should be "dev" by default
	if Version != "dev" {
		t.Logf("Version is %s (expected 'dev' for development builds)", Version)
	}
}
