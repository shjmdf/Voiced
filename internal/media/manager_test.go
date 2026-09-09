package media

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func TestHandleOfferReturnsUsableAnswer(t *testing.T) {
	manager, err := NewManager(Config{}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	t.Cleanup(func() { manager.CloseRoom("room-1") })

	browser, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection() error = %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })
	if _, err := browser.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatalf("AddTransceiverFromKind() error = %v", err)
	}

	offer, err := browser.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer() error = %v", err)
	}
	if err := browser.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription(offer) error = %v", err)
	}

	answer, err := manager.HandleOffer("room-1", "member-1", offer)
	if err != nil {
		t.Fatalf("HandleOffer() error = %v", err)
	}
	if answer.Type != webrtc.SDPTypeAnswer {
		t.Fatalf("answer type = %s, want answer", answer.Type)
	}
	if err := browser.SetRemoteDescription(answer); err != nil {
		t.Fatalf("SetRemoteDescription(answer) error = %v", err)
	}
}

func TestManagerReportsConfiguredPorts(t *testing.T) {
	manager, err := NewManager(Config{
		UDPPortMin: 40000,
		UDPPortMax: 40010,
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:example.invalid:3478"}}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	info := manager.DebugInfo()
	if info.UDPPortMin != 40000 || info.UDPPortMax != 40010 {
		t.Fatalf("DebugInfo() UDP range = %d-%d, want 40000-40010", info.UDPPortMin, info.UDPPortMax)
	}
	config := manager.PublicConfig()
	if len(config.ICEServers) != 1 || config.ICEServers[0].URLs[0] != "stun:example.invalid:3478" {
		t.Fatalf("PublicConfig() = %#v, want configured STUN URL", config)
	}
}

func TestManagerForwardsAudioToAnotherParticipant(t *testing.T) {
	const roomID = "room-1"
	var manager *Manager
	var browsersMu sync.Mutex
	browsers := make(map[string]*testBrowser)
	signalErrors := make(chan error, 8)

	var err error
	manager, err = NewManager(Config{}, func(signal Signal) {
		browsersMu.Lock()
		browser := browsers[signal.ParticipantID]
		browsersMu.Unlock()
		if browser == nil {
			return
		}

		switch signal.Type {
		case "ice_candidate":
			candidate, ok := signal.Payload.(webrtc.ICECandidateInit)
			if !ok {
				signalErrors <- fmt.Errorf("server candidate has type %T", signal.Payload)
				return
			}
			browser.addServerCandidate(candidate, signalErrors)
		case "offer":
			offer, ok := signal.Payload.(webrtc.SessionDescription)
			if !ok {
				signalErrors <- fmt.Errorf("server offer has type %T", signal.Payload)
				return
			}
			go browser.answerServerOffer(manager, roomID, offer, signalErrors)
		}
	}, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	t.Cleanup(func() { manager.CloseRoom(roomID) })

	listener := newTestBrowser(t, manager, roomID, "listener")
	listener.trackReceived = make(chan struct{})
	listener.pc.OnTrack(func(_ *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case <-listener.trackReceived:
		default:
			close(listener.trackReceived)
		}
	})
	browsersMu.Lock()
	browsers["listener"] = listener
	browsersMu.Unlock()
	if _, err := listener.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	}); err != nil {
		t.Fatalf("listener AddTransceiverFromKind() error = %v", err)
	}
	negotiateInitial(t, manager, roomID, "listener", listener)

	source := newTestBrowser(t, manager, roomID, "source")
	browsersMu.Lock()
	browsers["source"] = source
	browsersMu.Unlock()
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"source-audio",
		"source",
	)
	if err != nil {
		t.Fatalf("NewTrackLocalStaticSample() error = %v", err)
	}
	if _, err := source.pc.AddTrack(track); err != nil {
		t.Fatalf("source AddTrack() error = %v", err)
	}
	negotiateInitial(t, manager, roomID, "source", source)

	for index := 0; index < 20; index++ {
		if err := track.WriteSample(media.Sample{Data: []byte{0xf8, 0xff, 0xfe}, Duration: 20 * time.Millisecond}); err != nil {
			t.Fatalf("WriteSample() error = %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case <-listener.trackReceived:
	case err := <-signalErrors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not receive the forwarded audio track")
	}
}

type testBrowser struct {
	pc *webrtc.PeerConnection

	mu                     sync.Mutex
	serverReady            bool
	remoteDescriptionIsSet bool
	browserCandidates      []webrtc.ICECandidateInit
	serverCandidates       []webrtc.ICECandidateInit
	serverOfferMu          sync.Mutex
	trackReceived          chan struct{}
	manager                *Manager
	roomID, participantID  string
}

func newTestBrowser(t *testing.T, manager *Manager, roomID string, participantID string) *testBrowser {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection() error = %v", err)
	}
	browser := &testBrowser{pc: pc, manager: manager, roomID: roomID, participantID: participantID}
	t.Cleanup(func() { _ = pc.Close() })
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		browser.addBrowserCandidate(candidate.ToJSON())
	})
	return browser
}

