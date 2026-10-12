package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
)

// scmWebhookProvider describes the manual webhook ingress for one SCM
// provider type. Each provider has its own receiver endpoint (the payload
// shape and signature scheme differ), but configuration, secret rotation and
// delivery bookkeeping are shared.
type scmWebhookProvider struct {
	Type        models.SCMProviderType
	PathSegment string   // URL segment of the receiver: /api/scm/webhooks/<segment>/<key>
	Events      []string // Provider event names the receiver accepts
	EventsJSON  string   // Same list, serialised for the scm_webhooks.events column
}

var scmWebhookProviders = map[models.SCMProviderType]scmWebhookProvider{
	models.SCMProviderTypeGitLab: {
		Type:        models.SCMProviderTypeGitLab,
		PathSegment: "gitlab",
		Events:      gitLabWebhookEvents,
		EventsJSON:  `["push","tag_push","merge_request","note","release"]`,
	},
	models.SCMProviderTypeGitHub: {
		Type:        models.SCMProviderTypeGitHub,
		PathSegment: "github",
		Events:      gitHubWebhookEvents,
		EventsJSON:  `["push","create","pull_request","pull_request_review","pull_request_review_comment","issue_comment","release"]`,
	},
}

// scmWebhookProviderFor returns the manual webhook spec for a provider type,
// or false when that provider has no inbound receiver.
func scmWebhookProviderFor(providerType string) (scmWebhookProvider, bool) {
	spec, ok := scmWebhookProviders[models.SCMProviderType(providerType)]
	return spec, ok
}

const scmWebhookUnsupportedMessage = "Manual webhooks are only available for GitHub and GitLab repositories"

type scmWebhookConfigResponse struct {
	Configured     bool       `json:"configured"`
	Provider       string     `json:"provider"`
	CallbackURL    string     `json:"callback_url,omitempty"`
	Secret         string     `json:"secret,omitempty"`
	Events         []string   `json:"events"`
	Active         bool       `json:"active"`
	LastDeliveryAt *time.Time `json:"last_delivery_at,omitempty"`
}

func (h *SCMItemLinksHandler) webhookRepoAdmin(w http.ResponseWriter, r *http.Request, repoID int) (string, bool) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return "", false
	}
	access, err := h.webhookRepo.GetRepositoryAccess(r.Context(), repoID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "workspace_repository")
		return "", false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return "", false
	}
	if !RequireWorkspacePermission(w, r, user.ID, access.WorkspaceID, models.PermissionWorkspaceAdmin, h.permissionService) {
		return "", false
	}
	return access.ProviderType, true
}

// webhookProviderAdmin combines the admin check with the provider lookup and
// writes the 400 for providers without a manual webhook receiver.
func (h *SCMItemLinksHandler) webhookProviderAdmin(w http.ResponseWriter, r *http.Request, repoID int) (scmWebhookProvider, bool) {
	providerType, ok := h.webhookRepoAdmin(w, r, repoID)
	if !ok {
		return scmWebhookProvider{}, false
	}
	spec, ok := scmWebhookProviderFor(providerType)
	if !ok {
		respondBadRequest(w, r, scmWebhookUnsupportedMessage)
		return scmWebhookProvider{}, false
	}
	return spec, true
}

func (h *SCMItemLinksHandler) webhookCallbackURL(r *http.Request, spec scmWebhookProvider, key string) string {
	base := h.baseURL
	if base == "" {
		scheme := "https"
		if r.TLS == nil {
			if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
				scheme = forwarded
			} else {
				scheme = "http"
			}
		}
		host := r.Host
		if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
			host = forwarded
		}
		base = scheme + "://" + host
	}
	return strings.TrimRight(base, "/") + "/api/scm/webhooks/" + spec.PathSegment + "/" + key
}

// GetSCMWebhookConfig returns manual setup information without exposing the secret.
func (h *SCMItemLinksHandler) GetSCMWebhookConfig(w http.ResponseWriter, r *http.Request) {
	repoID, ok := requireIDParam(w, r, "repoId")
	if !ok {
		return
	}
	spec, ok := h.webhookProviderAdmin(w, r, repoID)
	if !ok {
		return
	}
	config, err := h.webhookRepo.GetConfig(r.Context(), repoID)
	if errors.Is(err, repository.ErrNotFound) {
		respondJSONOK(w, scmWebhookConfigResponse{Configured: false, Provider: string(spec.Type), Events: spec.Events})
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, scmWebhookConfigResponse{
		Configured:     true,
		Provider:       string(spec.Type),
		CallbackURL:    h.webhookCallbackURL(r, spec, config.WebhookKey),
		Events:         spec.Events,
		Active:         config.Active,
		LastDeliveryAt: config.LastDeliveryAt,
	})
}

