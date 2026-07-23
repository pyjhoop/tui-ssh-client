package model_test

import (
	"strings"
	"testing"

	"github.com/pyjhoop/tui-ssh-client/internal/model"
)

func TestTitleFallsBackToUserHost(t *testing.T) {
	s := model.Server{Host: "example.com", User: "root"}
	if got, want := s.Title(), "root@example.com"; got != want {
		t.Errorf("Title: got %q, want %q", got, want)
	}

	s.Name = "prod"
	if got, want := s.Title(), "prod"; got != want {
		t.Errorf("Title with name: got %q, want %q", got, want)
	}
}

func TestAddrUsesDefaultPort(t *testing.T) {
	s := model.Server{Host: "example.com", User: "root"}
	if got, want := s.Addr(), "example.com:22"; got != want {
		t.Errorf("Addr: got %q, want %q", got, want)
	}

	s.Port = 2222
	if got, want := s.Addr(), "example.com:2222"; got != want {
		t.Errorf("Addr with port: got %q, want %q", got, want)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		server  model.Server
		wantErr bool
	}{
		{"password ok", model.Server{Host: "h", User: "u", Port: 22, Auth: model.AuthPassword, Password: "p"}, false},
		{"key ok", model.Server{Host: "h", User: "u", Port: 22, Auth: model.AuthKey, KeyPath: "/k.pem"}, false},
		{"no host", model.Server{User: "u", Auth: model.AuthPassword, Password: "p"}, true},
		{"no user", model.Server{Host: "h", Auth: model.AuthPassword, Password: "p"}, true},
		{"no password", model.Server{Host: "h", User: "u", Auth: model.AuthPassword}, true},
		{"no key path", model.Server{Host: "h", User: "u", Auth: model.AuthKey}, true},
		{"bad port", model.Server{Host: "h", User: "u", Port: 70000, Auth: model.AuthPassword, Password: "p"}, true},
		{"unknown auth", model.Server{Host: "h", User: "u", Auth: "totp"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.server.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate: got err %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestFilterKeyCoversEveryColumn: the sidebar filter is one substring match, so
// everything the user might type has to be in this one string.
func TestFilterKeyCoversEveryColumn(t *testing.T) {
	s := model.Server{Name: "Web-1", User: "Deploy", Host: "10.0.0.1", Group: "PROD"}
	key := s.FilterKey()

	for _, want := range []string{"web-1", "deploy", "10.0.0.1", "prod"} {
		if !strings.Contains(key, want) {
			t.Errorf("FilterKey() = %q, missing %q", key, want)
		}
	}
	if key != strings.ToLower(key) {
		t.Errorf("FilterKey() = %q, want it lowercased", key)
	}
}
