package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const maxWebhookBody = 1 << 20

type webhookEvent struct {
	Name, Delivery string
	Body           []byte
}

type ghRepository struct {
	FullName string `json:"full_name"`
}

type issuesEvent struct {
	Action     string       `json:"action"`
	Label      ghLabel      `json:"label"`
	Issue      ghIssue      `json:"issue"`
	Repository ghRepository `json:"repository"`
}

type issueCommentEvent struct {
	Action     string       `json:"action"`
	Comment    ghComment    `json:"comment"`
	Issue      ghIssue      `json:"issue"`
	Repository ghRepository `json:"repository"`
}

func verifySignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

func webhookHandler(secret string, queue chan<- webhookEvent) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	})
	if secret == "" {
		return mux
	}
	mux.HandleFunc("POST /webhooks/github", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
		if err != nil || len(body) > maxWebhookBody {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		if !verifySignature(secret, body, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		name := r.Header.Get("X-GitHub-Event")
		if name != "issues" && name != "issue_comment" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case queue <- webhookEvent{Name: name, Delivery: r.Header.Get("X-GitHub-Delivery"), Body: body}:
			w.WriteHeader(http.StatusAccepted)
		default:
			http.Error(w, "queue full", http.StatusServiceUnavailable)
		}
	})
	return mux
}

func newWebhookServer(addr, secret string, queue chan<- webhookEvent) *http.Server {
	return &http.Server{Addr: addr, Handler: webhookHandler(secret, queue), ReadHeaderTimeout: 10 * time.Second}
}

// One delivery at a time keeps the events of an issue in order.
func (b *bot) worker(queue <-chan webhookEvent) {
	for ev := range queue {
		switch ev.Name {
		case "issues":
			var e issuesEvent
			if err := json.Unmarshal(ev.Body, &e); err != nil {
				log.Printf("discord bot: delivery %s: %v", ev.Delivery, err)
				continue
			}
			b.handleIssueEvent(e.Repository.FullName, e.Action, e.Label.Name, e.Issue)
		}
	}
}