func randomWebhookValue(bytesCount int) (string, error) {
	raw := make([]byte, bytesCount)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// RotateSCMWebhookSecret creates or rotates the one-time manual setup secret.
func (h *SCMItemLinksHandler) RotateSCMWebhookSecret(w http.ResponseWriter, r *http.Request) {
	repoID, ok := requireIDParam(w, r, "repoId")
	if !ok {
		return
	}
	spec, ok := h.webhookProviderAdmin(w, r, repoID)
	if !ok {
		return
	}
	secret, err := randomWebhookValue(32)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	encrypted, err := h.encryption.Encrypt(secret)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	newKey, err := randomWebhookValue(18)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	key, err := h.webhookRepo.RotateConfig(r.Context(), repoID, newKey, encrypted, spec.EventsJSON)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, scmWebhookConfigResponse{
		Configured:  true,
		Provider:    string(spec.Type),
		CallbackURL: h.webhookCallbackURL(r, spec, key),
		Secret:      secret,
		Events:      spec.Events,
		Active:      true,
	})
}

// DeleteSCMWebhookConfig removes the manual webhook configuration for a repository.
func (h *SCMItemLinksHandler) DeleteSCMWebhookConfig(w http.ResponseWriter, r *http.Request) {
	repoID, ok := requireIDParam(w, r, "repoId")
	if !ok {
		return
	}
	if _, ok := h.webhookRepoAdmin(w, r, repoID); !ok {
		return
	}
	if err := h.webhookRepo.DeleteConfig(r.Context(), repoID); err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, map[string]bool{"deleted": true})
}

// loadWebhookTarget resolves the receiver key to its repository and checks
// that the stored repository belongs to the provider whose endpoint was hit.
// A GitHub payload posted to the GitLab endpoint (or vice versa) is rejected
// before any secret is decrypted.
func (h *SCMItemLinksHandler) loadWebhookTarget(w http.ResponseWriter, r *http.Request, providerType models.SCMProviderType) (repository.SCMWebhookTarget, string, bool) {
	key := r.PathValue("webhookKey")
	target, err := h.webhookRepo.GetTargetByKey(r.Context(), key)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "scm_webhook")
		return repository.SCMWebhookTarget{}, "", false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return repository.SCMWebhookTarget{}, "", false
	}
	if target.ProviderType != string(providerType) {
		respondBadRequest(w, r, "Invalid webhook provider")
		return repository.SCMWebhookTarget{}, "", false
	}
	secret, err := h.encryption.Decrypt(target.EncryptedSecret)
	if err != nil {
		respondInternalError(w, r, err)
		return repository.SCMWebhookTarget{}, "", false
	}
	return target, secret, true
}

// webhookDeliveryID prefers the provider's delivery identifier and falls back
// to a digest of the body so redeliveries without an id still deduplicate.
func webhookDeliveryID(headerValue string, body []byte) string {
	if id := strings.TrimSpace(headerValue); id != "" {
		return id
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// acceptWebhookDelivery records a deduplicated delivery, acknowledges it, and
// schedules a targeted repository sync in the background. Polling remains the
// recovery path: a failed or lost delivery is picked up by the next sync tick.
func (h *SCMItemLinksHandler) acceptWebhookDelivery(w http.ResponseWriter, r *http.Request, target repository.SCMWebhookTarget, deliveryID, eventType, summary string) {
	inserted, err := h.webhookRepo.RecordPendingDelivery(r.Context(), target.ID, deliveryID, eventType, summary)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	if !inserted {
		return
	}

	baseCtx := context.WithoutCancel(r.Context())
	go func() {
		started := time.Now()
		ctx, cancel := context.WithTimeout(baseCtx, 45*time.Second)
		defer cancel()
		syncErr := h.syncService.SyncRepository(ctx, target.WorkspaceRepositoryID)
		status, errorMessage := "processed", ""
		if syncErr != nil {
			status, errorMessage = "failed", syncErr.Error()
			slog.Warn("SCM webhook sync failed",
				slog.String("provider", target.ProviderType),
				slog.Int("repository_id", target.WorkspaceRepositoryID),
				slog.Any("error", syncErr))
		}
		updateErr := h.webhookRepo.CompleteDelivery(ctx, target.ID, deliveryID, status, errorMessage, time.Since(started))
		if updateErr != nil {
			slog.Warn("SCM webhook delivery update failed", slog.Any("error", updateErr), slog.String("delivery_id", deliveryID))
		}
	}()
}
