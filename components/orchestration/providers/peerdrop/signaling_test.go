package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Exercise both actual WebRTC clients against a two-person signaling fixture.
// The fixture handles only room setup and SDP, never comparison or application data.
func TestNativeChannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hosts := make(chan *websocket.Conn, 1)
	guests := make(chan *websocket.Conn, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"code":"123-456-789"}`)) })
	mux.HandleFunc("GET /session/123-456-789/turn", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"iceServers":[]}`)) })
	var joined atomic.Int32
	// The caller launches the guest only after the host announces joined.
	mux.HandleFunc("/ws/123-456-789", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		var join map[string]string
		if wsjson.Read(ctx, ws, &join) != nil || join["type"] != "join" {
			return
		}
		if joined.Add(1) == 1 {
			if wsjson.Write(ctx, ws, map[string]any{"type": "joined", "peerId": "host", "peerCount": 1}) != nil {
				return
			}
			hosts <- ws
			var guest *websocket.Conn
			select {
			case guest = <-guests:
			case <-ctx.Done():
				return
			}
			if wsjson.Write(ctx, ws, map[string]string{"type": "peer-joined", "peerId": "guest"}) != nil {
				return
			}
			var offer signalDescription
			if wsjson.Read(ctx, ws, &offer) != nil {
				return
			}
			if wsjson.Write(ctx, guest, roomEvent{Type: "relay", From: "host", Payload: offer}) != nil {
				return
			}
		} else {
			var host *websocket.Conn
			select {
			case host = <-hosts:
			case <-ctx.Done():
				return
			}
			if wsjson.Write(ctx, ws, map[string]any{"type": "joined", "peerId": "guest", "peerCount": 2}) != nil {
				return
			}
			guests <- ws
			var answer signalDescription
			if wsjson.Read(ctx, ws, &answer) != nil {
				return
			}
			if wsjson.Write(ctx, host, roomEvent{Type: "relay", From: "guest", Payload: answer}) != nil {
				return
			}
		}
		<-ctx.Done()
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	defer cancel()
	previous := providerHTTP
	providerHTTP = server.Client()
	defer func() { providerHTTP = previous }()
	type result struct {
		l   *link
		err error
	}
	hostResult := make(chan result, 1)
	room := make(chan string, 1)
	go func() {
		l, err := connect(ctx, server.URL, "", true, func(code string) { room <- code })
		hostResult <- result{l, err}
	}()
	var code string
	select {
	case code = <-room:
	case result := <-hostResult:
		t.Fatal(result.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	guest, err := connect(ctx, server.URL, code, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.pc.Close()
	host := <-hostResult
	if host.err != nil {
		t.Fatal(host.err)
	}
	defer host.l.pc.Close()
	codes := make(chan string, 1)
	go func() {
		code, err := comparison(ctx, host.l, true)
		if err != nil {
			code = ""
		}
		codes <- code
	}()
	checked, err := comparison(ctx, guest, false)
	if err != nil || checked != <-codes {
		t.Fatal("DTLS channel comparison failed", err)
	}
}
