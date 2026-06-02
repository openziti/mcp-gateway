package gateway

import (
	"errors"
	"net"
	"testing"
	"time"
)

type acceptResult struct {
	conn net.Conn
	err  error
}

func TestResilientListenerSwapUnblocksParkedAccept(t *testing.T) {
	oldInner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen old: %v", err)
	}
	defer oldInner.Close()

	listener := newResilientListener(oldInner)
	defer listener.Close()

	accepted := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		accepted <- acceptResult{conn: conn, err: err}
	}()

	time.Sleep(20 * time.Millisecond)

	newInner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen new: %v", err)
	}
	listener.Swap(newInner)

	client, err := net.Dial("tcp", newInner.Addr().String())
	if err != nil {
		t.Fatalf("dial new listener: %v", err)
	}
	defer client.Close()

	select {
	case result := <-accepted:
		if result.err != nil {
			t.Fatalf("accept returned error: %v", result.err)
		}
		result.conn.Close()
	case <-time.After(time.Second):
		t.Fatal("accept did not resume on swapped listener")
	}
}

func TestResilientListenerFailReturnsTerminalError(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listener := newResilientListener(inner)

	terminal := errors.New("terminal failure")
	listener.Fail(terminal)

	_, err = listener.Accept()
	if !errors.Is(err, terminal) {
		t.Fatalf("accept error = %v, want %v", err, terminal)
	}
}

func TestResilientListenerCloseUnblocksParkedAccept(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listener := newResilientListener(inner)

	accepted := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		accepted <- err
	}()

	time.Sleep(20 * time.Millisecond)
	if err := listener.Close(); err != nil {
		t.Fatalf("close returned error: %v", err)
	}

	select {
	case err := <-accepted:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("accept error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("accept did not unblock on close")
	}
}

type errorListener struct {
	err error
}

func (l errorListener) Accept() (net.Conn, error) {
	return nil, l.err
}

func (l errorListener) Close() error {
	return nil
}

func (l errorListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

func TestResilientListenerPropagatesUnswappedAcceptError(t *testing.T) {
	want := errors.New("inner failed")
	listener := newResilientListener(errorListener{err: want})

	_, err := listener.Accept()
	if !errors.Is(err, want) {
		t.Fatalf("accept error = %v, want %v", err, want)
	}
}
