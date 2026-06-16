package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/xdimtech/go-xiaozhi/pkg/config"
)

var (
	ErrMissingAuthorization = errors.New("missing or invalid Authorization header")
	ErrInvalidToken         = errors.New("invalid token")
)

type Authenticator struct {
	enabled        bool
	tokens         map[string]string
	allowedDevices map[string]struct{}
}

func New(conf config.AuthConf) *Authenticator {
	a := &Authenticator{
		enabled:        conf.Enabled,
		tokens:         make(map[string]string, len(conf.Tokens)),
		allowedDevices: make(map[string]struct{}, len(conf.AllowedDevices)),
	}
	for _, token := range conf.Tokens {
		if strings.TrimSpace(token.Token) == "" {
			continue
		}
		a.tokens[token.Token] = token.Name
	}
	for _, device := range conf.AllowedDevices {
		device = strings.TrimSpace(device)
		if device != "" {
			a.allowedDevices[device] = struct{}{}
		}
	}
	return a
}

func (a *Authenticator) Authenticate(r *http.Request) error {
	if a == nil || !a.enabled {
		return nil
	}

	deviceID := r.Header.Get("Device-Id")
	if _, ok := a.allowedDevices[deviceID]; ok {
		return nil
	}

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return ErrMissingAuthorization
	}
	token := bearerTokenLikePython(authHeader)
	if _, ok := a.tokens[token]; !ok {
		return ErrInvalidToken
	}
	return nil
}

func bearerTokenLikePython(authHeader string) string {
	parts := strings.Split(authHeader, " ")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}
