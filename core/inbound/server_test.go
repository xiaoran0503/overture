package inbound

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shawn1m/overture/core/outbound"
)

func TestIsLoopbackAddress(t *testing.T) {
	tests := []struct {
		address  string
		loopback bool
	}{
		{address: "127.0.0.1:5555", loopback: true},
		{address: "[::1]:5555", loopback: true},
		{address: ":5555", loopback: false},
		{address: "0.0.0.0:5555", loopback: false},
	}

	for _, tt := range tests {
		if got := isLoopbackAddress(tt.address); got != tt.loopback {
			t.Errorf("isLoopbackAddress(%q) = %v, want %v", tt.address, got, tt.loopback)
		}
	}
}

func TestHTTPTokenProtectsControlPaths(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, nil, false, "secret")
	s.HTTPMux.HandleFunc("/config", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	unauthorized := httptest.NewRecorder()
	s.httpHandler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/config", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/config", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer secret")
	authorized := httptest.NewRecorder()
	s.httpHandler().ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("authorized status = %d, want %d", authorized.Code, http.StatusNoContent)
	}
}

func TestRunRejectsUnprotectedRemoteHTTP(t *testing.T) {
	s := NewServer("127.0.0.1:53", "0.0.0.0:5555", outbound.Dispatcher{}, nil, false, "")
	if err := s.Run(); err == nil {
		t.Fatal("Run accepted an unprotected non-loopback debug HTTP address")
	}
}
