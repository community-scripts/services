package main

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	syncCommandName   = "sync"
	originDiscord     = "discord"
	originGitHub      = "github"
	discordMessageMax = 2000
)

type syncIndex struct {
	mu       sync.RWMutex
	byThread map[string]threadRecord
}

func newSyncIndex() *syncIndex {
	return &syncIndex{byThread: map[string]threadRecord{}}
}

func (x *syncIndex) get(threadID string) (threadRecord, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	rec, ok := x.byThread[threadID]
	return rec, ok
}

func (x *syncIndex) set(rec threadRecord) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if rec.SyncEnabled {
		x.byThread[rec.ThreadID] = rec
	} else {
		delete(x.byThread, rec.ThreadID)
	}
}

func (x *syncIndex) drop(threadID string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.byThread, threadID)
}

func (x *syncIndex) replace(recs []threadRecord) {
	next := make(map[string]threadRecord, len(recs))
	for _, rec := range recs {
		if rec.SyncEnabled {
			next[rec.ThreadID] = rec
		}
	}
	x.mu.Lock()
	x.byThread = next
	x.mu.Unlock()
}

func (b *bot) reloadIndex() {
	recs, err := b.store.syncedThreads()
	if err != nil {
		log.Printf("discord bot: load synced threads: %v", err)
		return
	}
	b.index.replace(recs)
}

// Rows edited by hand in PocketBase show up without a restart.
func (b *bot) refreshLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			b.reloadIndex()
		}
	}
}

func mirrorOnNotice(issueURL string) string {
	return "🔁 Messages in this thread are now mirrored to " + issueURL + " and are public there. A moderator can stop this with `/sync off`."
}

type pending struct {
	since time.Time
	msgs  []*discordgo.Message
}

// Messages are mirrored one comment per thread and window, not one per message.
type batcher struct {
	mu       sync.Mutex
	window   time.Duration
	byThread map[string]*pending
}

func newBatcher(window time.Duration) *batcher {
	return &batcher{window: window, byThread: map[string]*pending{}}
}

func (b *batcher) add(m *discordgo.Message, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.byThread[m.ChannelID]
	if p == nil {
		p = &pending{since: now}
		b.byThread[m.ChannelID] = p
	}
	p.msgs = append(p.msgs, m)
}

func (b *batcher) due(now time.Time) map[string][]*discordgo.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string][]*discordgo.Message{}
	for id, p := range b.byThread {
		if now.Sub(p.since) >= b.window {
			out[id] = p.msgs
			delete(b.byThread, id)
		}
	}
	return out
}

var (
	userMention = regexp.MustCompile(`<@!?(\d+)>`)
	ownLineHTML = regexp.MustCompile(`(?ms)^[ \t]*<!--.*?-->[ \t]*(\n|$)`)
	inlineHTML  = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// A Discord mention is a GitHub @-handle for whoever owns that name there.
func plainContent(m *discordgo.Message) string {
	names := map[string]string{}
	for _, u := range m.Mentions {
		names[u.ID] = u.Username
	}
	return userMention.ReplaceAllStringFunc(m.Content, func(s string) string {
		id := userMention.FindStringSubmatch(s)[1]
		if name, ok := names[id]; ok {
			return "`@" + name + "`"
		}
		return "`@" + id + "`"
	})
}

func renderBatch(msgs []*discordgo.Message, guildID, threadID string, uploads map[string]string) string {
	var b strings.Builder
	for _, m := range msgs {
		author := "unknown"
		if m.Author != nil {
			author = m.Author.Username
		}
		fmt.Fprintf(&b, "**%s** via Discord · %s\n", author, m.Timestamp.UTC().Format("2006-01-02 15:04 UTC"))
		if text := strings.TrimSpace(plainContent(m)); text != "" {
			b.WriteString(quote(text) + "\n")
		}
		for _, a := range m.Attachments {
			if permanent := uploads[a.URL]; permanent != "" {
				fmt.Fprintf(&b, "> ![%s](%s)\n", a.Filename, permanent)
				continue
			}
			fmt.Fprintf(&b, "> 📎 [%s](%s) · [view in Discord](https://discord.com/channels/%s/%s/%s)\n", a.Filename, a.URL, guildID, threadID, m.ID)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "<sub>Mirrored from the [Discord thread](%s).</sub>\n", threadURL(guildID, threadID))
	return b.String()
}

func stripHTMLComments(s string) string {
	return inlineHTML.ReplaceAllString(ownLineHTML.ReplaceAllString(s, ""), "")
}

func splitDiscord(s string, limit int) []string {
	var chunks []string
	runes := []rune(s)
	for len(runes) > limit {
		cut := limit
		for i := limit; i > 0; i-- {
			if runes[i] == '\n' {
				cut = i
				break
			}
		}
		if chunk := strings.TrimRight(string(runes[:cut]), "\n"); chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = runes[cut:]
		for len(runes) > 0 && runes[0] == '\n' {
			runes = runes[1:]
		}
	}
	if rest := string(runes); rest != "" {
		chunks = append(chunks, rest)
	}
	return chunks
}

func githubToDiscord(login, htmlURL, body string) []string {
	header := fmt.Sprintf("**%s** on GitHub · <%s>", login, htmlURL)
	text := strings.TrimSpace(stripHTMLComments(body))
	if text == "" {
		return []string{header}
	}
	return splitDiscord(header+"\n"+text, discordMessageMax)
}

func sortByMessageID(rows []syncRecord) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].MessageID, rows[j].MessageID
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	})
}

