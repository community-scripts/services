package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/bwmarrin/discordgo"
)

const commandName = "Create GitHub Issue"

type appConfig struct {
	discordToken       string
	githubAppID        string
	githubInstallation string
	githubKey          string
	pocketbaseURL      string
	pocketbaseEmail    string
	pocketbasePassword string
	maxThreadMessages  int
}

func loadConfig() (appConfig, error) {
	var missing []string
	get := func(name string) string {
		value := os.Getenv(name)
		if value == "" {
			missing = append(missing, name)
		}
		return value
	}

	cfg := appConfig{
		discordToken:       get("DISCORD_TOKEN"),
		githubAppID:        get("GITHUB_APP_ID"),
		githubInstallation: get("GITHUB_APP_INSTALLATION_ID"),
		githubKey:          normalisePEM(get("GITHUB_APP_PRIVATE_KEY")),
		pocketbaseURL:      get("POCKETBASE_URL"),
		pocketbaseEmail:    get("POCKETBASE_ADMIN_EMAIL"),
		pocketbasePassword: get("POCKETBASE_ADMIN_PASSWORD"),
		maxThreadMessages:  500,
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}

	if raw := os.Getenv("MAX_THREAD_MESSAGES"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("MAX_THREAD_MESSAGES must be a positive integer, got %q", raw)
		}
		cfg.maxThreadMessages = n
	}
	return cfg, nil
}

// Env files mangle the newlines in a PEM, so a base64 blob is accepted too.
func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

var pemBlock = regexp.MustCompile(`(?s)-----BEGIN ([A-Z0-9 ]+?)-----(.*?)-----END [A-Z0-9 ]+?-----`)

// Rebuild the block from its base64 body, so however the value survived its trip
// through an env field -- real newlines, literal backslash-n, spaces, or no
// separators at all -- it comes out as PEM that pem.Decode accepts.
func rebuildPEM(s string) string {
	m := pemBlock.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	label, body := strings.TrimSpace(m[1]), stripSpace(m[2])

	var b strings.Builder
	b.WriteString("-----BEGIN " + label + "-----\n")
	for i := 0; i < len(body); i += 64 {
		j := i + 64
		if j > len(body) {
			j = len(body)
		}
		b.WriteString(body[i:j] + "\n")
	}
	b.WriteString("-----END " + label + "-----\n")
	return b.String()
}

func normalisePEM(raw string) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, `\n`, "\n"))
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "-----BEGIN") {
		decoded, err := base64.StdEncoding.DecodeString(stripSpace(raw))
		if err != nil {
			return raw
		}
		raw = string(decoded)
	}
	return rebuildPEM(raw)
}

type bot struct {
	cfg    appConfig
	store  *store
	github *gitHub
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("discord bot: %v", err)
	}

	gh, err := newGitHub(cfg.githubAppID, cfg.githubInstallation, cfg.githubKey)
	if err != nil {
		log.Fatalf("discord bot: %v", err)
	}

	b := &bot{
		cfg:    cfg,
		store:  newStore(cfg.pocketbaseURL, cfg.pocketbaseEmail, cfg.pocketbasePassword),
		github: gh,
	}

	session, err := discordgo.New("Bot " + cfg.discordToken)
	if err != nil {
		log.Fatalf("discord bot: %v", err)
	}
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentMessageContent

	session.AddHandler(b.onReady)
	session.AddHandler(b.onGuildCreate)
	session.AddHandler(b.onInteraction)

	if err := session.Open(); err != nil {
		log.Fatalf("discord bot: open session: %v", err)
	}
	defer session.Close()

	log.Println("discord bot: running, press Ctrl+C to stop")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}

// Guild-scoped registration applies immediately, unlike global commands which
// take up to an hour to propagate.
func registerCommand(s *discordgo.Session, appID, guildID string) {
	permissions := int64(discordgo.PermissionManageMessages)
	command := &discordgo.ApplicationCommand{
		Name:                     commandName,
		Type:                     discordgo.MessageApplicationCommand,
		DefaultMemberPermissions: &permissions,
	}
	if _, err := s.ApplicationCommandCreate(appID, guildID, command); err != nil {
		log.Printf("discord bot: register command in guild %s: %v", guildID, err)
	}
}

func (b *bot) onReady(s *discordgo.Session, r *discordgo.Ready) {
	for _, guild := range r.Guilds {
		registerCommand(s, r.User.ID, guild.ID)
	}
	log.Printf("discord bot: ready as %s, %d guild(s)", r.User.String(), len(r.Guilds))
}