func negotiateInitial(t *testing.T, manager *Manager, roomID string, participantID string, browser *testBrowser) {
	t.Helper()
	offer, err := browser.pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer() error = %v", err)
	}
	if err := browser.pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription(offer) error = %v", err)
	}
	answer, err := manager.HandleOffer(roomID, participantID, offer)
	if err != nil {
		t.Fatalf("HandleOffer() error = %v", err)
	}
	if err := browser.pc.SetRemoteDescription(answer); err != nil {
		t.Fatalf("SetRemoteDescription(answer) error = %v", err)
	}
	browser.finishInitialNegotiation()
}

func (b *testBrowser) addBrowserCandidate(candidate webrtc.ICECandidateInit) {
	b.mu.Lock()
	if !b.serverReady {
		b.browserCandidates = append(b.browserCandidates, candidate)
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	_ = b.manager.AddICECandidate(b.roomID, b.participantID, candidate)
}

func (b *testBrowser) addServerCandidate(candidate webrtc.ICECandidateInit, errors chan<- error) {
	b.mu.Lock()
	if !b.remoteDescriptionIsSet {
		b.serverCandidates = append(b.serverCandidates, candidate)
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	if err := b.pc.AddICECandidate(candidate); err != nil {
		errors <- fmt.Errorf("browser AddICECandidate() error = %w", err)
	}
}

func (b *testBrowser) finishInitialNegotiation() {
	b.mu.Lock()
	b.remoteDescriptionIsSet = true
	b.serverReady = true
	serverCandidates := b.serverCandidates
	browserCandidates := b.browserCandidates
	b.serverCandidates = nil
	b.browserCandidates = nil
	b.mu.Unlock()

	for _, candidate := range serverCandidates {
		_ = b.pc.AddICECandidate(candidate)
	}
	for _, candidate := range browserCandidates {
		_ = b.manager.AddICECandidate(b.roomID, b.participantID, candidate)
	}
}

func (b *testBrowser) answerServerOffer(manager *Manager, roomID string, offer webrtc.SessionDescription, errors chan<- error) {
	b.serverOfferMu.Lock()
	defer b.serverOfferMu.Unlock()
	if err := b.pc.SetRemoteDescription(offer); err != nil {
		errors <- fmt.Errorf("SetRemoteDescription(server offer) error = %w", err)
		return
	}
	answer, err := b.pc.CreateAnswer(nil)
	if err == nil {
		err = b.pc.SetLocalDescription(answer)
	}
	if err == nil {
		err = manager.HandleAnswer(roomID, b.participantID, answer)
	}
	if err != nil {
		errors <- fmt.Errorf("answer server offer: %w", err)
	}
}
