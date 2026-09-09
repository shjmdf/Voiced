package media

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

type peerKey struct {
	roomID        string
	participantID string
}

// Manager is a small audio SFU. Each browser sends one Opus/RTP audio track
// to the server; the manager binds that track to every other browser in the
// room. It never decodes or mixes the audio.
type Manager struct {
	api           *webrtc.API
	configuration webrtc.Configuration
	config        Config
	signal        SignalSink
	isMuted       MutedSource

	mu           sync.Mutex
	peers        map[peerKey]*peer
	publications map[string]map[string]*publication
}

type peer struct {
	roomID        string
	participantID string
	pc            *webrtc.PeerConnection

	mu          sync.Mutex
	closed      bool
	negotiating bool
	pending     bool
	senders     map[string]*webrtc.RTPSender
}

type publication struct {
	participantID string
	track         *webrtc.TrackLocalStaticRTP
	muted         atomic.Bool
}

func NewManager(config Config, signal SignalSink, isMuted MutedSource) (*Manager, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, fmt.Errorf("register WebRTC codecs: %w", err)
	}

	interceptors := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, interceptors); err != nil {
		return nil, fmt.Errorf("register WebRTC interceptors: %w", err)
	}

	settingEngine := webrtc.SettingEngine{}
	if config.UDPPortMin != 0 {
		if err := settingEngine.SetEphemeralUDPPortRange(config.UDPPortMin, config.UDPPortMax); err != nil {
			return nil, fmt.Errorf("set UDP port range: %w", err)
		}
	}

	return &Manager{
		api: webrtc.NewAPI(
			webrtc.WithMediaEngine(mediaEngine),
			webrtc.WithInterceptorRegistry(interceptors),
			webrtc.WithSettingEngine(settingEngine),
		),
		configuration: webrtc.Configuration{ICEServers: config.ICEServers},
		config:        config,
		signal:        signal,
		isMuted:       isMuted,
		peers:         make(map[peerKey]*peer),
		publications:  make(map[string]map[string]*publication),
	}, nil
}

// HandleOffer creates (or replaces) this participant's WebRTC peer
// connection and produces the answer for the browser's offer.
func (m *Manager) HandleOffer(roomID string, participantID string, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if offer.Type != webrtc.SDPTypeOffer {
		return webrtc.SessionDescription{}, errors.New("expected an SDP offer")
	}

	m.ClosePeer(roomID, participantID)

	connection, err := m.api.NewPeerConnection(m.configuration)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("create peer connection: %w", err)
	}

	p := &peer{
		roomID:        roomID,
		participantID: participantID,
		pc:            connection,
		senders:       make(map[string]*webrtc.RTPSender),
	}

	connection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		m.emit(Signal{
			RoomID:        roomID,
			ParticipantID: participantID,
			Type:          "ice_candidate",
			Payload:       candidate.ToJSON(),
		})
	})

	connection.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		m.publish(roomID, participantID, track)
	})

	connection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			m.closeIfCurrent(p)
		}
	})

	m.mu.Lock()
	m.peers[peerKey{roomID: roomID, participantID: participantID}] = p
	m.mu.Unlock()

	// Adding existing outgoing tracks before creating the answer includes them
	// in this first negotiation for a participant joining an active room.
	for _, existing := range m.roomPublications(roomID) {
		if existing.participantID != participantID {
			m.addPublicationToPeer(p, existing)
		}
	}

	if err := connection.SetRemoteDescription(offer); err != nil {
		m.ClosePeer(roomID, participantID)
		return webrtc.SessionDescription{}, fmt.Errorf("set remote offer: %w", err)
	}

	answer, err := connection.CreateAnswer(nil)
	if err != nil {
		m.ClosePeer(roomID, participantID)
		return webrtc.SessionDescription{}, fmt.Errorf("create answer: %w", err)
	}
	if err := connection.SetLocalDescription(answer); err != nil {
		m.ClosePeer(roomID, participantID)
		return webrtc.SessionDescription{}, fmt.Errorf("set local answer: %w", err)
	}

	return answer, nil
}

