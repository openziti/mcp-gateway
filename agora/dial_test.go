package agora

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/openziti/agora/sdk/agent/tunnel"
)

func TestDialerAttachOncePerName(t *testing.T) {
	sub, ops := newTestSubsystem(t, nil)
	d := sub.Dialer()

	if err := d.Attach(context.Background(), "relay"); err != nil {
		t.Fatalf("first Attach returned error: %v", err)
	}
	if err := d.Attach(context.Background(), "relay"); err != nil {
		t.Fatalf("second Attach returned error: %v", err)
	}
	if len(ops.attached) != 1 {
		t.Fatalf("attach must be idempotent per name: %#v", ops.attached)
	}
	if client, err := d.HTTPClient("relay"); err != nil || client == nil {
		t.Fatalf("HTTPClient = %v, %v", client, err)
	}
}

func TestDialerHTTPClientUnknownIsError(t *testing.T) {
	sub, _ := newTestSubsystem(t, nil)
	if _, err := sub.Dialer().HTTPClient("never-attached"); err == nil {
		t.Fatal("expected error for a never-attached tunnel")
	}
}

func TestDialerHTTPClientPerformsNoAttach(t *testing.T) {
	sub, ops := newTestSubsystem(t, nil)
	d := sub.Dialer()
	if err := d.Attach(context.Background(), "relay"); err != nil {
		t.Fatalf("Attach returned error: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := d.HTTPClient("relay"); err != nil {
			t.Fatalf("HTTPClient returned error: %v", err)
		}
	}
	if len(ops.attached) != 1 {
		t.Fatalf("HTTPClient must not attach or re-bump: %#v", ops.attached)
	}
}

func TestDialerCloseDetachesEachOnce(t *testing.T) {
	sub, ops := newTestSubsystem(t, nil)
	d := sub.Dialer()
	for _, name := range []string{"a", "b"} {
		if err := d.Attach(context.Background(), name); err != nil {
			t.Fatalf("Attach(%q) returned error: %v", name, err)
		}
	}

	if err := d.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	got := append([]string(nil), ops.detached...)
	sort.Strings(got)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("detached = %#v, want each of a,b once", ops.detached)
	}
	if _, err := d.HTTPClient("a"); err == nil {
		t.Fatal("HTTPClient should error after Close")
	}
}

func TestDialerAttachConflictSurfaces(t *testing.T) {
	sub, ops := newTestSubsystem(t, nil)
	ops.attachErr = fmt.Errorf("ambiguous tunnel: %w", tunnel.ErrConflict)

	if err := sub.Dialer().Attach(context.Background(), "relay"); err == nil {
		t.Fatal("expected a real ConnectTunnelConflict to surface as an error")
	}
}

func TestDialerDialContextRoutesThroughDial(t *testing.T) {
	sub, ops := newTestSubsystem(t, nil)
	d := sub.Dialer()
	if err := d.Attach(context.Background(), "relay"); err != nil {
		t.Fatalf("Attach returned error: %v", err)
	}
	client, err := d.HTTPClient("relay")
	if err != nil {
		t.Fatalf("HTTPClient returned error: %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil {
		t.Fatal("expected an http.Transport with a DialContext")
	}
	// The dialer ignores addr and routes through tunnel.Dial by name.
	_, _ = transport.DialContext(context.Background(), "tcp", "ignored:1234")
	if len(ops.dialed) != 1 || ops.dialed[0] != "relay" {
		t.Fatalf("DialContext must dial the attached tunnel: %#v", ops.dialed)
	}
}
