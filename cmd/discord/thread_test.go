package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

type fakeFetcher struct {
	messages []*discordgo.Message // oldest first
	calls    int
}

// Mirrors the real API: newest first, capped at the requested page size, paged
// backwards via beforeID.
func (f *fakeFetcher) ChannelMessages(_ string, limit int, beforeID, _, _ string, _ ...discordgo.RequestOption) ([]*discordgo.Message, error) {
	f.calls++
	var pool []*discordgo.Message
	for i := len(f.messages) - 1; i >= 0; i-- {
		pool = append(pool, f.messages[i])
	}
	if beforeID != "" {
		cutoff, _ := strconv.Atoi(beforeID)
		var filtered []*discordgo.Message
		for _, m := range pool {
			if id, _ := strconv.Atoi(m.ID); id < cutoff {
				filtered = append(filtered, m)
			}
		}
		pool = filtered
	}
	if len(pool) > limit {
		pool = pool[:limit]
	}
	return pool, nil
}

func msg(id int, author, content string, minute int, attachments ...*discordgo.MessageAttachment) *discordgo.Message {
	return &discordgo.Message{
		ID:          strconv.Itoa(id),
		Type:        discordgo.MessageTypeDefault,
		Content:     content,
		Timestamp:   time.Date(2026, 9, 10, 21, minute, 0, 0, time.UTC),
		Author:      &discordgo.User{Username: author},
		Attachments: attachments,
	}
}

func manyMessages(n int) []*discordgo.Message {
	out := make([]*discordgo.Message, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, msg(1000+i, "user", fmt.Sprintf("message %d", i), i%60))
	}
	return out
}

func TestCollectThreadPagesPastAPILimit(t *testing.T) {
	all := manyMessages(250)
	all[5].Type = discordgo.MessageTypeGuildMemberJoin

	got, err := collectThread(&fakeFetcher{messages: all}, "c", 500)
	if err != nil {
		t.Fatalf("collectThread: %v", err)
	}
	if len(got) != 249 {
		t.Fatalf("got %d messages, want 249 (one system message dropped)", len(got))
	}
	if got[0].ID != "1000" || got[len(got)-1].ID != "1249" {
		t.Fatalf("not oldest-first: %s..%s", got[0].ID, got[len(got)-1].ID)
	}
}

func TestCollectThreadHonoursLimit(t *testing.T) {
	got, err := collectThread(&fakeFetcher{messages: manyMessages(250)}, "c", 10)
	if err != nil {
		t.Fatalf("collectThread: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d messages, want 10", len(got))
	}
}

