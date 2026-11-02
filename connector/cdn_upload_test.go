// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadMediaToCDN(t *testing.T) {
	var (
		gotAuth string
		gotBody []byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"abc123","size":1,"expires_at_ms":1}`))
	}))
	defer server.Close()

	cfg := CdnConfig{BaseURL: server.URL + "/", UploadToken: "token-1"}
	link, err := uploadMediaToCDN(context.Background(), cfg, []byte("hello cdn"))
	if err != nil {
		t.Fatalf("uploadMediaToCDN: %v", err)
	}
	if gotAuth != "Bearer token-1" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !strings.HasPrefix(link, server.URL+"/f/abc123#") {
		t.Fatalf("link = %q", link)
	}
	fragment := link[strings.LastIndex(link, "#")+1:]
	key, err := base64.RawURLEncoding.DecodeString(fragment)
	if err != nil || len(key) != 32 {
		t.Fatalf("fragment key = %q, %v", fragment, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotBody) < 12 {
		t.Fatalf("body too short: %d", len(gotBody))
	}
	plain, err := gcm.Open(nil, gotBody[:12], gotBody[12:], nil)
	if err != nil {
		t.Fatalf("decrypt uploaded body: %v", err)
	}
	if string(plain) != "hello cdn" {
		t.Fatalf("plain = %q", plain)
	}
}

func TestUploadMediaToCDNRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	_, err := uploadMediaToCDN(context.Background(), CdnConfig{BaseURL: server.URL, UploadToken: "bad"}, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}
