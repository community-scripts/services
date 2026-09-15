package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestRenderMetaTable(t *testing.T) {
	body := renderIssueBody(renderInput{
		Messages:  []*discordgo.Message{{ID: "1", Content: "hi", Author: &discordgo.User{Username: "a"}}},
		ThreadURL: "https://d/t",
		Moderator: "mod",
		Meta: []metaField{
			{"Script name", "prometheus"},
			{"OS and version", ""},
			{"Proxmox version", "9.2.18"},
		},
	})
	if !strings.Contains(body, "| **Script name** | prometheus |") {
		t.Error("filled field missing")
	}
	if strings.Contains(body, "OS and version") {
		t.Error("empty field should be dropped")
	}
}

func TestRenderNoMetaNoTable(t *testing.T) {
	body := renderIssueBody(renderInput{
		Messages:  []*discordgo.Message{{ID: "1", Content: "hi", Author: &discordgo.User{Username: "a"}}},
		ThreadURL: "https://d/t",
	})
	if strings.Contains(body, "| --- | --- |") {
		t.Error("no table without metadata")
	}
}

func TestRenderUploadedImage(t *testing.T) {
	msg := &discordgo.Message{ID: "1", Author: &discordgo.User{Username: "a"},
		Attachments: []*discordgo.MessageAttachment{
			{Filename: "shot.png", URL: "https://cdn/shot.png"},
			{Filename: "debug.log", URL: "https://cdn/debug.log"},
		}}
	body := renderIssueBody(renderInput{
		Messages:  []*discordgo.Message{msg},
		ThreadURL: "https://d/t",
		Uploads:   map[string]string{"https://cdn/shot.png": "https://pb/f/x/shot.png"},
	})
	if !strings.Contains(body, "![shot.png](https://pb/f/x/shot.png)") {
		t.Error("image should be embedded from PocketBase")
	}
	if !strings.Contains(body, "[debug.log](https://cdn/debug.log)") {
		t.Error("non-image should stay a Discord link")
	}
	if !strings.Contains(body, "1 file(s) are still Discord links") {
		t.Error("note should count only the links, got:\n" + body)
	}
}

func TestIsImage(t *testing.T) {
	for _, c := range []struct {
		name, ct string
		want     bool
	}{
		{"a.PNG", "", true},
		{"a.jpeg", "", true},
		{"a.log", "", false},
		{"noext", "", false},
		{"a.log", "image/png", true},
	} {
		if got := isImage(c.name, c.ct); got != c.want {
			t.Errorf("isImage(%q,%q)=%v", c.name, c.ct, got)
		}
	}
}