// HandleAnswer completes a server-initiated renegotiation after a new audio
// publisher has joined or stopped sending audio.
func (m *Manager) HandleAnswer(roomID string, participantID string, answer webrtc.SessionDescription) error {
	if answer.Type != webrtc.SDPTypeAnswer {
		return errors.New("expected an SDP answer")
	}

	p, ok := m.findPeer(roomID, participantID)
	if !ok {
		return errors.New("no active WebRTC peer connection")
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("WebRTC peer connection is closed")
	}
	err := p.pc.SetRemoteDescription(answer)
	p.negotiating = false
	pending := p.pending
	p.pending = false
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("set remote answer: %w", err)
	}
	if pending {
		go m.negotiate(p)
	}
	return nil
}

// AddICECandidate adds a browser-discovered candidate to its current peer
// connection. The browser buffers server candidates until it has an SDP.
func (m *Manager) AddICECandidate(roomID string, participantID string, candidate webrtc.ICECandidateInit) error {
	p, ok := m.findPeer(roomID, participantID)
	if !ok {
		return errors.New("no active WebRTC peer connection")
	}
	if err := p.pc.AddICECandidate(candidate); err != nil {
		return fmt.Errorf("add ICE candidate: %w", err)
	}
	return nil
}

// SetMuted controls only forwarding. The owner command remains persisted in
// room.Manager, while this method stops or resumes RTP delivery immediately.
func (m *Manager) SetMuted(roomID string, participantID string, muted bool) {
	m.mu.Lock()
	publisher := m.publications[roomID][participantID]
	m.mu.Unlock()
	if publisher != nil {
		publisher.muted.Store(muted)
	}
}

// ClosePeer releases one browser's transport and removes its publication from
// all listeners. It is called when the WebSocket member leaves as well as
// when that browser replaces its microphone connection.
func (m *Manager) ClosePeer(roomID string, participantID string) {
	key := peerKey{roomID: roomID, participantID: participantID}
	m.mu.Lock()
	p := m.peers[key]
	delete(m.peers, key)
	m.mu.Unlock()
	if p != nil {
		p.close()
	}
	m.removePublication(roomID, participantID, nil)
}

// CloseRoom closes all UDP/DTLS transports for a dissolved or empty room.
func (m *Manager) CloseRoom(roomID string) {
	m.mu.Lock()
	peers := make([]*peer, 0)
	for key, p := range m.peers {
		if key.roomID == roomID {
			peers = append(peers, p)
			delete(m.peers, key)
		}
	}
	delete(m.publications, roomID)
	m.mu.Unlock()

	for _, p := range peers {
		p.close()
	}
}

func (m *Manager) PublicConfig() PublicConfig {
	servers := make([]PublicICEServer, 0, len(m.config.ICEServers))
	for _, server := range m.config.ICEServers {
		servers = append(servers, PublicICEServer{URLs: append([]string(nil), server.URLs...)})
	}
	return PublicConfig{ICEServers: servers}
}

func (m *Manager) DebugInfo() DebugInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	publicationCount := 0
	for _, roomPublications := range m.publications {
		publicationCount += len(roomPublications)
	}
	return DebugInfo{
		UDPPortMin:       m.config.UDPPortMin,
		UDPPortMax:       m.config.UDPPortMax,
		PeerCount:        len(m.peers),
		PublicationCount: publicationCount,
	}
}

func (m *Manager) publish(roomID string, participantID string, remote *webrtc.TrackRemote) {
	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		remote.Codec().RTPCodecCapability,
		"audio-"+participantID,
		participantID,
	)
	if err != nil {
		return
	}
	publisher := &publication{
		participantID: participantID,
		track:         localTrack,
	}
	if m.isMuted != nil {
		publisher.muted.Store(m.isMuted(roomID, participantID))
	}

	m.mu.Lock()
	if m.peers[peerKey{roomID: roomID, participantID: participantID}] == nil {
		// The WebSocket member left while Pion was delivering OnTrack.
		m.mu.Unlock()
		return
	}
	roomPublications, exists := m.publications[roomID]
	if !exists {
		roomPublications = make(map[string]*publication)
		m.publications[roomID] = roomPublications
	}
	previous := roomPublications[participantID]
	roomPublications[participantID] = publisher
	peers := m.peersInRoomLocked(roomID, participantID)
	m.mu.Unlock()

	if previous != nil {
		m.removePublicationFromPeers(roomID, participantID, previous)
	}
	for _, listener := range peers {
		if m.addPublicationToPeer(listener, publisher) {
			go m.negotiate(listener)
		}
	}

	for {
		packet, _, err := remote.ReadRTP()
		if err != nil {
			m.removePublication(roomID, participantID, publisher)
			return
		}
		if !publisher.muted.Load() {
			_ = publisher.track.WriteRTP(packet)
		}
	}
}

