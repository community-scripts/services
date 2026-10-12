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
	GuildID          string
	Enabled          bool
	AllowedRoleIDs   []string
	TargetRepo       string
	DefaultLabels    []string
	DevRepo          string
	TestingChannelID string
}

type threadRecord struct {
	ID          string `json:"id"`
	GuildID     string `json:"guild_id"`
	ThreadID    string `json:"thread_id"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	IssueURL    string `json:"issue_url"`
	Kind        string `json:"kind"`
	Slug        string `json:"slug"`
	State       string `json:"state"`
	SyncEnabled bool   `json:"sync_enabled"`
}

type syncRecord struct {
	ID        string `json:"id,omitempty"`
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id"`
	CommentID int64  `json:"comment_id"`
	Origin    string `json:"origin"`
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

func (s *store) do(method, path string, body []byte) ([]byte, error) {
	return s.request(method, path, "application/json", body)
}

// upload posts a multipart body, which PocketBase needs for file fields.
func (s *store) upload(path, contentType string, body []byte) ([]byte, error) {
	return s.request(http.MethodPost, path, contentType, body)
}

// Retries once on 401/403 so an expired token refreshes itself.
func (s *store) request(method, path, contentType string, body []byte) ([]byte, error) {
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
		if body != nil && contentType != "" {
			req.Header.Set("Content-Type", contentType)
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

func (s *store) listRecords(collection, filter string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	for page := 1; ; page++ {
		path := fmt.Sprintf("/api/collections/%s/records?perPage=200&page=%d&filter=%s", collection, page, url.QueryEscape(filter))
		payload, err := s.do(http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var out struct {
			Page       int               `json:"page"`
			TotalPages int               `json:"totalPages"`
			Items      []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(payload, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Items...)
		if out.Page >= out.TotalPages || len(out.Items) == 0 {
			return all, nil
		}
	}
}

func decodeInto[T any](raw json.RawMessage) (*T, error) {
	if raw == nil {
		return nil, nil
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func decodeList[T any](raws []json.RawMessage) ([]T, error) {
	out := make([]T, 0, len(raws))
	for _, raw := range raws {
		var item T
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
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

func decodeSettings(raw json.RawMessage) (*guildSettings, error) {
	if raw == nil {
		return nil, nil
	}
	var record struct {
		GuildID          string          `json:"guild_id"`
		Enabled          *bool           `json:"enabled"`
		AllowedRoleIDs   json.RawMessage `json:"allowed_role_ids"`
		TargetRepo       string          `json:"target_repo"`
		DefaultLabels    json.RawMessage `json:"default_labels"`
		DevRepo          string          `json:"dev_repo"`
		TestingChannelID string          `json:"testing_channel_id"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	return &guildSettings{
		GuildID:          record.GuildID,
		Enabled:          record.Enabled == nil || *record.Enabled,
		AllowedRoleIDs:   decodeStringList(record.AllowedRoleIDs),
		TargetRepo:       record.TargetRepo,
		DefaultLabels:    decodeStringList(record.DefaultLabels),
		DevRepo:          record.DevRepo,
		TestingChannelID: record.TestingChannelID,
	}, nil
}

func (s *store) guildConfig(guildID string) (*guildSettings, error) {
	raw, err := s.firstRecord("discord_config", fmt.Sprintf("guild_id='%s'", guildID))
	if err != nil {
		return nil, err
	}
	return decodeSettings(raw)
}

func (s *store) configByDevRepo(repo string) (*guildSettings, error) {
	raw, err := s.firstRecord("discord_config", fmt.Sprintf("dev_repo='%s'", repo))
	if err != nil {
		return nil, err
	}
	return decodeSettings(raw)
}

func (s *store) allConfigs() ([]guildSettings, error) {
	raws, err := s.listRecords("discord_config", "")
	if err != nil {
		return nil, err
	}
	out := make([]guildSettings, 0, len(raws))
	for _, raw := range raws {
		cfg, err := decodeSettings(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, *cfg)
	}
	return out, nil
}

func (s *store) issueForThread(threadID string) (*threadRecord, error) {
	raw, err := s.firstRecord("discord_issues", fmt.Sprintf("thread_id='%s'", threadID))
	if err != nil {
		return nil, err
	}
	return decodeInto[threadRecord](raw)
}

func (s *store) threadByIssue(repo string, number int) (*threadRecord, error) {
	raw, err := s.firstRecord("discord_issues", fmt.Sprintf("repo='%s' && issue_number=%d", repo, number))
	if err != nil {
		return nil, err
	}
	return decodeInto[threadRecord](raw)
}

func (s *store) syncedThreads() ([]threadRecord, error) {
	raws, err := s.listRecords("discord_issues", "sync_enabled=true")
	if err != nil {
		return nil, err
	}
	return decodeList[threadRecord](raws)
}

func (s *store) recordIssue(entry map[string]any) (*threadRecord, error) {
	payload, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	out, err := s.do(http.MethodPost, "/api/collections/discord_issues/records", payload)
	if err != nil {
		return nil, err
	}
	return decodeInto[threadRecord](out)
}

func (s *store) updateIssueRecord(id string, entry map[string]any) error {
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = s.do(http.MethodPatch, "/api/collections/discord_issues/records/"+id, payload)
	return err
}

func (s *store) recordSync(rows []syncRecord) error {
	for _, row := range rows {
		payload, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := s.do(http.MethodPost, "/api/collections/discord_sync/records", payload); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) syncByMessage(messageID string) (*syncRecord, error) {
	raw, err := s.firstRecord("discord_sync", fmt.Sprintf("message_id='%s'", messageID))
	if err != nil {
		return nil, err
	}
	return decodeInto[syncRecord](raw)
}

func (s *store) syncByComment(commentID int64) ([]syncRecord, error) {
	raws, err := s.listRecords("discord_sync", fmt.Sprintf("comment_id=%d", commentID))
	if err != nil {
		return nil, err
	}
	return decodeList[syncRecord](raws)
}

func (s *store) deleteSync(id string) error {
	_, err := s.do(http.MethodDelete, "/api/collections/discord_sync/records/"+id, nil)
	return err
}
