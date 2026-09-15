package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// Discord attachment URLs are signed and expire after roughly a day, so an
// image that only lives there is gone from the issue by the time anyone reads
// it. Images are copied into PocketBase; everything else stays a link, which
// keeps logs and archives out of the database.
const maxAttachmentBytes = 8 << 20

func isImage(filename, contentType string) bool {
	if strings.HasPrefix(contentType, "image/") {
		return true
	}
	dot := strings.LastIndex(filename, ".")
	if dot < 0 {
		return false
	}
	switch strings.ToLower(filename[dot:]) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// uploadAttachment stores one file and returns the URL it is served from.
func (s *store) uploadAttachment(filename string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	body, err := s.upload("/api/collections/issue_attachments/records", w.FormDataContentType(), buf.Bytes())
	if err != nil {
		return "", err
	}

	var out struct {
		ID   string `json:"id"`
		File string `json:"file"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.ID == "" || out.File == "" {
		return "", fmt.Errorf("upload returned no file reference")
	}
	return fmt.Sprintf("%s/api/files/issue_attachments/%s/%s", s.baseURL, out.ID, out.File), nil
}

// fetchAttachment pulls the file off Discord's CDN while its link is still
// valid. Anything larger than the cap is left as a link instead.
func fetchAttachment(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download attachment: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("attachment is larger than %d bytes", maxAttachmentBytes)
	}
	return data, nil
}
