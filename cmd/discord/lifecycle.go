package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

type lifecycleMode string

const (
	modeReady   lifecycleMode = "ready"
	modeHold    lifecycleMode = "hold"
	modeDefer   lifecycleMode = "defer"
	modeMigrate lifecycleMode = "migrate"
	modeDelete  lifecycleMode = "delete"
	modeNoop    lifecycleMode = "noop"
)

const (
	labelReady     = "Ready For Testing"
	labelDeferred  = "deferred"
	labelMigration = "Migration To ProxmoxVE"
	labelMigrating = "Started Migration To ProxmoxVE"

	tagTesting   = "Testing"
	tagHold      = "On Hold"
	tagDeferred  = "Deferred"
	tagMigrating = "Migrating"

	mirrorNote = "Messages in this thread are mirrored to the GitHub issue above and are public there."
)

var (
	botLogin  = regexp.MustCompile(`\[bot\]$|^github-actions$`)
	prRef     = regexp.MustCompile(`community-scripts/ProxmoxVE#([0-9]+)`)
	markerRef = regexp.MustCompile(`discord-thread-id: ([0-9]+)`)
	rawClient = &http.Client{Timeout: 20 * time.Second}
)

func routeIssueEvent(action, label string, labels []string) lifecycleMode {
	if action == "closed" {
		return modeDelete
	}
	switch action + ":" + label {
	case "labeled:" + labelReady:
		return modeReady
	case "labeled:" + labelDeferred:
		return modeDefer
	case "labeled:" + labelMigrating:
		return modeMigrate
	case "unlabeled:" + labelReady:
		for _, l := range labels {
			if l == labelDeferred || l == labelMigration || l == labelMigrating {
				return modeNoop
			}
		}
		return modeHold
	}
	return modeNoop
}

func lastHumanUpdate(comments []ghComment, body string) string {
	text := ""
	for _, c := range comments {
		if !botLogin.MatchString(c.User.Login) && c.Body != "" {
			text = c.Body
		}
	}
	if text == "" {
		text = body
	}
	if len(text) > 800 {
		n := 800
		for !utf8.ValidString(text[:n]) {
			n--
		}
		text = text[:n] + "\n…\n"
	}
	return text
}

func lastMatch(re *regexp.Regexp, comments []ghComment) string {
	last := ""
	for _, c := range comments {
		for _, m := range re.FindAllStringSubmatch(c.Body, -1) {
			last = m[1]
		}
	}
	return last
}

func prFromComments(comments []ghComment) string     { return lastMatch(prRef, comments) }
func markerFromComments(comments []ghComment) string { return lastMatch(markerRef, comments) }

func threadName(title string) string {
	return truncate("Wanted Tester for "+title, 100)
}

func threadURL(guildID, threadID string) string {
	return fmt.Sprintf("https://discord.com/channels/%s/%s", guildID, threadID)
}

func resumedNotice(testing, issueURL string) string {
	return "▶️ **Testing has resumed** — the `Ready For Testing` label is back on the issue.\n\n" + testing + "\n🔗 " + issueURL + "\n"
}

func pausedNotice(lastUpdate, issueURL string) string {
	return "⚠️ **Testing paused** — the `Ready For Testing` label was removed because there may be problems with this script.\n\n" +
		"The thread stays open, but please hold off on testing the version above until it is marked ready again.\n\n" +
		"Latest update from the issue:\n\n" + lastUpdate + "\n\n🔗 " + issueURL + "\n"
}

func deferredNotice(lastUpdate, issueURL string) string {
	return "⏸️ **This script has been deferred** and is postponed indefinitely.\n\n" +
		"This thread is locked. It will reopen if the `Ready For Testing` label is added to the issue again.\n\n" +
		"Latest update from the issue:\n\n" + lastUpdate + "\n\n🔗 " + issueURL + "\n"
}

func migrationNotice(pr, issueURL string) string {
	var b strings.Builder
	b.WriteString("🚚 **Migration to ProxmoxVE has started.**\n\n")
	if pr != "" {
		fmt.Fprintf(&b, "A pull request is open in the main repository: https://github.com/community-scripts/ProxmoxVE/pull/%s\n\n", pr)
	}
	b.WriteString("Testing in DevScripts is finished — this thread is locked and will be removed once the migration is complete. Please continue the discussion on the pull request.\n\n🔗 " + issueURL + "\n")
	return b.String()
}

