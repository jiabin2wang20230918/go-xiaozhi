package auth

import (
	"net/http"
	"testing"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

func TestAuthenticateDisabled(t *testing.T) {
	a := New(config.AuthConf{Enabled: false})
	req, _ := http.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	if err := a.Authenticate(req); err != nil {
		t.Fatalf("disabled auth should allow request: %v", err)
	}
}

func TestAuthenticateAllowedDevice(t *testing.T) {
	a := New(config.AuthConf{
		Enabled:        true,
		AllowedDevices: []string{"device-1"},
	})
	req, _ := http.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	req.Header.Set("Device-Id", "device-1")
	if err := a.Authenticate(req); err != nil {
		t.Fatalf("allowed device should bypass token: %v", err)
	}
}

func TestAuthenticateEmptyAllowedDevicesDoesNotBypassToken(t *testing.T) {
	a := New(config.AuthConf{
		Enabled:        true,
		AllowedDevices: []string{""},
	})
	req, _ := http.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	if err := a.Authenticate(req); err != ErrMissingAuthorization {
		t.Fatalf("empty allowed device list should not bypass token, got %v", err)
	}
}

func TestAuthenticateBearerToken(t *testing.T) {
	a := New(config.AuthConf{
		Enabled: true,
		Tokens:  []config.AuthToken{{Token: "secret", Name: "device"}},
	})
	req, _ := http.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	if err := a.Authenticate(req); err != nil {
		t.Fatalf("valid bearer token rejected: %v", err)
	}
}

func TestAuthenticateMatchesPythonBearerTokenParsing(t *testing.T) {
	a := New(config.AuthConf{
		Enabled: true,
		Tokens:  []config.AuthToken{{Token: "secret", Name: "device"}},
	})
	tests := []struct {
		name   string
		header string
		want   error
	}{
		{name: "trailing space ignored by split index", header: "Bearer secret ", want: nil},
		{name: "extra field ignored by split index", header: "Bearer secret extra", want: nil},
		{name: "double space produces empty token", header: "Bearer  secret", want: ErrInvalidToken},
		{name: "missing bearer space", header: "Bearersecret", want: ErrMissingAuthorization},
		{name: "empty token", header: "Bearer ", want: ErrInvalidToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
			req.Header.Set("Authorization", tt.header)
			if err := a.Authenticate(req); err != tt.want {
				t.Fatalf("Authenticate() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestAuthenticateRejectsInvalidToken(t *testing.T) {
	a := New(config.AuthConf{
		Enabled: true,
		Tokens:  []config.AuthToken{{Token: "secret", Name: "device"}},
	})
	req, _ := http.NewRequest(http.MethodGet, "/xiaozhi/v1/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	if err := a.Authenticate(req); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}
