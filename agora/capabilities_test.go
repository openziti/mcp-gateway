package agora

import "testing"

func TestDeriveCapabilitiesDedupesInOrder(t *testing.T) {
	got := Derive(
		[]string{"mcp-tools", " filesystem ", "mcp-tools"},
		[]string{"agora-serve", "", "filesystem", "github"},
	)
	want := []string{"mcp-tools", "filesystem", "agora-serve", "github"}

	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("capabilities = %#v, want %#v", got, want)
		}
	}
}
