package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// GitHub rejects issue bodies over 65536 characters.
const maxIssueBody = 65000

// The subset of *discordgo.Session the thread reader needs, so the paging logic
// can be exercised without a live gateway.
type messageFetcher interface {
	ChannelMessages(channelID string, limit int, beforeID, afterID, aroundID string, options ...discordgo.RequestOption) ([]*discordgo.Message, error)
}

func isUserMessage(m *discordgo.Message) bool {
	return m.Type == discordgo.MessageTypeDefault || m.Type == discordgo.MessageTypeReply
}

// collectThread walks a thread oldest-first. The API caps a page at 100 and
// returns newest-first, so it pages backwards and flips the result.
func collectThread(s messageFetcher, channelID string, limit int) ([]*discordgo.Message, error) {
	var collected []*discordgo.Message
	before := ""

	for len(collected) < limit {
		page, err := s.ChannelMessages(channelID, 100, before, "", "")
		if err != nil {
			return nil, fmt.Errorf("fetch messages: %w", err)
		}
		if len(page) == 0 {
			break
		}
		collected = append(collected, page...)
		before = page[len(page)-1].ID
		if len(page) < 100 {
			break
		}
	}

	ordered := make([]*discordgo.Message, 0, len(collected))
	for i := len(collected) - 1; i >= 0; i-- {
		if isUserMessage(collected[i]) {
			ordered = append(ordered, collected[i])
		}
	}
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered, nil
}

func deriveTitle(threadName string, first *discordgo.Message) string {
	if name := strings.TrimSpace(threadName); name != "" {
		return truncate(name, 240)
	}
	if first != nil {
		line := strings.TrimSpace(strings.SplitN(first.Content, "\n", 2)[0])
		if line != "" {
			return truncate(line, 240)
		}
	}
	return "Discord report"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func quote(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

type renderInput struct {
	Messages  []*discordgo.Message
	GuildID   string
	ChannelID string
	ThreadURL string
	Moderator string
}

func renderIssueBody(in renderInput) string {
	var b strings.Builder
	attachments := 0

	// Backticked, not @-prefixed: a Discord username is not a GitHub one, and a
	// bare @name would notify whoever happens to hold it on GitHub.
	fmt.Fprintf(&b, "_Imported from Discord by `%s` — [open the thread](%s)._\n\n---\n\n", in.Moderator, in.ThreadURL)

	for _, m := range in.Messages {
		author := "unknown"
		if m.Author != nil {
			author = m.Author.Username
		}
		fmt.Fprintf(&b, "#### %s — %s\n", author, m.Timestamp.UTC().Format("2006-01-02 15:04 UTC"))

		if strings.TrimSpace(m.Content) != "" {
			b.WriteString(quote(m.Content) + "\n")
		}
		for _, a := range m.Attachments {
			attachments++
			link := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", in.GuildID, in.ChannelID, m.ID)
			fmt.Fprintf(&b, "> 📎 [%s](%s) · [view in Discord](%s)\n", a.Filename, a.URL, link)
		}
		b.WriteString("\n")
	}

	if attachments > 0 {
		fmt.Fprintf(&b, "---\n\n> **Note:** Discord attachment links are signed and expire after roughly 24 hours. Use the thread link above to reach the %d attached file(s) afterwards.\n", attachments)
	}

	body := b.String()
	if len(body) <= maxIssueBody {
		return body
	}
	notice := fmt.Sprintf("\n\n---\n\n_Truncated: the thread exceeded GitHub's issue size limit. [Read it in full on Discord](%s)._", in.ThreadURL)
	return body[:maxIssueBody-len(notice)] + notice
}
