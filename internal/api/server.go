package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"voiced/internal/media"
	"voiced/internal/room"
)

type Server struct {
	rooms      *room.Manager
	media      *media.Manager
	debugMedia bool
}

func NewServer(rooms *room.Manager, mediaManager *media.Manager, debugMedia bool) *Server {
	return &Server{rooms: rooms, media: mediaManager, debugMedia: debugMedia}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/rooms", s.listRooms)
	mux.HandleFunc("POST /api/rooms", s.createRoom)
	mux.HandleFunc("GET /api/rooms/{roomID}", s.getRoom)
	mux.HandleFunc("POST /api/rooms/{roomID}/join", s.joinRoom)
	mux.HandleFunc("GET /api/rooms/{roomID}/participants", s.listParticipants)
	mux.HandleFunc("GET /api/webrtc-config", s.webrtcConfig)
	if s.debugMedia {
		mux.HandleFunc("GET /api/debug/media", s.mediaDebug)
	}

	return mux
}

// WithCORS permits a separately deployed browser frontend to call this API.
// Exact origins are configured at startup; arbitrary sites are not allowed.
func WithCORS(next http.Handler, allowedOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if originAllowed(origin, allowedOrigins) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			if !originAllowed(origin, allowedOrigins) {
				http.Error(w, "origin is not allowed", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originAllowed(origin string, allowedOrigins []string) bool {
	for _, allowedOrigin := range allowedOrigins {
		if origin == allowedOrigin {
			return true
		}
	}
	return false
}

// webrtcConfig gives the browser the public ICE server URLs. It deliberately
// excludes credentials: production TURN credentials need short-lived, user-
// specific generation rather than a static public API response.
func (s *Server) webrtcConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.media.PublicConfig())
}

func (s *Server) mediaDebug(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.media.DebugInfo())
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) createRoom(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name     string `json:"name"`
		Nickname string `json:"nickname"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}

	joined, err := s.rooms.Create(request.Name, request.Nickname)
	if err != nil {
		writeRoomError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, joined)
}

func (s *Server) listRooms(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.rooms.List())
}

func (s *Server) getRoom(w http.ResponseWriter, r *http.Request) {
	roomInfo, err := s.rooms.Get(r.PathValue("roomID"))
	if err != nil {
		writeRoomError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, roomInfo)
}

func (s *Server) joinRoom(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Nickname string `json:"nickname"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}

	joined, err := s.rooms.Join(r.PathValue("roomID"), request.Nickname)
	if err != nil {
		writeRoomError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, joined)
}

func (s *Server) listParticipants(w http.ResponseWriter, r *http.Request) {
	members, err := s.rooms.ListParticipants(r.PathValue("roomID"))
	if err != nil {
		writeRoomError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, members)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "CONTENT_TYPE_REQUIRED", "Content-Type must be application/json")
		return false
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body must be valid JSON")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body must contain one JSON value")
		return false
	}

	return true
}

func writeRoomError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, room.ErrNotFound), errors.Is(err, room.ErrParticipantNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, room.ErrRoomNameRequired), errors.Is(err, room.ErrNicknameRequired):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, room.ErrNotOwner):
		writeError(w, http.StatusForbidden, "NOT_OWNER", err.Error())
	case errors.Is(err, room.ErrCannotRemoveOwner):
		writeError(w, http.StatusConflict, "OWNER_MUST_LEAVE", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
