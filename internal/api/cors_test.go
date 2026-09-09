package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithCORSAllowsConfiguredOrigin(t *testing.T) {
	handler := WithCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), []string{"http://localhost:5173"})

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestWithCORSRejectsUnknownPreflightOrigin(t *testing.T) {
	handler := WithCORS(http.NotFoundHandler(), []string{"http://localhost:5173"})

	request := httptest.NewRequest(http.MethodOptions, "/api/rooms", nil)
	request.Header.Set("Origin", "https://untrusted.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}
