package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

func TestSyncIndexSetGetDrop(t *testing.T) {
	idx := newSyncIndex()
	if _, ok := idx.get("1"); ok {
		t.Fatal("empty index must miss")
	}
	idx.set(threadRecord{ThreadID: "1", Repo: "o/r", IssueNumber: 3, SyncEnabled: true})
	rec, ok := idx.get("1")
	if !ok || rec.IssueNumber != 3 {
		t.Fatalf("got %+v %v", rec, ok)
	}
	idx.set(threadRecord{ThreadID: "2", SyncEnabled: false})
	if _, ok := idx.get("2"); ok {
		t.Fatal("rows with sync off must not be indexed")
	}
	idx.drop("1")
	if _, ok := idx.get("1"); ok {
		t.Fatal("dropped row still present")
	}
}

func TestSyncIndexReplace(t *testing.T) {
	idx := newSyncIndex()
	idx.set(threadRecord{ThreadID: "1", SyncEnabled: true})
	idx.replace([]threadRecord{{ThreadID: "2", SyncEnabled: true}})
	if _, ok := idx.get("1"); ok {
		t.Fatal("replace must drop stale rows")
	}
	if _, ok := idx.get("2"); !ok {
		t.Fatal("replace must add new rows")
	}
}

func TestBatcherDue(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)
	b := newBatcher(60 * time.Second)
	b.add(&discordgo.Message{ID: "1", ChannelID: "A"}, t0)
	b.add(&discordgo.Message{ID: "2", ChannelID: "A"}, t0.Add(20*time.Second))
	b.add(&discordgo.Message{ID: "3", ChannelID: "B"}, t0.Add(30*time.Second))

	if got := b.due(t0.Add(59 * time.Second)); len(got) != 0 {
		t.Fatalf("nothing is due yet, got %v", got)
	}
	got := b.due(t0.Add(61 * time.Second))
	if len(got) != 1 || len(got["A"]) != 2 || got["A"][1].ID != "2" {
		t.Fatalf("want thread A with two messages, got %v", got)
	}
	if got := b.due(t0.Add(91 * time.Second)); len(got) != 1 || len(got["B"]) != 1 {
		t.Fatalf("want thread B later, got %v", got)
	}
	if got := b.due(t0.Add(200 * time.Second)); len(got) != 0 {
		t.Fatal("flushed batches must not come back")
	}
}

func TestBatcherDropsDeletedAndEditedBeforeFlush(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)
	b := newBatcher(60 * time.Second)
	b.add(&discordgo.Message{ID: "1", ChannelID: "A", Content: "spam"}, t0)
	b.add(&discordgo.Message{ID: "2", ChannelID: "A", Content: "old"}, t0)
	b.remove("A", "1")
	b.update(&discordgo.Message{ID: "2", ChannelID: "A", Content: "new"})
	b.update(&discordgo.Message{ID: "2", ChannelID: "A"})

	got := b.due(t0.Add(61 * time.Second))
	if len(got["A"]) != 1 || got["A"][0].ID != "2" || got["A"][0].Content != "new" {
		t.Fatalf("want only message 2 with its edit, got %+v", got["A"])
	}

	b.add(&discordgo.Message{ID: "3", ChannelID: "B"}, t0)
	b.remove("B", "3")
	if got := b.due(t0.Add(200 * time.Second)); len(got) != 0 {
		t.Fatalf("a batch emptied by deletions must not flush, got %v", got)
	}
}

func TestPlainContentMentions(t *testing.T) {
	m := &discordgo.Message{
		Content:  "hi <@123> and <@!456>, see <#789>",
		Mentions: []*discordgo.User{{ID: "123", Username: "alice"}, {ID: "456", Username: "bob"}},
	}
	if got := plainContent(m); got != "hi `@alice` and `@bob`, see <#789>" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderBatchQuotesAndImages(t *testing.T) {
	msgs := []*discordgo.Message{
		msg(1, "alice", "hi\nthere", 3, &discordgo.MessageAttachment{Filename: "shot.png", URL: "https://cdn/shot.png"}),
		msg(2, "bob", "yo", 4, &discordgo.MessageAttachment{Filename: "debug.log", URL: "https://cdn/debug.log"}),
	}
	body := renderBatch(msgs, "g", "t", map[string]string{"https://cdn/shot.png": "https://pb/f/x/shot.png"})
	for _, want := range []string{
		"**alice** via Discord · 2026-09-10 21:03 UTC\n> hi\n> there\n> ![shot.png](https://pb/f/x/shot.png)\n",
		"**bob** via Discord · 2026-09-10 21:04 UTC\n> yo\n> 📎 [debug.log](https://cdn/debug.log) · [view in Discord](https://discord.com/channels/g/t/2)\n",
		"[Discord thread](https://discord.com/channels/g/t)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestSplitDiscordPrefersNewline(t *testing.T) {
	text := strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 30)
	chunks := splitDiscord(text, 40)
	if len(chunks) != 2 || chunks[0] != strings.Repeat("a", 30) || chunks[1] != strings.Repeat("b", 30) {
		t.Fatalf("got %q", chunks)
	}
	long := strings.Repeat("ä", 95)
	chunks = splitDiscord(long, 40)
	if len(chunks) != 3 || utf8.RuneCountInString(chunks[0]) != 40 || utf8.RuneCountInString(chunks[2]) != 15 {
		t.Fatalf("rune-based split expected, got %d chunks", len(chunks))
	}
	if got := splitDiscord("short", 40); len(got) != 1 || got[0] != "short" {
		t.Fatalf("got %q", got)
	}
}

func TestGithubToDiscordStripsMarkers(t *testing.T) {
	chunks := githubToDiscord("alice", "https://gh/c/1", "text\n<!-- discord-thread-id: 1 -->\nmore")
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	if chunks[0] != "**alice** on GitHub · <https://gh/c/1>\ntext\nmore" {
		t.Fatalf("got %q", chunks[0])
	}
}
