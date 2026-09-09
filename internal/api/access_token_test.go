package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"voiced/internal/access"
	"voiced/internal/room"
)

func TestCreateAndJoinRequireAccessToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "access-token")
	if err := os.WriteFile(tokenFile, []byte("room-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := access.NewFileVerifier(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(room.NewManager(), nil, verifier, false).Routes()

	denied := requestJSON(t, handler, http.MethodPost, "/api/rooms", map[string]string{
		"name": "Private room", "nickname": "Owner", "token": "wrong",
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("create with wrong token status = %d, want %d", denied.Code, http.StatusForbidden)
	}

	created := requestJSON(t, handler, http.MethodPost, "/api/rooms", map[string]string{
		"name": "Private room", "nickname": "Owner", "token": "room-token",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create with valid token status = %d, want %d: %s", created.Code, http.StatusCreated, created.Body.String())
	}
	var response struct {
		Room room.Info `json:"room"`
	}
	if err := json.NewDecoder(created.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}

	joinPath := "/api/rooms/" + response.Room.ID + "/join"
	denied = requestJSON(t, handler, http.MethodPost, joinPath, map[string]string{
		"nickname": "Guest", "token": "wrong",
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("join with wrong token status = %d, want %d", denied.Code, http.StatusForbidden)
	}

	joined := requestJSON(t, handler, http.MethodPost, joinPath, map[string]string{
		"nickname": "Guest", "token": "room-token",
	})
	if joined.Code != http.StatusOK {
		t.Fatalf("join with valid token status = %d, want %d: %s", joined.Code, http.StatusOK, joined.Body.String())
	}
}

func requestJSON(t *testing.T, handler http.Handler, method string, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
