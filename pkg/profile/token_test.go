package profile_test

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

func makeJWT(email string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"email":"%s"}`, email)))
	return fmt.Sprintf("%s.%s.sig", header, payload)
}

func TestParseTokenJSON_ExtractsEmailFromExplicitField(t *testing.T) {
	data := []byte(`{
		"access_token": "ya29.fake-token",
		"refresh_token": "1//fake-refresh",
		"email": "user@example.com",
		"expiry": "2026-11-17T15:00:00Z"
	}`)
	tok, err := profile.ParseTokenJSON(data)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if tok.Email != "user@example.com" {
		t.Errorf("expected user@example.com, got %s", tok.Email)
	}
	if tok.AccessToken != "ya29.fake-token" {
		t.Errorf("unexpected access token: %s", tok.AccessToken)
	}
}

func TestParseTokenJSON_IDTokenEmailField(t *testing.T) {
	data := []byte(`{
		"token": {
			"access_token": "ya29.live-access",
			"refresh_token": "1//live-refresh"
		},
		"id_token_email": "liveuser@example.com",
		"auth_method": "oauth"
	}`)
	tok, err := profile.ParseTokenJSON(data)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if tok.Email != "liveuser@example.com" {
		t.Errorf("expected liveuser@example.com, got %s", tok.Email)
	}
}

func TestParseTokenJSON_NestedTokenFormat(t *testing.T) {
	// agy standard nested format
	data := []byte(`{
		"token": {
			"access_token": "ya29.nested-access",
			"refresh_token": "1//nested-refresh",
			"expiry": "2026-11-17T15:00:00Z"
		},
		"id_token": "` + makeJWT("nested@example.com") + `"
	}`)
	tok, err := profile.ParseTokenJSON(data)
	if err != nil {
		t.Fatalf("failed to parse nested token: %v", err)
	}
	if tok.AccessToken != "ya29.nested-access" {
		t.Errorf("expected ya29.nested-access, got %s", tok.AccessToken)
	}
	if tok.Email != "nested@example.com" {
		t.Errorf("expected nested@example.com, got %s", tok.Email)
	}
}

func TestParseTokenJSON_ExtractsEmailFromIDTokenJWT(t *testing.T) {
	jwt := makeJWT("jwt_user@example.com")
	data := []byte(fmt.Sprintf(`{
		"access_token": "ya29.fake",
		"id_token": "%s"
	}`, jwt))
	tok, err := profile.ParseTokenJSON(data)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if tok.Email != "jwt_user@example.com" {
		t.Errorf("expected jwt_user@example.com, got %s", tok.Email)
	}
}

func TestParseTokenJSON_InvalidData(t *testing.T) {
	_, err := profile.ParseTokenJSON([]byte(`invalid json`))
	if err == nil {
		t.Errorf("expected error on invalid json, got nil")
	}
}
