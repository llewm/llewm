// The optional helper speaks PeerDrop room signaling and exchanges broker data
// only over a dedicated WebRTC data channel. The broker never imports this module.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pion/webrtc/v4"
)

const channelLabel = "llewm-pairing-v1"
const maxPacket = 48 << 10

var roomCode = regexp.MustCompile(`^[0-9]{3}-[0-9]{3}-[0-9]{3}$`)

type signalDescription struct {
	Type string                     `json:"type"`
	SDP  *webrtc.SessionDescription `json:"sdp,omitempty"`
}
type roomEvent struct {
	Type    string            `json:"type"`
	From    string            `json:"from"`
	PeerID  string            `json:"peerId"`
	Count   int               `json:"peerCount"`
	Payload signalDescription `json:"payload"`
}

var providerHTTP = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected provider redirect") }}

func providerRequest(ctx context.Context, method, address string, body []byte, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := providerHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("PeerDrop returned HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return errors.New("oversized provider response")
	}
	return json.Unmarshal(raw, result)
}

// Browsers and PeerDrop allow either a single ICE URL or an array.
type iceURLs []string

func (urls *iceURLs) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil && one != "" {
		*urls = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil || len(many) == 0 {
		return errors.New("invalid ICE server URLs")
	}
	*urls = many
	return nil
}

type link struct {
	channel  *webrtc.DataChannel
	pc       *webrtc.PeerConnection
	incoming chan []byte
	ended    <-chan struct{}
}

func (l *link) receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.ended:
		return nil, errors.New("pairing connection closed")
	case data := <-l.incoming:
		return data, nil
	}
}

// Signal only until the first channel opens. Each attempt uses one peer and
// one connection. A disconnect requires a fresh pairing and comparison.
func connect(ctx context.Context, provider, room string, host bool, announce func(string)) (*link, error) {
	origin, err := url.Parse(provider)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || (origin.Path != "" && origin.Path != "/") || strings.ContainsAny(provider, "?#") {
		return nil, errors.New("provider must be an HTTPS origin")
	}
	base := "https://" + origin.Host
	if host {
		var created struct{ Code string }
		if err := providerRequest(ctx, "POST", base+"/session", []byte(`{"filesOnly":true}`), &created); err != nil {
			return nil, err
		}
		room = created.Code
	}
	if !roomCode.MatchString(room) {
		return nil, errors.New("invalid PeerDrop room code")
	}
	var ice struct {
		Servers []struct {
			URLs       iceURLs `json:"urls"`
			Username   string  `json:"username"`
			Credential string  `json:"credential"`
		} `json:"iceServers"`
	}
	if err := providerRequest(ctx, "GET", base+"/session/"+room+"/turn", nil, &ice); err != nil {
		return nil, err
	}
	var servers []webrtc.ICEServer
	for _, s := range ice.Servers {
		servers = append(servers, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	settings := webrtc.SettingEngine{}
	settings.SetSCTPMaxMessageSize(maxPacket)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			pc.Close()
		}
	}()
	session, stop := context.WithCancel(ctx)
	defer stop()
	ws, _, err := websocket.Dial(session, "wss://"+origin.Host+"/ws/"+room, &websocket.DialOptions{HTTPClient: providerHTTP})
	if err != nil {
		return nil, err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(128 << 10)
	if err := wsjson.Write(session, ws, map[string]string{"type": "join", "session": room}); err != nil {
		return nil, err
	}
	var joined roomEvent
	if err := wsjson.Read(session, ws, &joined); err != nil {
		return nil, err
	}
	wantCount := 2
	if host {
		wantCount = 1
	}
	if joined.Type != "joined" || joined.Count != wantCount {
		return nil, errors.New("expected an exclusive two-person PeerDrop room; start a fresh attempt")
	}
	if host {
		announce(room)
	}
	live, end := context.WithCancel(ctx)
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			end()
		}
	})
	ready := make(chan *webrtc.DataChannel, 1)
	incoming := make(chan []byte, 4)
	var attached atomic.Bool
	attach := func(dc *webrtc.DataChannel) {
		if dc.Label() != channelLabel || !attached.CompareAndSwap(false, true) {
			end()
			return
		}
		dc.OnClose(end)
		dc.OnOpen(func() {
			select {
			case ready <- dc:
			default:
				end()
			}
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if len(msg.Data) > maxPacket {
				end()
				return
			}
			select {
			case incoming <- msg.Data:
			default:
				end()
			}
		})
	}
	pc.OnDataChannel(attach)
	if host {
		dc, err := pc.CreateDataChannel(channelLabel, nil)
		if err != nil {
			return nil, err
		}
		attach(dc)
	}
	events := make(chan roomEvent)
	readErr := make(chan error, 1)
	go func() {
		for {
			var e roomEvent
			if err := wsjson.Read(session, ws, &e); err != nil {
				readErr <- err
				return
			}
			select {
			case events <- e:
			case <-session.Done():
				return
			}
		}
	}()
	var peer string
	negotiated := false
	for {
		select {
		case <-live.Done():
			return nil, errors.New("pairing expired or disconnected")
		case err := <-readErr:
			return nil, err
		case dc := <-ready:
			success = true
			return &link{channel: dc, pc: pc, incoming: incoming, ended: live.Done()}, nil
		case e := <-events:
			switch e.Type {
			case "peer-joined":
				if !host || peer != "" || e.PeerID == "" {
					return nil, errors.New("unexpected additional peer")
				}
				peer = e.PeerID
				offer, err := pc.CreateOffer(nil)
				if err != nil {
					return nil, err
				}
				if err := sendDescription(session, pc, ws, offer); err != nil {
					return nil, err
				}
			case "relay":
				if negotiated || e.From == "" || (peer != "" && peer != e.From) || e.Payload.SDP == nil {
					return nil, errors.New("unexpected signaling message")
				}
				expected := webrtc.SDPTypeOffer
				if host {
					expected = webrtc.SDPTypeAnswer
				}
				if e.Payload.Type != expected.String() || e.Payload.SDP.Type != expected {
					return nil, errors.New("unexpected session description")
				}
				peer = e.From
				negotiated = true
				if err := pc.SetRemoteDescription(*e.Payload.SDP); err != nil {
					return nil, err
				}
				if !host {
					answer, err := pc.CreateAnswer(nil)
					if err != nil {
						return nil, err
					}
					if err := sendDescription(session, pc, ws, answer); err != nil {
						return nil, err
					}
				}
			default:
				return nil, errors.New("PeerDrop room changed; start a fresh attempt")
			}
		}
	}
}

func sendDescription(ctx context.Context, pc *webrtc.PeerConnection, ws *websocket.Conn, sdp webrtc.SessionDescription) error {
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(sdp); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gathered:
	}
	return wsjson.Write(ctx, ws, signalDescription{Type: sdp.Type.String(), SDP: pc.LocalDescription()})
}
