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
	apiBase        string

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
		apiBase:        "https://api.github.com",
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

	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", g.apiBase, g.installationID)
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

// do sends one authenticated request; path is relative to the API base.
func (g *gitHub) do(method, path string, payload any, want int) ([]byte, error) {
	token, err := g.installationToken()
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, g.apiBase+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (g *gitHub) get(path string, out any) error {
	body, err := g.do(http.MethodGet, path, nil, http.StatusOK)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	Locked      bool      `json:"locked"`
	State       string    `json:"state"`
	Labels      []ghLabel `json:"labels"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

func (i ghIssue) labelNames() []string {
	names := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		names = append(names, l.Name)
	}
	return names
}

type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type ghComment struct {
	ID                    int64  `json:"id"`
	Body                  string `json:"body"`
	HTMLURL               string `json:"html_url"`
	User                  ghUser `json:"user"`
	PerformedViaGitHubApp *struct {
		ID int64 `json:"id"`
	} `json:"performed_via_github_app"`
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

func repoPath(fullName string) (string, error) {
	owner, repo, err := splitRepo(fullName)
	if err != nil {
		return "", err
	}
	return "/repos/" + owner + "/" + repo, nil
}

func (g *gitHub) createIssue(fullName, title, body string, labels []string) (createdIssue, error) {
	base, err := repoPath(fullName)
	if err != nil {
		return createdIssue{}, err
	}
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	out, err := g.do(http.MethodPost, base+"/issues", payload, http.StatusCreated)
	if err != nil {
		return createdIssue{}, fmt.Errorf("create issue: %w", err)
	}
	var issue createdIssue
	if err := json.Unmarshal(out, &issue); err != nil {
		return createdIssue{}, fmt.Errorf("create issue: %w", err)
	}
	return issue, nil
}

// updateIssue refreshes the body only: title and labels may have been corrected on GitHub.
func (g *gitHub) updateIssue(fullName string, number int, body string) error {
	base, err := repoPath(fullName)
	if err != nil {
		return err
	}
	_, err = g.do(http.MethodPatch, fmt.Sprintf("%s/issues/%d", base, number), map[string]any{"body": body}, http.StatusOK)
	return err
}

func (g *gitHub) issue(fullName string, number int) (ghIssue, error) {
	base, err := repoPath(fullName)
	if err != nil {
		return ghIssue{}, err
	}
	var out ghIssue
	err = g.get(fmt.Sprintf("%s/issues/%d", base, number), &out)
	return out, err
}

func (g *gitHub) listIssues(fullName, state string) ([]ghIssue, error) {
	base, err := repoPath(fullName)
	if err != nil {
		return nil, err
	}
	var all []ghIssue
	for page := 1; page <= 20; page++ {
		var batch []ghIssue
		if err := g.get(fmt.Sprintf("%s/issues?state=%s&per_page=100&page=%d", base, state, page), &batch); err != nil {
			return nil, err
		}
		for _, i := range batch {
			if i.PullRequest == nil {
				all = append(all, i)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

func (g *gitHub) comments(fullName string, number int) ([]ghComment, error) {
	base, err := repoPath(fullName)
	if err != nil {
		return nil, err
	}
	var all []ghComment
	for page := 1; page <= 20; page++ {
		var batch []ghComment
		if err := g.get(fmt.Sprintf("%s/issues/%d/comments?per_page=100&page=%d", base, number, page), &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

func (g *gitHub) createComment(fullName string, number int, body string) (ghComment, error) {
	base, err := repoPath(fullName)
	if err != nil {
		return ghComment{}, err
	}
	out, err := g.do(http.MethodPost, fmt.Sprintf("%s/issues/%d/comments", base, number), map[string]string{"body": body}, http.StatusCreated)
	if err != nil {
		return ghComment{}, err
	}
	var c ghComment
	if err := json.Unmarshal(out, &c); err != nil {
		return ghComment{}, err
	}
	return c, nil
}

func (g *gitHub) updateComment(fullName string, id int64, body string) error {
	base, err := repoPath(fullName)
	if err != nil {
		return err
	}
	_, err = g.do(http.MethodPatch, fmt.Sprintf("%s/issues/comments/%d", base, id), map[string]string{"body": body}, http.StatusOK)
	return err
}

func (g *gitHub) deleteComment(fullName string, id int64) error {
	base, err := repoPath(fullName)
	if err != nil {
		return err
	}
	_, err = g.do(http.MethodDelete, fmt.Sprintf("%s/issues/comments/%d", base, id), nil, http.StatusNoContent)
	return err
}

func (g *gitHub) lockIssue(fullName string, number int, reason string) error {
	base, err := repoPath(fullName)
	if err != nil {
		return err
	}
	_, err = g.do(http.MethodPut, fmt.Sprintf("%s/issues/%d/lock", base, number), map[string]string{"lock_reason": reason}, http.StatusNoContent)
	return err
}

func (g *gitHub) unlockIssue(fullName string, number int) error {
	base, err := repoPath(fullName)
	if err != nil {
		return err
	}
	_, err = g.do(http.MethodDelete, fmt.Sprintf("%s/issues/%d/lock", base, number), nil, http.StatusNoContent)
	return err
}
