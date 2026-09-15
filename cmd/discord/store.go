package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type store struct {
	baseURL    string
	email      string
	password   string
	collection string
	client     *http.Client

	mu    sync.Mutex
	token string
}

type guildSettings struct {
	Enabled        bool
	AllowedRoleIDs []string
	TargetRepo     string
	DefaultLabels  []string
}

type issueRecord struct {
	IssueNumber int    `json:"issue_number"`
	IssueURL    string `json:"issue_url"`
}

func newStore(baseURL, email, password, collection string) *store {
	return &store{
		baseURL:    strings.TrimRight(baseURL, "/"),
		email:      email,
		password:   password,
		collection: collection,
		client:     &http.Client{Timeout: 20 * time.Second},
	}
}

// PocketBase 0.23 moved superuser auth to the _superusers collection; older
// builds only expose /api/admins.
func (s *store) authenticate() error {
	payload, err := json.Marshal(map[string]string{"identity": s.email, "password": s.password})
	if err != nil {
		return err
	}

	// A dedicated collection user is preferred; the superuser paths stay as the
	// fallback. /api/admins is gone in PocketBase 0.23+.
	paths := []string{"/api/collections/_superusers/auth-with-password", "/api/admins/auth-with-password"}
	if s.collection != "" {
		paths = []string{"/api/collections/" + s.collection + "/auth-with-password"}
	}

	var attempts []string
	for _, path := range paths {
		req, err := http.NewRequest(http.MethodPost, s.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.client.Do(req)
		if err != nil {
			attempts = append(attempts, path+": "+err.Error())
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			attempts = append(attempts, path+": "+resp.Status)
			continue
		}

		var out struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			attempts = append(attempts, path+": "+err.Error())
			continue
		}
		s.mu.Lock()
		s.token = out.Token
		s.mu.Unlock()
		return nil
	}
	return fmt.Errorf("pocketbase auth failed: %s", strings.Join(attempts, "; "))
}

func (s *store) currentToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// Retries once on 401/403 so an expired token refreshes itself instead of
// surfacing as a failed command.
func (s *store) do(method, path string, body []byte) ([]byte, error) {
	attempt := func() (*http.Response, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, s.baseURL+path, reader)
		if err != nil {
			return nil, err
		}
		if token := s.currentToken(); token != "" {
			req.Header.Set("Authorization", token)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return s.client.Do(req)
	}

	if s.currentToken() == "" {
		if err := s.authenticate(); err != nil {
			return nil, err
		}
	}

	resp, err := attempt()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		if err := s.authenticate(); err != nil {
			return nil, err
		}
		if resp, err = attempt(); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pocketbase %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(payload)))
	}
	return payload, nil
}

func (s *store) firstRecord(collection, filter string) (json.RawMessage, error) {
	path := fmt.Sprintf("/api/collections/%s/records?perPage=1&filter=%s", collection, url.QueryEscape(filter))
	payload, err := s.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	return out.Items[0], nil
}

// allowed_role_ids and default_labels are json fields, which PocketBase may hand
// back either as an array or as a string holding one.
func decodeStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil || encoded == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(encoded), &list); err != nil {
		return nil
	}
	return list
}

func (s *store) guildConfig(guildID string) (*guildSettings, error) {
	raw, err := s.firstRecord("discord_config", fmt.Sprintf("guild_id='%s'", guildID))
	if err != nil || raw == nil {
		return nil, err
	}

	var record struct {
		Enabled        *bool           `json:"enabled"`
		AllowedRoleIDs json.RawMessage `json:"allowed_role_ids"`
		TargetRepo     string          `json:"target_repo"`
		DefaultLabels  json.RawMessage `json:"default_labels"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}

	return &guildSettings{
		Enabled:        record.Enabled == nil || *record.Enabled,
		AllowedRoleIDs: decodeStringList(record.AllowedRoleIDs),
		TargetRepo:     record.TargetRepo,
		DefaultLabels:  decodeStringList(record.DefaultLabels),
	}, nil
}

func (s *store) issueForThread(threadID string) (*issueRecord, error) {
	raw, err := s.firstRecord("discord_issues", fmt.Sprintf("thread_id='%s'", threadID))
	if err != nil || raw == nil {
		return nil, err
	}
	var record issueRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *store) recordIssue(entry map[string]any) error {
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = s.do(http.MethodPost, "/api/collections/discord_issues/records", payload)
	return err
}
