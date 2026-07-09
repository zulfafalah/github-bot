package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// appJWT builds a short-lived JWT signed with the GitHub App's private key,
// used to authenticate as the app itself (not an installation).
func appJWT(cfg *config) (string, error) {
	key, err := loadPrivateKey(cfg)
	if err != nil {
		return "", fmt.Errorf("load private key: %w", err)
	}

	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": cfg.AppID,
	}

	headerB64, err := jsonB64(header)
	if err != nil {
		return "", err
	}
	claimsB64, err := jsonB64(claims)
	if err != nil {
		return "", err
	}

	signingInput := headerB64 + "." + claimsB64
	hashed := sha256.Sum256([]byte(signingInput))

	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func jsonB64(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func loadPrivateKey(cfg *config) (*rsa.PrivateKey, error) {
	var pemBytes []byte
	if cfg.PrivateKeyPath != "" {
		b, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, err
		}
		pemBytes = b
	} else {
		pemBytes = []byte(cfg.PrivateKeyPEM)
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM data found")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return key, nil
}

type installationTokenResponse struct {
	Token string `json:"token"`
}

// installationToken exchanges the app JWT for a short-lived token scoped to
// one installation, which is what's used to call the GitHub API as the app.
func installationToken(cfg *config, installationID int64) (string, error) {
	jwt, err := appJWT(cfg)
	if err != nil {
		return "", err
	}

	endpoint := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "fal-github-bot")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("access_tokens API %d: %s", resp.StatusCode, body)
	}

	var res installationTokenResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return "", err
	}
	return res.Token, nil
}