// ---- Discord -> GitHub

func (b *bot) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot || !isUserMessage(m.Message) {
		return
	}
	if _, ok := b.index.get(m.ChannelID); !ok {
		return
	}
	b.batch.add(m.Message, time.Now())
}

func (b *bot) flushLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			for threadID, msgs := range b.batch.due(now) {
				b.flushBatch(threadID, msgs)
			}
		}
	}
}

func (b *bot) flushBatch(threadID string, msgs []*discordgo.Message) {
	rec, ok := b.index.get(threadID)
	if !ok {
		return
	}
	body := renderBatch(msgs, rec.GuildID, threadID, b.copyImages(msgs))
	c, err := b.github.createComment(rec.Repo, rec.IssueNumber, body)
	if err != nil {
		log.Printf("discord bot: mirror thread %s to %s#%d: %v", threadID, rec.Repo, rec.IssueNumber, err)
		return
	}
	rows := make([]syncRecord, 0, len(msgs))
	for _, m := range msgs {
		rows = append(rows, syncRecord{ThreadID: threadID, MessageID: m.ID, CommentID: c.ID, Origin: originDiscord})
	}
	if err := b.store.recordSync(rows); err != nil {
		log.Printf("discord bot: record mirrored messages for comment %d: %v", c.ID, err)
	}
}

func (b *bot) onMessageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate) {
	b.refreshMirroredComment(s, m.ChannelID, m.ID, false)
}

func (b *bot) onMessageDelete(s *discordgo.Session, m *discordgo.MessageDelete) {
	b.refreshMirroredComment(s, m.ChannelID, m.ID, true)
}

