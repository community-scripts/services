package main

import (
	"bytes"
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
	"strings"
	"sync"
	"time"
)

type gitHub struct {
	appID          string
	installationID string
	key            *rsa.PrivateKey
	client         *http.Client

	mu        sync.Mutex
	token     string
	tokenTill time.Time
}

func newGitHub(appID, installationID, privateKeyPEM string) (*gitHub, error) {
	key, err := parseRSAKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	return &gitHub{
		appID:          appID,
		installationID: installationID,
		key:            key,
		client:         &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// GitHub hands out PKCS#1 ("RSA PRIVATE KEY"); accept PKCS#8 too so a re-encoded
// key does not fail at runtime.
func parseRSAKey(pemData string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, fmt.Errorf("github private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github private key is not RSA")
	}
	return key, nil
}

func b64(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// App JWTs are RS256 and must not live longer than 10 minutes; the backdated iat
// absorbs clock skew between us and GitHub.
func (g *gitHub) appJWT() (string, error) {
	now := time.Now()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": g.appID,
	})
	if err != nil {
		return "", err
	}

	signingInput := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign app jwt: %w", err)
	}
	return signingInput + "." + b64(sig), nil
}

func (g *gitHub) installationToken() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.token != "" && time.Now().Before(g.tokenTill.Add(-time.Minute)) {
		return g.token, nil
	}

	jwt, err := g.appJWT()
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", g.installationID)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("installation token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("installation token: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("installation token: %w", err)
	}
	g.token, g.tokenTill = out.Token, out.ExpiresAt
	return g.token, nil
}

type createdIssue struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
}

func splitRepo(fullName string) (owner, repo string, err error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repository %q, expected owner/name", fullName)
	}
	return parts[0], parts[1], nil
}

func (g *gitHub) createIssue(fullName, title, body string, labels []string) (createdIssue, error) {
	owner, repo, err := splitRepo(fullName)
	if err != nil {
		return createdIssue{}, err
	}
	token, err := g.installationToken()
	if err != nil {
		return createdIssue{}, err
	}

	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return createdIssue{}, err
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues", owner, repo)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return createdIssue{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return createdIssue{}, fmt.Errorf("create issue: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return createdIssue{}, fmt.Errorf("create issue: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var issue createdIssue
	if err := json.Unmarshal(respBody, &issue); err != nil {
		return createdIssue{}, fmt.Errorf("create issue: %w", err)
	}
	return issue, nil
}
