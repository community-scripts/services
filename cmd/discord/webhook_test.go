package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"a":1}`)
	if !verifySignature("s", body, sign("s", body)) {
		t.Error("valid signature rejected")
	}
	if verifySignature("s", body, sign("other", body)) {
		t.Error("wrong secret accepted")
	}
	if verifySignature("s", body, "") {
		t.Error("missing header accepted")
	}
	if verifySignature("s", body, "sha1=abc") {
		t.Error("wrong prefix accepted")
	}
}

func postWebhook(t *testing.T, url string, body []byte, signature, event string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signature)
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "d1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	queue := make(chan webhookEvent, 1)
	srv := httptest.NewServer(webhookHandler("s", queue))
	defer srv.Close()
	body := []byte(`{"action":"labeled"}`)
	if resp := postWebhook(t, srv.URL, body, sign("other", body), "issues"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	if len(queue) != 0 {
		t.Fatal("rejected delivery must not be queued")
	}
}

func TestWebhookQueuesIssueEvent(t *testing.T) {
	queue := make(chan webhookEvent, 1)
	srv := httptest.NewServer(webhookHandler("s", queue))
	defer srv.Close()
	body := []byte(`{"action":"labeled"}`)
	if resp := postWebhook(t, srv.URL, body, sign("s", body), "issues"); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	ev := <-queue
	if ev.Name != "issues" || ev.Delivery != "d1" || !bytes.Equal(ev.Body, body) {
		t.Fatalf("got %+v", ev)
	}
	if resp := postWebhook(t, srv.URL, body, sign("s", body), "ping"); resp.StatusCode != http.StatusNoContent || len(queue) != 0 {
		t.Fatalf("unknown event: want 204 and nothing queued, got %d / %d", resp.StatusCode, len(queue))
	}
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %v %v", err, resp)
	}
	resp.Body.Close()
}