func TestCollectThreadStopsOnShortPage(t *testing.T) {
	f := &fakeFetcher{messages: manyMessages(30)}
	if _, err := collectThread(f, "c", 500); err != nil {
		t.Fatalf("collectThread: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("made %d API calls for a single short page, want 1", f.calls)
	}
}

func TestDeriveTitle(t *testing.T) {
	first := msg(1, "z", "Stuck at Configuring\nmore text", 0)

	if got := deriveTitle("Docmost update 0.95.0 -> 0.96.0", first); got != "Docmost update 0.95.0 -> 0.96.0" {
		t.Fatalf("thread name should win, got %q", got)
	}
	if got := deriveTitle("", first); got != "Stuck at Configuring" {
		t.Fatalf("fallback to first line, got %q", got)
	}
	if got := deriveTitle("", msg(1, "z", "", 0)); got != "Discord report" {
		t.Fatalf("final fallback, got %q", got)
	}
}

func TestRenderIssueBody(t *testing.T) {
	messages := []*discordgo.Message{
		msg(1, "Zazeur", `Update hangs at "Configuring Docmost".`, 50,
			&discordgo.MessageAttachment{Filename: "shot.png", URL: "https://cdn.discordapp.com/a/shot.png?ex=1"}),
		msg(2, "Mick", "Looks like a source build.", 51),
	}

	body := renderIssueBody(renderInput{
		Messages:  messages,
		GuildID:   "000000000000000001",
		ChannelID: "000000000000000002",
		ThreadURL: "https://discord.com/channels/000000000000000001/000000000000000002",
		Moderator: "MickLesk",
	})

	for _, want := range []string{
		`> Update hangs at "Configuring Docmost".`,
		"#### Zazeur — 2026-09-10 21:50 UTC",
		"[shot.png](https://cdn.discordapp.com/a/shot.png?ex=1)",
		"/000000000000000001/000000000000000002/1)",
		"expire after roughly 24 hours",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	if strings.Contains(body, "@MickLesk") {
		t.Error("moderator must not be rendered as a GitHub mention")
	}
}

func TestRenderIssueBodyWithoutAttachments(t *testing.T) {
	body := renderIssueBody(renderInput{
		Messages:  []*discordgo.Message{msg(9, "a", "hi", 0)},
		GuildID:   "1",
		ChannelID: "2",
		ThreadURL: "https://discord.com/channels/1/2",
		Moderator: "m",
	})
	if strings.Contains(body, "expire after roughly") {
		t.Error("expiry note should only appear when something is attached")
	}
}

func TestRenderIssueBodyTruncates(t *testing.T) {
	messages := make([]*discordgo.Message, 0, 400)
	for i := 0; i < 400; i++ {
		messages = append(messages, msg(i, "u", strings.Repeat("x", 400), 0))
	}

	body := renderIssueBody(renderInput{
		Messages:  messages,
		GuildID:   "1",
		ChannelID: "2",
		ThreadURL: "https://discord.com/channels/1/2",
		Moderator: "m",
	})

	if len(body) > maxIssueBody {
		t.Fatalf("body is %d bytes, over the %d limit", len(body), maxIssueBody)
	}
	if !strings.Contains(body, "Truncated:") {
		t.Error("truncation should be signposted")
	}
}

func TestSplitRepo(t *testing.T) {
	owner, repo, err := splitRepo("community-scripts/ProxmoxVE")
	if err != nil || owner != "community-scripts" || repo != "ProxmoxVE" {
		t.Fatalf("got %q/%q err=%v", owner, repo, err)
	}
	for _, bad := range []string{"ProxmoxVE", "", "a/b/c", "/b", "a/"} {
		if _, _, err := splitRepo(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestHasAllowedRole(t *testing.T) {
	allowed := []string{"000000000000000002", "000000000000000003"}

	if !hasAllowedRole([]string{"000000000000000009", "000000000000000003"}, allowed) {
		t.Error("a single matching role should be enough")
	}
	if hasAllowedRole([]string{"000000000000000009"}, allowed) {
		t.Error("non-matching roles must be rejected")
	}
	if hasAllowedRole([]string{"000000000000000002"}, nil) {
		t.Error("an empty allow list must reject everyone, not admit them")
	}
	if hasAllowedRole(nil, allowed) {
		t.Error("a member with no roles must be rejected")
	}
}

func TestDecodeStringList(t *testing.T) {
	// PocketBase returns json fields either as an array or as a string holding one.
	if got := decodeStringList(json.RawMessage(`["a","b"]`)); len(got) != 2 || got[0] != "a" {
		t.Errorf("array form: %v", got)
	}
	if got := decodeStringList(json.RawMessage(`"[\"a\",\"b\"]"`)); len(got) != 2 || got[1] != "b" {
		t.Errorf("string form: %v", got)
	}
	if got := decodeStringList(nil); got != nil {
		t.Errorf("nil should stay nil, got %v", got)
	}
	if got := decodeStringList(json.RawMessage(`"nonsense"`)); got != nil {
		t.Errorf("garbage should not panic or invent entries, got %v", got)
	}
}

func TestNormalisePEM(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----"

	if got := normalisePEM(strings.ReplaceAll(pem, "\n", "\\n")); got != pem {
		t.Errorf("escaped newlines should be restored, got %q", got)
	}
	if got := normalisePEM("LS0tLS1CRUdJTiBSU0EgUFJJVkFURSBLRVktLS0tLQo="); !strings.Contains(got, "BEGIN RSA PRIVATE KEY") {
		t.Errorf("base64 should be decoded, got %q", got)
	}
	if got := normalisePEM(""); got != "" {
		t.Errorf("empty stays empty, got %q", got)
	}
}
