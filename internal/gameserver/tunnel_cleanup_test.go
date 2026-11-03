package gameserver

import (
	"net"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/tunnel"
)

func cleanupTestTunnel(t *testing.T, psk string) (*tunnel.Conn, *tunnel.Conn) {
	t.Helper()
	serverNet, peerNet := net.Pipe()
	accepted := make(chan *tunnel.Conn, 1)
	errors := make(chan error, 1)
	go func() {
		connection, err := tunnel.HandshakeServer(serverNet, psk)
		if err != nil {
			errors <- err
			return
		}
		accepted <- connection
	}()
	peer, err := tunnel.HandshakeClient(peerNet, psk)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case connection := <-accepted:
		t.Cleanup(func() { _ = connection.Close(); _ = peer.Close() })
		return connection, peer
	case err := <-errors:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("tunnel handshake timeout")
	}
	return nil, nil
}

func TestTunnelDisconnectClosesOnlyOwnedSessions(t *testing.T) {
	srv := NewServer(config.Game{}, logging.Discard(), nil)
	first, firstPeer := cleanupTestTunnel(t, "isolated-test-tunnel")
	second, _ := cleanupTestTunnel(t, "isolated-test-tunnel")
	owned := newSession(srv, 101, "local|one|7357|test", 7357, first)
	unrelated := newSession(srv, 102, "local|two|7358|test", 7358, second)
	srv.sessions[owned.ID], srv.sessions[unrelated.ID] = owned, unrelated
	srv.byToken[owned.Token], srv.byToken[unrelated.Token] = owned, unrelated
	gsSessionsActive.Add(2)
	t.Cleanup(func() { unrelated.Close("test_finished") })
	done := make(chan struct{})
	go func() {
		srv.handleTunnel(first)
		close(done)
	}()
	_ = firstPeer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnected tunnel did not exit")
	}
	if !owned.Closed() {
		t.Fatal("disconnected tunnel left its session alive")
	}
	select {
	case <-owned.Done():
	default:
		t.Fatal("session keepalive was not stopped")
	}
	if unrelated.Closed() || srv.byID(unrelated.ID) != unrelated || srv.SessionCount() != 1 {
		t.Fatal("disconnect changed an unrelated tunnel session")
	}
	if srv.byID(owned.ID) != nil {
		t.Fatal("disconnected session remains registered")
	}
}
