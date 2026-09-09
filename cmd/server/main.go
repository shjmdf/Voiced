package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"

	"voiced/internal/access"
	"voiced/internal/api"
	"voiced/internal/media"
	"voiced/internal/room"
	"voiced/internal/signaling"
)

type serverConfig struct {
	listenAddress   string
	frontendOrigins []string
	accessTokenFile string
	mediaConfig     media.Config
	debugMedia      bool
}

func main() {
	config, err := loadServerConfig()
	if err != nil {
		log.Fatal(err)
	}

	rooms := room.NewManager()
	hub := signaling.NewHub()
	accessVerifier, err := access.NewFileVerifier(config.accessTokenFile)
	if err != nil {
		log.Fatal(err)
	}
	mediaManager, err := media.NewManager(config.mediaConfig, func(signal media.Signal) {
		hub.SendTo(signal.RoomID, signal.ParticipantID, signaling.NewMessage(signal.Type, signal.Payload))
	}, func(roomID string, participantID string) bool {
		member, err := rooms.GetParticipant(roomID, participantID)
		return err == nil && member.MutedByOwner
	})
	if err != nil {
		log.Fatal(err)
	}

	apiServer := api.NewServer(rooms, mediaManager, accessVerifier, config.debugMedia)
	signalingHandler := signaling.NewHandler(rooms, hub, mediaManager, config.frontendOrigins)
	apiHandler := api.WithCORS(apiServer.Routes(), config.frontendOrigins)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/health", apiHandler)
	mux.Handle("GET /ws/rooms/{roomID}", signalingHandler)

	server := &http.Server{
		Addr:              config.listenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("HTTP API and WebSocket listening on %s", config.listenAddress)
	log.Printf("allowed frontend origins: %s", strings.Join(config.frontendOrigins, ", "))
	if config.mediaConfig.UDPPortMin == 0 {
		log.Printf("WebRTC UDP ports: selected by the operating system")
	} else {
		log.Printf("WebRTC UDP ports: %d-%d", config.mediaConfig.UDPPortMin, config.mediaConfig.UDPPortMax)
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func loadServerConfig() (serverConfig, error) {
	listenAddress := flag.String("listen-addr", os.Getenv("LISTEN_ADDR"), "HTTP API and WebSocket address; overrides LISTEN_ADDR")
	frontendOriginValue := flag.String("frontend-origin", os.Getenv("FRONTEND_ORIGIN"), "comma-separated browser origins; overrides FRONTEND_ORIGIN")
	accessTokenFile := flag.String("access-token-file", os.Getenv("ACCESS_TOKEN_FILE"), "shared access token file; overrides ACCESS_TOKEN_FILE")
	udpPortMin := flag.String("udp-port-min", os.Getenv("WEBRTC_UDP_PORT_MIN"), "WebRTC UDP range start; overrides WEBRTC_UDP_PORT_MIN")
	udpPortMax := flag.String("udp-port-max", os.Getenv("WEBRTC_UDP_PORT_MAX"), "WebRTC UDP range end; overrides WEBRTC_UDP_PORT_MAX")
	stunURLs := flag.String("stun-urls", os.Getenv("WEBRTC_STUN_URLS"), "comma-separated STUN URLs; overrides WEBRTC_STUN_URLS")
	debugMedia := flag.Bool("debug-media", environmentBool("VOICED_DEBUG"), "enable GET /api/debug/media; overrides VOICED_DEBUG")
	flag.Parse()

	if strings.TrimSpace(*listenAddress) == "" {
		return serverConfig{}, fmt.Errorf("listen address is required: set LISTEN_ADDR or use -listen-addr")
	}
	frontendOrigins := splitValues(*frontendOriginValue)
	if len(frontendOrigins) == 0 {
		return serverConfig{}, fmt.Errorf("frontend origin is required: set FRONTEND_ORIGIN or use -frontend-origin")
	}
	if strings.TrimSpace(*accessTokenFile) == "" {
		return serverConfig{}, fmt.Errorf("access token file is required: set ACCESS_TOKEN_FILE or use -access-token-file")
	}

	min, err := parsePort("WEBRTC_UDP_PORT_MIN", *udpPortMin)
	if err != nil {
		return serverConfig{}, err
	}
	max, err := parsePort("WEBRTC_UDP_PORT_MAX", *udpPortMax)
	if err != nil {
		return serverConfig{}, err
	}

	mediaConfig := media.Config{UDPPortMin: min, UDPPortMax: max}
	for _, url := range splitValues(*stunURLs) {
		mediaConfig.ICEServers = append(mediaConfig.ICEServers, webrtc.ICEServer{URLs: []string{url}})
	}

	return serverConfig{
		listenAddress:   strings.TrimSpace(*listenAddress),
		frontendOrigins: frontendOrigins,
		accessTokenFile: strings.TrimSpace(*accessTokenFile),
		mediaConfig:     mediaConfig,
		debugMedia:      *debugMedia,
	}, nil
}

func parsePort(name string, value string) (uint16, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("%s must be a UDP port from 1 to 65535", name)
	}
	return uint16(port), nil
}

func splitValues(value string) []string {
	items := strings.Split(value, ",")
	values := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

func environmentBool(name string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return err == nil && value
}
