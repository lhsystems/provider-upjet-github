/*
Copyright 2022 Upbound Inc.
*/

package budget

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewGitHubServicePAT(t *testing.T) {
	token := "personal-access-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization header = %q, want Bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"budgets":[]}`))
	}))
	defer server.Close()

	service, err := NewGitHubService(context.Background(), GithubCredentials{
		Token:   &token,
		BaseURL: &server.URL,
	})
	if err != nil {
		t.Fatalf("NewGitHubService() error = %v", err)
	}
	if _, err := service.ListBudgets(context.Background(), "example"); err != nil {
		t.Fatalf("ListBudgets() error = %v", err)
	}
}

func TestNewGitHubServiceAppAuthentication(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}
	pemFile := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/app/installations/456/access_tokens":
			if got := req.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
				t.Errorf("App token Authorization header = %q, want Bearer JWT", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
		case "/enterprises/example/settings/billing/budgets":
			if got := req.Header.Get("Authorization"); got != "token installation-token" {
				t.Errorf("API Authorization header = %q, want installation token", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"budgets":[]}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	appAuth := []GithubAppAuth{{ID: "123", InstallationID: "456", PEMFile: pemFile}}
	service, err := NewGitHubService(context.Background(), GithubCredentials{
		AppAuth: &appAuth,
		BaseURL: &server.URL,
	})
	if err != nil {
		t.Fatalf("NewGitHubService() error = %v", err)
	}
	if _, err := service.ListBudgets(context.Background(), "example"); err != nil {
		t.Fatalf("ListBudgets() error = %v", err)
	}
}

func TestNewGitHubServiceRejectsInvalidCredentials(t *testing.T) {
	tests := map[string]GithubCredentials{
		"missing authentication": {},
		"empty app auth":         {AppAuth: &[]GithubAppAuth{}},
		"invalid app id":         {AppAuth: &[]GithubAppAuth{{ID: "app", InstallationID: "456", PEMFile: "key"}}},
		"invalid installation id": {
			AppAuth: &[]GithubAppAuth{{ID: "123", InstallationID: "installation", PEMFile: "key"}},
		},
	}

	for name, credentials := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewGitHubService(context.Background(), credentials); err == nil {
				t.Fatal("NewGitHubService() error = nil, want credential validation error")
			}
		})
	}
}
