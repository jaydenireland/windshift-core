package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/models"
)

// gitHubWebhookEvents lists the X-GitHub-Event values that trigger a
// repository sync. Anything else (including the "ping" GitHub sends when the
// hook is created) is acknowledged and ignored.
var gitHubWebhookEvents = []string{"push", "create", "pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment", "release"}

const gitHubSignatureHeader = "X-Hub-Signature-256"

type gitHubWebhookPayload struct {
	Action     string `json:"action"`
	Ref        string `json:"ref"`
	RefType    string `json:"ref_type"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Issue struct {
		Number int `json:"number"`
	} `json:"issue"`
	Release struct {
		TagName string `json:"tag_name"`
	} `json:"release"`
}

func normalizeGitHubWebhookEvent(header string) string {
	event := strings.ToLower(strings.TrimSpace(header))
	for _, accepted := range gitHubWebhookEvents {
		if event == accepted {
			return event
		}
	}
	return ""
}

// verifyGitHubSignature checks the "sha256=<hex>" HMAC GitHub sends in
// X-Hub-Signature-256 against the body, using constant-time comparison.
func verifyGitHubSignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

// ReceiveGitHubWebhook validates and deduplicates a GitHub delivery, then
// schedules a targeted repository sync. Polling remains the recovery path.
//
// Unlike GitLab's shared-token header, GitHub signs the raw body, so the body
// is read before the secret check and must not be re-serialised in between.
func (h *SCMItemLinksHandler) ReceiveGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	target, secret, ok := h.loadWebhookTarget(w, r, models.SCMProviderTypeGitHub)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondBadRequest(w, r, "Webhook payload is too large or unreadable")
		return
	}
	if !verifyGitHubSignature(secret, body, r.Header.Get(gitHubSignatureHeader)) {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid webhook signature"})
		return
	}

	eventType := normalizeGitHubWebhookEvent(r.Header.Get("X-GitHub-Event"))
	if eventType == "" {
		// "ping" lands here: GitHub shows this 202 as the hook test result.
		respondJSON(w, http.StatusAccepted, map[string]bool{"ignored": true})
		return
	}
	var payload gitHubWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		respondBadRequest(w, r, "Invalid GitHub webhook JSON")
		return
	}
	if strconv.FormatInt(payload.Repository.ID, 10) != target.RepositoryExternalID {
		respondJSON(w, http.StatusForbidden, map[string]string{"error": "repository mismatch"})
		return
	}
	number := payload.PullRequest.Number
	if number == 0 {
		number = payload.Issue.Number
	}
	deliveryID := webhookDeliveryID(r.Header.Get("X-GitHub-Delivery"), body)
	summary, _ := json.Marshal(map[string]any{"event": eventType, "action": payload.Action, "repository_id": payload.Repository.ID, "full_name": payload.Repository.FullName, "number": number, "ref": payload.Ref, "ref_type": payload.RefType, "tag": payload.Release.TagName})
	h.acceptWebhookDelivery(w, r, target, deliveryID, eventType, string(summary))
}