func fetchRaw(url string) ([]byte, bool) {
	resp, err := rawClient.Get(url)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return data, err == nil
}

type threadAPI interface {
	selfID() string
	create(channelID, name, content, tag string) (string, error)
	post(threadID, content string) (string, error)
	edit(threadID, messageID, content string) error
	remove(threadID, messageID string) error
	pin(threadID, messageID string) error
	setState(threadID string, locked, archived bool, tag string) error
	exists(threadID string) bool
	locked(threadID string) (bool, error)
	delete(threadID string) error
}

type discordThreads struct{ s *discordgo.Session }

var noMentions = &discordgo.MessageAllowedMentions{}

func clipContent(s string) string {
	if utf8.RuneCountInString(s) <= 2000 {
		return s
	}
	return truncate(s, 1990) + "\n…"
}

func (d *discordThreads) selfID() string { return d.s.State.User.ID }

func (d *discordThreads) tagID(channelID, name string) string {
	if name == "" {
		return ""
	}
	ch, err := d.s.Channel(channelID)
	if err != nil {
		return ""
	}
	for _, t := range ch.AvailableTags {
		if strings.EqualFold(t.Name, name) {
			return t.ID
		}
	}
	return ""
}

func (d *discordThreads) create(channelID, name, content, tag string) (string, error) {
	data := &discordgo.ThreadStart{Name: truncate(name, 100)}
	if id := d.tagID(channelID, tag); id != "" {
		data.AppliedTags = []string{id}
	}
	ch, err := d.s.ForumThreadStartComplex(channelID, data, &discordgo.MessageSend{Content: clipContent(content), AllowedMentions: noMentions})
	if err != nil {
		return "", err
	}
	return ch.ID, nil
}

