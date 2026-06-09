package agora

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openziti/agora/sdk/agent/tunnel"
)

func serveEnabled(cfg *Config) {
	cfg.Serve = &ServeConfig{Enabled: true}
}

func TestServeBindsExistingTunnel(t *testing.T) {
	sub, ops := newTestSubsystem(t, serveEnabled)
	ops.tunnels["engineering"] = "tcp" // operator-provisioned, pre-existing

	sv, err := sub.Serve(context.Background())
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if sv.Managed() {
		t.Fatal("expected bind (managed=false) for a pre-existing tunnel")
	}
	if len(ops.created) != 0 {
		t.Fatalf("Serve must not create when binding: %#v", ops.created)
	}
	if sv.Listener() == nil {
		t.Fatal("expected a bound listener")
	}

	if err := sv.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if len(ops.deleted) != 0 {
		t.Fatalf("bound tunnel must not be deleted on close: %#v", ops.deleted)
	}
}

func TestServeCreatesWhenAbsent(t *testing.T) {
	sub, ops := newTestSubsystem(t, func(cfg *Config) {
		cfg.Serve = &ServeConfig{Enabled: true, Grants: []string{"alice@example.com"}}
	})

	sv, err := sub.Serve(context.Background())
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if !sv.Managed() {
		t.Fatal("expected managed=true for a created tunnel")
	}
	if len(ops.created) != 1 {
		t.Fatalf("created specs = %#v", ops.created)
	}
	if ops.created[0].Name != "engineering" || ops.created[0].Mode != tunnel.ModeTCP {
		t.Fatalf("unexpected create spec: %#v", ops.created[0])
	}
	if len(ops.created[0].GrantEmails) != 1 || ops.created[0].GrantEmails[0] != "alice@example.com" {
		t.Fatalf("grants did not ride the create path: %#v", ops.created[0].GrantEmails)
	}

	if err := sv.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if len(ops.deleted) != 1 || ops.deleted[0] != "engineering" {
		t.Fatalf("created tunnel must be deleted on close: %#v", ops.deleted)
	}
}

func TestServeWrongModeBindIsError(t *testing.T) {
	sub, ops := newTestSubsystem(t, serveEnabled)
	ops.tunnels["engineering"] = "http" // wrong mode

	sv, err := sub.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "expected tcp") {
		t.Fatalf("expected wrong-mode error, got %v", err)
	}
	if sv != nil {
		t.Fatal("expected nil serve on wrong-mode bind")
	}
	if len(ops.listeners) != 1 || !ops.listeners[0].closed {
		t.Fatal("listener opened on the bind path must be closed on wrong-mode error")
	}
	if len(ops.deleted) != 0 {
		t.Fatalf("bound tunnel must be left intact: %#v", ops.deleted)
	}
}

func TestServeCreateThenListenFailsUnwinds(t *testing.T) {
	sub, ops := newTestSubsystem(t, serveEnabled)
	// first Listen (absent) returns ErrNotFound, create succeeds, second Listen fails.
	ops.listenErr = errors.New("listen boom")
	ops.listenErrAfter = 1

	sv, err := sub.Serve(context.Background())
	if err == nil {
		t.Fatal("expected serve error")
	}
	if sv != nil {
		t.Fatal("expected nil serve on failure")
	}
	if len(ops.created) != 1 {
		t.Fatalf("expected one create attempt: %#v", ops.created)
	}
	if len(ops.deleted) != 1 || ops.deleted[0] != "engineering" {
		t.Fatalf("created tunnel must be deleted when the second listen fails: %#v", ops.deleted)
	}
}

func TestServeCreateConflictBinds(t *testing.T) {
	sub, ops := newTestSubsystem(t, serveEnabled)
	ops.createConflict = true // a racing process provisions the tunnel first

	sv, err := sub.Serve(context.Background())
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if sv.Managed() {
		t.Fatal("a lost create race should bind (managed=false), not manage")
	}
	if len(ops.created) != 1 {
		t.Fatalf("expected exactly one create attempt: %#v", ops.created)
	}

	if err := sv.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if len(ops.deleted) != 0 {
		t.Fatalf("bound tunnel must not be deleted: %#v", ops.deleted)
	}
}
