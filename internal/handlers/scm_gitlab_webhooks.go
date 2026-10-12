package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/models"
)

var gitLabWebhookEvents = []string{"push", "tag_push", "merge_request", "note", "release"}

type gitLabWebhookPayload struct {
	ObjectKind string `json:"object_kind"`
	EventName  string `json:"event_name"`
	ProjectID  int64  `json:"project_id"`
	Ref        string `json:"ref"`
	Project    struct {
		ID                int64  `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		IID    int    `json:"iid"`
		Action string `json:"action"`
		Tag    string `json:"tag"`
	} `json:"object_attributes"`
}

func normalizeGitLabWebhookKind(payload gitLabWebhookPayload) string {
	kind := strings.ToLower(strings.TrimSpace(payload.ObjectKind))
	switch kind {
	case "push", "tag_push", "merge_request", "note", "release":
		return kind
	default:
		return ""
	}
}

// ReceiveGitLabWebhook validates and deduplicates a GitLab delivery, then
// schedules a targeted repository sync. Polling remains the recovery path.
func (h *SCMItemLinksHandler) ReceiveGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	target, secret, ok := h.loadWebhookTarget(w, r, models.SCMProviderTypeGitLab)
	if !ok {
		return
	}
	provided := r.Header.Get("X-Gitlab-Token")
	if len(secret) != len(provided) || subtle.ConstantTimeCompare([]byte(secret), []byte(provided)) != 1 {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid webhook token"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondBadRequest(w, r, "Webhook payload is too large or unreadable")
		return
	}
	var payload gitLabWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		respondBadRequest(w, r, "Invalid GitLab webhook JSON")
		return
	}
	eventType := normalizeGitLabWebhookKind(payload)
	if eventType == "" {
		respondJSON(w, http.StatusAccepted, map[string]bool{"ignored": true})
		return
	}
	projectID := payload.Project.ID
	if projectID == 0 {
		projectID = payload.ProjectID
	}
	if strconv.FormatInt(projectID, 10) != target.RepositoryExternalID {
		respondJSON(w, http.StatusForbidden, map[string]string{"error": "project mismatch"})
		return
	}
	deliveryID := webhookDeliveryID(r.Header.Get("X-Gitlab-Event-UUID"), body)
	summary, _ := json.Marshal(map[string]any{"object_kind": eventType, "project_id": projectID, "path": payload.Project.PathWithNamespace, "iid": payload.ObjectAttributes.IID, "action": payload.ObjectAttributes.Action, "ref": payload.Ref, "tag": payload.ObjectAttributes.Tag})
	h.acceptWebhookDelivery(w, r, target, deliveryID, eventType, string(summary))
}