// An auto-archived forum post rejects messages, so every post unarchives first.
func (d *discordThreads) post(threadID, content string) (string, error) {
	off := false
	if _, err := d.s.ChannelEditComplex(threadID, &discordgo.ChannelEdit{Archived: &off, Locked: &off}); err != nil {
		return "", err
	}
	m, err := d.s.ChannelMessageSendComplex(threadID, &discordgo.MessageSend{Content: clipContent(content), AllowedMentions: noMentions})
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

func (d *discordThreads) edit(threadID, messageID, content string) error {
	text := clipContent(content)
	_, err := d.s.ChannelMessageEditComplex(&discordgo.MessageEdit{Channel: threadID, ID: messageID, Content: &text, AllowedMentions: noMentions})
	return err
}

func (d *discordThreads) remove(threadID, messageID string) error {
	return d.s.ChannelMessageDelete(threadID, messageID)
}

func (d *discordThreads) pin(threadID, messageID string) error {
	return d.s.ChannelMessagePin(threadID, messageID)
}

func (d *discordThreads) setState(threadID string, locked, archived bool, tag string) error {
	edit := &discordgo.ChannelEdit{Locked: &locked, Archived: &archived}
	if tag != "" {
		if ch, err := d.s.Channel(threadID); err == nil {
			if id := d.tagID(ch.ParentID, tag); id != "" {
				edit.AppliedTags = &[]string{id}
			}
		}
	}
	_, err := d.s.ChannelEditComplex(threadID, edit)
	return err
}

func (d *discordThreads) exists(threadID string) bool {
	_, err := d.s.Channel(threadID)
	return err == nil
}

func (d *discordThreads) locked(threadID string) (bool, error) {
	ch, err := d.s.Channel(threadID)
	if err != nil {
		return false, err
	}
	return ch.ThreadMetadata != nil && ch.ThreadMetadata.Locked, nil
}

func (d *discordThreads) delete(threadID string) error {
	_, err := d.s.ChannelDelete(threadID)
	return err
}

type lifecycleContext struct {
	cfg      *guildSettings
	repo     string
	issue    ghIssue
	rec      *threadRecord
	exists   bool
	comments []ghComment
}

func (b *bot) handleIssueEvent(repo, action, label string, issue ghIssue) {
	mode := routeIssueEvent(action, label, issue.labelNames())
	if mode == modeNoop {
		return
	}
	cfg, err := b.store.configByDevRepo(repo)
	if err != nil {
		log.Printf("discord bot: lifecycle config for %s: %v", repo, err)
		return
	}
	if cfg == nil || !cfg.Enabled || cfg.TestingChannelID == "" {
		log.Printf("discord bot: no lifecycle config for %s, ignoring %s", repo, mode)
		return
	}

	lc := &lifecycleContext{cfg: cfg, repo: repo, issue: issue}
	if lc.comments, err = b.github.comments(repo, issue.Number); err != nil {
		log.Printf("discord bot: %s #%d comments: %v", repo, issue.Number, err)
		return
	}
	if err := b.resolveThread(lc); err != nil {
		log.Printf("discord bot: %s #%d thread: %v", repo, issue.Number, err)
		return
	}
	log.Printf("discord bot: %s #%d: %s (thread %v)", repo, issue.Number, mode, lc.exists)

	switch mode {
	case modeReady:
		err = b.ready(lc)
	case modeHold:
		err = b.hold(lc)
	case modeDefer:
		err = b.deferTesting(lc)
	case modeMigrate:
		err = b.migrate(lc)
	case modeDelete:
		err = b.deleteThread(lc)
	}
	if err != nil {
		log.Printf("discord bot: %s #%d %s: %v", repo, issue.Number, mode, err)
	}
}

// The row is authoritative; the marker comment only covers threads made before
// the bot kept rows, and gets a row of its own once seen.
func (b *bot) resolveThread(lc *lifecycleContext) error {
	rec, err := b.store.threadByIssue(lc.repo, lc.issue.Number)
	if err != nil {
		return err
	}
	if rec != nil {
		lc.rec, lc.exists = rec, b.threads.exists(rec.ThreadID)
		return nil
	}
	id := markerFromComments(lc.comments)
	if id == "" || !b.threads.exists(id) {
		return nil
	}
	rec, err = b.recordMarkedThread(lc.cfg, lc.issue, id)
	if err != nil {
		return err
	}
	lc.rec, lc.exists = rec, true
	return nil
}

func stateFromLabels(labels []string) string {
	for _, l := range labels {
		switch l {
		case labelMigration, labelMigrating:
			return "migrating"
		case labelDeferred:
			return "deferred"
		}
	}
	for _, l := range labels {
		if l == labelReady {
			return "testing"
		}
	}
	return "hold"
}

func (b *bot) threadRow(lc *lifecycleContext, threadID, state string, sync bool) map[string]any {
	return map[string]any{
		"guild_id":        lc.cfg.GuildID,
		"thread_id":       threadID,
		"message_id":      threadID,
		"repo":            lc.repo,
		"issue_number":    lc.issue.Number,
		"issue_url":       lc.issue.HTMLURL,
		"created_by_id":   b.threads.selfID(),
		"created_by_name": "lifecycle",
		"message_count":   0,
		"kind":            "testing",
		"slug":            slugFromName(lc.issue.Title),
		"state":           state,
		"sync_enabled":    sync,
	}
}

func (b *bot) setThreadState(lc *lifecycleContext, state string, sync bool) error {
	if lc.rec == nil {
		return nil
	}
	if err := b.store.updateIssueRecord(lc.rec.ID, map[string]any{"state": state, "sync_enabled": sync}); err != nil {
		return err
	}
	lc.rec.State, lc.rec.SyncEnabled = state, sync
	if sync {
		b.index.set(*lc.rec)
	} else {
		b.index.drop(lc.rec.ThreadID)
	}
	return nil
}

func (b *bot) ready(lc *lifecycleContext) error {
	rawBase := "https://raw.githubusercontent.com/" + lc.repo + "/main"
	info, err := resolveScript(lc.issue.Title, lc.issue.Body, func(p string) ([]byte, bool) {
		return fetchRaw(rawBase + "/" + p)
	})
	if err != nil {
		var missing errScriptMissing
		if !errors.As(err, &missing) {
			return err
		}
		shown := missing.Path
		if shown == "" {
			shown = "no script path"
		}
		_, err := b.github.createComment(lc.repo, lc.issue.Number, fmt.Sprintf(
			"⚠️ `%s` is not on `main`, so there is no test command to post. Fill in **Script path** in the issue or fix the name, then add `Ready For Testing` again.", shown))
		return err
	}

	var jsonBody []byte
	if info.Type != "pve" {
		jsonBody, _ = fetchRaw(rawBase + "/json/" + info.Slug + ".json")
	}
	testing := testingMessage(lc.issue.Title, info, rawBase, jsonBody)

	if lc.rec != nil && lc.exists {
		if _, err := b.threads.post(lc.rec.ThreadID, resumedNotice(testing, lc.issue.HTMLURL)); err != nil {
			return err
		}
		if err := b.threads.setState(lc.rec.ThreadID, false, false, tagTesting); err != nil {
			return err
		}
		if err := b.store.updateIssueRecord(lc.rec.ID, map[string]any{"slug": info.Slug}); err != nil {
			return err
		}
		if err := b.setThreadState(lc, "testing", true); err != nil {
			return err
		}
		url := threadURL(lc.cfg.GuildID, lc.rec.ThreadID)
		if _, err := b.github.createComment(lc.repo, lc.issue.Number, fmt.Sprintf("▶️ Testing resumed. Discord thread: %s\n\n<!-- discord-thread-id: %s -->\n", url, lc.rec.ThreadID)); err != nil {
			return err
		}
	} else {
		content := testing + "\nDiscussion & issue tracking:\n" + lc.issue.HTMLURL + "\n\n" + mirrorNote
		id, err := b.threads.create(lc.cfg.TestingChannelID, threadName(lc.issue.Title), content, tagTesting)
		if err != nil {
			return err
		}
		row := b.threadRow(lc, id, "testing", true)
		row["slug"] = info.Slug
		if lc.rec != nil {
			if err := b.store.updateIssueRecord(lc.rec.ID, row); err != nil {
				return err
			}
			b.index.drop(lc.rec.ThreadID)
			lc.rec.ThreadID, lc.rec.State, lc.rec.SyncEnabled, lc.rec.Slug = id, "testing", true, info.Slug
		} else if lc.rec, err = b.store.recordIssue(row); err != nil {
			return err
		}
		lc.exists = true
		b.index.set(*lc.rec)
		url := threadURL(lc.cfg.GuildID, id)
		if _, err := b.github.createComment(lc.repo, lc.issue.Number, testing+"\nDiscord testing thread: "+url+"\n\n<!-- discord-thread-id: "+id+" -->\n"); err != nil {
			return err
		}
	}

	if lc.issue.Locked {
		return b.github.unlockIssue(lc.repo, lc.issue.Number)
	}
	return nil
}

func (b *bot) hold(lc *lifecycleContext) error {
	if !lc.exists {
		log.Printf("discord bot: %s #%d: no thread to pause", lc.repo, lc.issue.Number)
		return nil
	}
	if _, err := b.threads.post(lc.rec.ThreadID, pausedNotice(lastHumanUpdate(lc.comments, lc.issue.Body), lc.issue.HTMLURL)); err != nil {
		return err
	}
	if err := b.threads.setState(lc.rec.ThreadID, false, false, tagHold); err != nil {
		return err
	}
	return b.setThreadState(lc, "hold", lc.rec.SyncEnabled)
}

func (b *bot) deferTesting(lc *lifecycleContext) error {
	if lc.exists {
		if _, err := b.threads.post(lc.rec.ThreadID, deferredNotice(lastHumanUpdate(lc.comments, lc.issue.Body), lc.issue.HTMLURL)); err != nil {
			return err
		}
		if err := b.threads.setState(lc.rec.ThreadID, true, true, tagDeferred); err != nil {
			return err
		}
		if err := b.setThreadState(lc, "deferred", false); err != nil {
			return err
		}
	}
	if _, err := b.github.createComment(lc.repo, lc.issue.Number, "⏸️ This script has been **deferred**. The Discord testing thread has been locked. Re-add the `Ready For Testing` label to resume testing."); err != nil {
		return err
	}
	return b.github.lockIssue(lc.repo, lc.issue.Number, "resolved")
}

func (b *bot) migrate(lc *lifecycleContext) error {
	if !lc.exists {
		log.Printf("discord bot: %s #%d: no thread to announce the migration in", lc.repo, lc.issue.Number)
		return nil
	}
	if locked, err := b.threads.locked(lc.rec.ThreadID); err != nil || locked {
		return err
	}
	if _, err := b.threads.post(lc.rec.ThreadID, migrationNotice(prFromComments(lc.comments), lc.issue.HTMLURL)); err != nil {
		return err
	}
	if err := b.threads.setState(lc.rec.ThreadID, true, true, tagMigrating); err != nil {
		return err
	}
	return b.setThreadState(lc, "migrating", false)
}

func (b *bot) deleteThread(lc *lifecycleContext) error {
	if !lc.exists {
		return nil
	}
	if err := b.threads.delete(lc.rec.ThreadID); err != nil {
		return err
	}
	return b.setThreadState(lc, "closed", false)
}
