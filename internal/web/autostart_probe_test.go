package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestIsWebServerRunningNeedsAnAnsweringServer is the regression test for the
// 18-hour blind spot.
//
// IsWebServerRunning used to be a TCP dial. A listening socket with a full
// accept queue still completes the handshake in the kernel, so the dial
// succeeded against a server that was wedged and could not accept at all —
// which is precisely the state the web frontend ended up in while logging
// "accept4: too many open files" every second. The minute-cron asked every
// minute and was told everything was fine.
//
// These tests pin down the difference: a socket that answers HTTP counts as
// running, a socket that only completes the handshake does not.
func TestIsWebServerRunningNeedsAnAnsweringServer(t *testing.T) {
	t.Run("healthy server counts as running", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
		defer srv.Close()

		addr := srv.Listener.Addr().String()
		if !IsWebServerRunning(addr) {
			t.Error("a server answering 200 must be reported as running")
		}
	})

	t.Run("socket that never answers is NOT running", func(t *testing.T) {
		// Accepts the TCP connection and then says nothing at all — the
		// shape of a server stuck with a full accept queue. The old dial-based
		// check called this healthy; it must not.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer ln.Close()

		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				// Hold the connection open, never write a response.
				go func(c net.Conn) {
					buf := make([]byte, 256)
					for {
						if _, err := c.Read(buf); err != nil {
							return
						}
					}
				}(c)
			}
		}()

		if IsWebServerRunning(ln.Addr().String()) {
			t.Error("a socket that completes the handshake but never answers must " +
				"NOT be reported as running — that blindness is what kept the " +
				"console down for 18 hours")
		}
	})

	t.Run("nothing listening is not running", func(t *testing.T) {
		// Bind then immediately close, so the port is almost certainly free.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := ln.Addr().String()
		ln.Close()

		if IsWebServerRunning(addr) {
			t.Error("a closed port must not be reported as running")
		}
	})

	t.Run("server erroring 500 is not running", func(t *testing.T) {
		// A 500 means the application is reachable but broken; for the
		// self-healing path we want that treated as unhealthy so it gets
		// replaced.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()

		if IsWebServerRunning(srv.Listener.Addr().String()) {
			t.Error("a 500 response must not be reported as running")
		}
	})

	t.Run("probe is bounded in time", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer ln.Close()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
			}
		}()

		start := time.Now()
		IsWebServerRunning(ln.Addr().String())
		// The old check answered instantly (dial only). The new one must give
		// up on its own rather than hang the caller.
		if elapsed := time.Since(start); elapsed > 20*time.Second {
			t.Errorf("probe took %v, want a bounded timeout", elapsed)
		}
	})
}
