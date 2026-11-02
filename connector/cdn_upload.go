// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// cdnUploadTimeout bounds one outbound upload to the file service.
const cdnUploadTimeout = 30 * time.Second

// uploadMediaToCDN encrypts data with a fresh AES-256-GCM key, uploads the
// ciphertext (body = nonce || ciphertext || tag) to the bridge file service
// and returns the zero-knowledge link. The key travels only in the URL
// fragment, so the file service only ever stores ciphertext.
func uploadMediaToCDN(ctx context.Context, cdn CdnConfig, data []byte) (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate cdn key: %w", err)
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate cdn nonce: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cdn cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create cdn gcm: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, data, nil)
	body := make([]byte, 0, len(nonce)+len(sealed))
	body = append(body, nonce...)
	body = append(body, sealed...)

	base := strings.TrimRight(strings.TrimSpace(cdn.BaseURL), "/")
	uploadURL := base + "/f"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build cdn request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cdn.UploadToken))
	req.Header.Set("Content-Type", "application/octet-stream")
	client := &http.Client{Timeout: cdnUploadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cdn upload failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return "", fmt.Errorf("cdn upload rejected (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode cdn response: %w", err)
	}
	if parsed.ID == "" {
		return "", fmt.Errorf("cdn upload returned no id")
	}
	return base + "/f/" + parsed.ID + "#" + base64.RawURLEncoding.EncodeToString(key), nil
}