// A comment holds a whole batch, so it is rebuilt from whatever messages remain.
func (b *bot) refreshMirroredComment(s *discordgo.Session, threadID, messageID string, deleted bool) {
	row, err := b.store.syncByMessage(messageID)
	if err != nil {
		log.Printf("discord bot: look up message %s: %v", messageID, err)
		return
	}
	if row == nil || row.Origin != originDiscord {
		return
	}
	rec, err := b.store.issueForThread(threadID)
	if err != nil || rec == nil {
		return
	}
	rows, err := b.store.syncByComment(row.CommentID)
	if err != nil {
		log.Printf("discord bot: look up comment %d: %v", row.CommentID, err)
		return
	}
	sortByMessageID(rows)

	var msgs []*discordgo.Message
	for _, r := range rows {
		if deleted && r.MessageID == messageID {
			b.dropSync(r)
			continue
		}
		m, err := s.ChannelMessage(threadID, r.MessageID)
		if err != nil {
			b.dropSync(r)
			continue
		}
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 {
		if err := b.github.deleteComment(rec.Repo, row.CommentID); err != nil {
			log.Printf("discord bot: delete comment %d: %v", row.CommentID, err)
		}
		return
	}
	body := renderBatch(msgs, rec.GuildID, threadID, b.copyImages(msgs))
	if err := b.github.updateComment(rec.Repo, row.CommentID, body); err != nil {
		log.Printf("discord bot: update comment %d: %v", row.CommentID, err)
	}
}

func (b *bot) dropSync(r syncRecord) {
	if err := b.store.deleteSync(r.ID); err != nil {
		log.Printf("discord bot: delete sync row %s: %v", r.ID, err)
	}
}

// ---- GitHub -> Discord

func (b *bot) handleIssueComment(e issueCommentEvent) {
	if app := e.Comment.PerformedViaGitHubApp; app != nil && strconv.FormatInt(app.ID, 10) == b.cfg.githubAppID {
		return
	}
	rec, err := b.store.threadByIssue(e.Repository.FullName, e.Issue.Number)
	if err != nil {
		log.Printf("discord bot: thread for %s#%d: %v", e.Repository.FullName, e.Issue.Number, err)
		return
	}
	if rec == nil || !rec.SyncEnabled {
		return
	}
	rows, err := b.store.syncByComment(e.Comment.ID)
	if err != nil {
		log.Printf("discord bot: look up comment %d: %v", e.Comment.ID, err)
		return
	}
	sortByMessageID(rows)

	switch e.Action {
	case "created":
		if len(rows) == 0 {
			b.mirrorComment(rec, e.Comment)
		}
	case "edited":
		chunks := githubToDiscord(e.Comment.User.Login, e.Comment.HTMLURL, e.Comment.Body)
		if len(rows) == len(chunks) {
			for i, r := range rows {
				if err := b.threads.edit(rec.ThreadID, r.MessageID, chunks[i]); err != nil {
					log.Printf("discord bot: edit message %s: %v", r.MessageID, err)
				}
			}
			return
		}
		b.removeMirrored(rec, rows)
		b.mirrorComment(rec, e.Comment)
	case "deleted":
		b.removeMirrored(rec, rows)
	}
}

func (b *bot) mirrorComment(rec *threadRecord, c ghComment) {
	var rows []syncRecord
	for _, chunk := range githubToDiscord(c.User.Login, c.HTMLURL, c.Body) {
		id, err := b.threads.post(rec.ThreadID, chunk)
		if err != nil {
			log.Printf("discord bot: mirror comment %d to thread %s: %v", c.ID, rec.ThreadID, err)
			break
		}
		rows = append(rows, syncRecord{ThreadID: rec.ThreadID, MessageID: id, CommentID: c.ID, Origin: originGitHub})
	}
	if err := b.store.recordSync(rows); err != nil {
		log.Printf("discord bot: record mirrored comment %d: %v", c.ID, err)
	}
}

func (b *bot) removeMirrored(rec *threadRecord, rows []syncRecord) {
	for _, r := range rows {
		if err := b.threads.remove(rec.ThreadID, r.MessageID); err != nil {
			log.Printf("discord bot: delete message %s: %v", r.MessageID, err)
		}
		b.dropSync(r)
	}
}

// ---- /sync on|off

func syncCommand(permissions *int64) *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:                     syncCommandName,
		Type:                     discordgo.ChatApplicationCommand,
		Description:              "Mirror this thread to its GitHub issue",
		DefaultMemberPermissions: permissions,
		Options: []*discordgo.ApplicationCommandOption{{
			Type:        discordgo.ApplicationCommandOptionString,
			Name:        "mode",
			Description: "on or off",
			Required:    true,
			Choices: []*discordgo.ApplicationCommandOptionChoice{
				{Name: "on", Value: "on"},
				{Name: "off", Value: "off"},
			},
		}},
	}
}

func (b *bot) handleSyncCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("discord bot: defer response: %v", err)
		return
	}
	message, err := b.toggleSync(i)
	if err != nil {
		log.Printf("discord bot: /sync: %v", err)
		message = "Changing the mirroring failed: " + err.Error()
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &message}); err != nil {
		log.Printf("discord bot: edit response: %v", err)
	}
}

func (b *bot) toggleSync(i *discordgo.InteractionCreate) (string, error) {
	if i.GuildID == "" {
		return "This command only works inside a server.", nil
	}
	settings, err := b.store.guildConfig(i.GuildID)
	if err != nil {
		return "", fmt.Errorf("read guild config: %w", err)
	}
	if settings == nil || !settings.Enabled {
		return "The bot is not configured for this server.", nil
	}
	if i.Member == nil || !hasAllowedRole(i.Member.Roles, settings.AllowedRoleIDs) {
		return "You do not have a role that is allowed to change mirroring.", nil
	}
	rec, err := b.store.issueForThread(i.ChannelID)
	if err != nil {
		return "", fmt.Errorf("look up thread: %w", err)
	}
	if rec == nil {
		return "No GitHub issue is linked to this thread. Use **Create GitHub Issue** first.", nil
	}
	options := i.ApplicationCommandData().Options
	on := len(options) > 0 && options[0].StringValue() == "on"
	if rec.SyncEnabled == on {
		if on {
			return "Mirroring is already on for " + rec.IssueURL, nil
		}
		return "Mirroring is already off.", nil
	}
	if err := b.store.updateIssueRecord(rec.ID, map[string]any{"sync_enabled": on}); err != nil {
		return "", err
	}
	rec.SyncEnabled = on
	b.index.set(*rec)

	if on {
		id, err := b.threads.post(rec.ThreadID, mirrorOnNotice(rec.IssueURL))
		if err != nil {
			return "", err
		}
		if err := b.threads.pin(rec.ThreadID, id); err != nil {
			log.Printf("discord bot: pin notice in %s: %v", rec.ThreadID, err)
		}
		return "Mirroring is on for " + rec.IssueURL, nil
	}
	if _, err := b.threads.post(rec.ThreadID, "⏹️ Mirroring to GitHub stopped."); err != nil {
		return "", err
	}
	return "Mirroring is off.", nil
}