func (m *Manager) removePublication(roomID string, participantID string, only *publication) {
	m.mu.Lock()
	roomPublications := m.publications[roomID]
	publisher := roomPublications[participantID]
	if publisher == nil || (only != nil && publisher != only) {
		m.mu.Unlock()
		return
	}
	delete(roomPublications, participantID)
	if len(roomPublications) == 0 {
		delete(m.publications, roomID)
	}
	peers := m.peersInRoomLocked(roomID, participantID)
	m.mu.Unlock()

	for _, listener := range peers {
		if listener.removePublication(participantID) {
			go m.negotiate(listener)
		}
	}
}

func (m *Manager) removePublicationFromPeers(roomID string, participantID string, _ *publication) {
	m.mu.Lock()
	peers := m.peersInRoomLocked(roomID, participantID)
	m.mu.Unlock()
	for _, listener := range peers {
		if listener.removePublication(participantID) {
			go m.negotiate(listener)
		}
	}
}

func (m *Manager) roomPublications(roomID string) []*publication {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]*publication, 0, len(m.publications[roomID]))
	for _, publisher := range m.publications[roomID] {
		result = append(result, publisher)
	}
	return result
}

func (m *Manager) peersInRoomLocked(roomID string, excludeParticipantID string) []*peer {
	result := make([]*peer, 0)
	for key, p := range m.peers {
		if key.roomID == roomID && key.participantID != excludeParticipantID {
			result = append(result, p)
		}
	}
	return result
}

func (m *Manager) addPublicationToPeer(p *peer, publisher *publication) bool {
	if p.participantID == publisher.participantID {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.senders[publisher.participantID] != nil {
		return false
	}

	sender, err := p.pc.AddTrack(publisher.track)
	if err != nil {
		return false
	}
	p.senders[publisher.participantID] = sender
	go drainRTCP(sender)
	return true
}

func (p *peer) removePublication(participantID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	sender := p.senders[participantID]
	if sender == nil {
		return false
	}
	if err := p.pc.RemoveTrack(sender); err != nil {
		return false
	}
	delete(p.senders, participantID)
	return true
}

func (m *Manager) negotiate(p *peer) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if p.negotiating || p.pc.SignalingState() != webrtc.SignalingStateStable {
		p.pending = true
		p.mu.Unlock()
		return
	}
	p.negotiating = true
	offer, err := p.pc.CreateOffer(nil)
	if err == nil {
		err = p.pc.SetLocalDescription(offer)
	}
	if err != nil {
		p.negotiating = false
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	m.emit(Signal{
		RoomID:        p.roomID,
		ParticipantID: p.participantID,
		Type:          "offer",
		Payload:       offer,
	})
}

func (m *Manager) closeIfCurrent(p *peer) {
	key := peerKey{roomID: p.roomID, participantID: p.participantID}
	m.mu.Lock()
	if m.peers[key] != p {
		m.mu.Unlock()
		return
	}
	delete(m.peers, key)
	m.mu.Unlock()
	p.close()
	m.removePublication(p.roomID, p.participantID, nil)
}

func (m *Manager) findPeer(roomID string, participantID string) (*peer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, exists := m.peers[peerKey{roomID: roomID, participantID: participantID}]
	return p, exists
}

func (p *peer) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	_ = p.pc.Close()
}

func drainRTCP(sender *webrtc.RTPSender) {
	for {
		if _, _, err := sender.ReadRTCP(); err != nil {
			return
		}
	}
}

func (m *Manager) emit(signal Signal) {
	if m.signal != nil {
		m.signal(signal)
	}
}