// Ready only lists guilds the bot was already in. Without this, being invited
// while it runs leaves the command unregistered until the next restart.
func (b *bot) onGuildCreate(s *discordgo.Session, g *discordgo.GuildCreate) {
	registerCommand(s, s.State.User.ID, g.ID)
	log.Printf("discord bot: joined guild %s (%s)", g.Name, g.ID)
}

func (b *bot) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}
	if i.ApplicationCommandData().Name != commandName {
		return
	}

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("discord bot: defer response: %v", err)
		return
	}

	message, err := b.handle(s, i)
	if err != nil {
		log.Printf("discord bot: %v", err)
		message = "Issue creation failed: " + err.Error()
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &message}); err != nil {
		log.Printf("discord bot: edit response: %v", err)
	}
}

func (b *bot) handle(s *discordgo.Session, i *discordgo.InteractionCreate) (string, error) {
	if i.GuildID == "" {
		return "This command only works inside a server.", nil
	}

	settings, err := b.store.guildConfig(i.GuildID)
	if err != nil {
		return "", fmt.Errorf("read guild config: %w", err)
	}
	if settings == nil {
		return "No `discord_config` record exists for this server. Add one in PocketBase first.", nil
	}
	if !settings.Enabled {
		return "Issue creation is disabled for this server.", nil
	}
	if i.Member == nil || !hasAllowedRole(i.Member.Roles, settings.AllowedRoleIDs) {
		return "You do not have a role that is allowed to create issues.", nil
	}

	channel, err := s.Channel(i.ChannelID)
	if err != nil {
		return "", fmt.Errorf("read channel: %w", err)
	}
	if !isThread(channel.Type) {
		return "That message is not part of a thread. Use this on a forum post or a thread message.", nil
	}

	existing, err := b.store.issueForThread(channel.ID)
	if err != nil {
		return "", fmt.Errorf("check for existing issue: %w", err)
	}
	if existing != nil {
		return "This thread already has an issue: " + existing.IssueURL, nil
	}

	messages, err := collectThread(s, channel.ID, b.cfg.maxThreadMessages)
	if err != nil {
		return "", err
	}
	if len(messages) == 0 {
		return "Could not read any messages from this thread.", nil
	}

	threadURL := fmt.Sprintf("https://discord.com/channels/%s/%s", i.GuildID, channel.ID)
	moderator := "unknown"
	if i.Member.User != nil {
		moderator = i.Member.User.Username
	}

	issue, err := b.github.createIssue(
		settings.TargetRepo,
		deriveTitle(channel.Name, messages[0]),
		renderIssueBody(renderInput{
			Messages:  messages,
			GuildID:   i.GuildID,
			ChannelID: channel.ID,
			ThreadURL: threadURL,
			Moderator: moderator,
		}),
		settings.DefaultLabels,
	)
	if err != nil {
		return "", err
	}

	entry := map[string]any{
		"guild_id":        i.GuildID,
		"thread_id":       channel.ID,
		"message_id":      i.ApplicationCommandData().TargetID,
		"repo":            settings.TargetRepo,
		"issue_number":    issue.Number,
		"issue_url":       issue.URL,
		"created_by_id":   i.Member.User.ID,
		"created_by_name": moderator,
		"message_count":   len(messages),
	}
	if err := b.store.recordIssue(entry); err != nil {
		// The issue exists, so this must not read as a failure -- but dedup stays
		// broken for this thread until the row is reconciled.
		log.Printf("discord bot: issue %s created but not recorded: %v", issue.URL, err)
	}

	if _, err := s.ChannelMessageSend(channel.ID, "📋 Tracked on GitHub: "+issue.URL); err != nil {
		log.Printf("discord bot: post issue link to thread: %v", err)
	}

	return fmt.Sprintf("Created %s from %d message(s).", issue.URL, len(messages)), nil
}

func hasAllowedRole(memberRoles, allowed []string) bool {
	for _, role := range memberRoles {
		for _, id := range allowed {
			if role == id {
				return true
			}
		}
	}
	return false
}

func isThread(t discordgo.ChannelType) bool {
	return t == discordgo.ChannelTypeGuildPublicThread ||
		t == discordgo.ChannelTypeGuildPrivateThread ||
		t == discordgo.ChannelTypeGuildNewsThread
}
