package main

import (
	"bytes"
	"testing"

	"github.com/openziti/mcp-gateway/build"
)

func TestVersionCommandPrintsBuildString(t *testing.T) {
	var out bytes.Buffer
	cmd := newVersionCommand()
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("version command returned error: %v", err)
	}
	if got, want := out.String(), build.String()+"\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}
