package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/sso"
)

const (
	testGitHubWebhookKey    = "github-webhook-key"
	testGitHubWebhookSecret = "synthetic-github-webhook-secret"
	testGitHubRepositoryID  = "424242"
	testGitLabWebhookKey    = "gitlab-webhook-key"
	testGitLabWebhookSecret = "synthetic-gitlab-webhook-token"
	testGitLabProjectID     = "777"
)

// newSCMWebhookHandlerTest seeds one GitHub and one GitLab repository, each
// with an active manual webhook, and returns a handler without a permission
// service (the receivers are unauthenticated endpoints).
func newSCMWebhookHandlerTest(t *testing.T) (*SCMItemLinksHandler, database.Database) {
	t.Helper()
	db, err := database.NewSQLiteDB(t.TempDir() + "/windshift.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	encryption := sso.NewSecretEncryption("synthetic-webhook-test-secret")

	result, err := db.ExecWrite(`INSERT INTO workspaces (name, key) VALUES (?, ?)`, "Primary", "PRI")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, _ := result.LastInsertId()

	seed := func(providerType, externalID, key, secret string) {
		result, err := db.ExecWrite(`INSERT INTO scm_providers (slug, name, provider_type, auth_method, enabled) VALUES (?, ?, ?, 'pat', true)`,
			providerType+"-test", providerType+" test", providerType)
		if err != nil {
			t.Fatal(err)
		}
		providerID, _ := result.LastInsertId()
		result, err = db.ExecWrite(`INSERT INTO workspace_scm_connections (workspace_id, scm_provider_id) VALUES (?, ?)`, workspaceID, providerID)
		if err != nil {
			t.Fatal(err)
		}
		connectionID, _ := result.LastInsertId()
		result, err = db.ExecWrite(`INSERT INTO workspace_repositories (workspace_scm_connection_id, repository_external_id, repository_name, repository_url) VALUES (?, ?, ?, ?)`,
			connectionID, externalID, "acme/"+providerType, "https://"+providerType+".example.test/acme/"+providerType)
		if err != nil {
			t.Fatal(err)
		}
		repoID, _ := result.LastInsertId()
		encrypted, err := encryption.Encrypt(secret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecWrite(`INSERT INTO scm_webhooks (workspace_repository_id, webhook_key, webhook_secret_encrypted, events, is_active) VALUES (?, ?, ?, '[]', true)`,
			repoID, key, encrypted); err != nil {
			t.Fatal(err)
		}
	}
	seed("github", testGitHubRepositoryID, testGitHubWebhookKey, testGitHubWebhookSecret)
	seed("gitlab", testGitLabProjectID, testGitLabWebhookKey, testGitLabWebhookSecret)

	return NewSCMItemLinksHandler(db, encryption, nil, "https://windshift.example.test"), db
}

func signGitHubBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func gitHubWebhookRequest(key, event, delivery, signature string, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/scm/webhooks/github/"+key, bytes.NewReader(body))
	req.SetPathValue("webhookKey", key)
	req.Header.Set("Content-Type", "application/json")
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	if signature != "" {
		req.Header.Set(gitHubSignatureHeader, signature)
	}
	return req
}

func gitHubPullRequestBody(t *testing.T, repoID int64) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"action":       "opened",
		"repository":   map[string]any{"id": repoID, "full_name": "acme/github"},
		"pull_request": map[string]any{"number": 17, "head": map[string]any{"ref": "feat9999-story8888-task123"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func countWebhookDeliveries(t *testing.T, db database.Database, key string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM scm_webhook_deliveries d
		JOIN scm_webhooks w ON w.id = d.scm_webhook_id
		WHERE w.webhook_key = ?`, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// waitForWebhookDeliveries blocks until no delivery for the key is pending so
// the background sync goroutine has finished touching the database before the
// test's cleanup closes it.
func waitForWebhookDeliveries(t *testing.T, db database.Database, key string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var pending int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM scm_webhook_deliveries d
			JOIN scm_webhooks w ON w.id = d.scm_webhook_id
			WHERE w.webhook_key = ? AND d.status = 'pending'`, key).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("webhook delivery never left pending state")
}

func TestReceiveGitHubWebhookAcceptsSignedDelivery(t *testing.T) {
	handler, db := newSCMWebhookHandlerTest(t)
	body := gitHubPullRequestBody(t, 424242)

	recorder := httptest.NewRecorder()
	handler.ReceiveGitHubWebhook(recorder, gitHubWebhookRequest(testGitHubWebhookKey, "pull_request", "delivery-1", signGitHubBody(testGitHubWebhookSecret, body), body))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]bool
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response["accepted"] {
		t.Fatalf("expected accepted response, got %s", recorder.Body.String())
	}
	waitForWebhookDeliveries(t, db, testGitHubWebhookKey)

	var eventType, summary, status string
	if err := db.QueryRow(`
		SELECT d.event_type, d.payload_summary, d.status FROM scm_webhook_deliveries d
		JOIN scm_webhooks w ON w.id = d.scm_webhook_id
		WHERE w.webhook_key = ? AND d.delivery_id = ?`, testGitHubWebhookKey, "delivery-1").Scan(&eventType, &summary, &status); err != nil {
		t.Fatal(err)
	}
	if eventType != "pull_request" {
		t.Fatalf("expected pull_request event, got %q", eventType)
	}
	if status == "pending" {
		t.Fatalf("delivery still pending after sync")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(summary), &parsed); err != nil {
		t.Fatalf("summary is not JSON: %s", summary)
	}
	if parsed["number"] != float64(17) || parsed["action"] != "opened" || parsed["full_name"] != "acme/github" {
		t.Fatalf("unexpected summary: %s", summary)
	}

	// A redelivery with the same delivery id is acknowledged but not recorded twice.
	recorder = httptest.NewRecorder()
	handler.ReceiveGitHubWebhook(recorder, gitHubWebhookRequest(testGitHubWebhookKey, "pull_request", "delivery-1", signGitHubBody(testGitHubWebhookSecret, body), body))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on redelivery, got %d", recorder.Code)
	}
	if got := countWebhookDeliveries(t, db, testGitHubWebhookKey); got != 1 {
		t.Fatalf("expected 1 recorded delivery, got %d", got)
	}
}

func TestReceiveGitHubWebhookRejectsBadSignature(t *testing.T) {
	handler, db := newSCMWebhookHandlerTest(t)
	body := gitHubPullRequestBody(t, 424242)

	cases := map[string]string{
		"missing":      "",
		"wrong secret": signGitHubBody("not-the-secret", body),
		"wrong scheme": "sha1=" + hex.EncodeToString(bytes.Repeat([]byte{1}, 20)),
		"not hex":      "sha256=zz",
	}
	for name, signature := range cases {
		recorder := httptest.NewRecorder()
		handler.ReceiveGitHubWebhook(recorder, gitHubWebhookRequest(testGitHubWebhookKey, "pull_request", "delivery-bad-"+name, signature, body))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d: %s", name, recorder.Code, recorder.Body.String())
		}
	}
	// Tampering with a correctly signed body must also fail.
	tampered := bytes.Replace(body, []byte(`"opened"`), []byte(`"closed"`), 1)
	recorder := httptest.NewRecorder()
	handler.ReceiveGitHubWebhook(recorder, gitHubWebhookRequest(testGitHubWebhookKey, "pull_request", "delivery-tampered", signGitHubBody(testGitHubWebhookSecret, body), tampered))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("tampered body: expected 401, got %d", recorder.Code)
	}
	if got := countWebhookDeliveries(t, db, testGitHubWebhookKey); got != 0 {
		t.Fatalf("expected no recorded deliveries, got %d", got)
	}
}

func TestReceiveGitHubWebhookRejectsRepositoryMismatch(t *testing.T) {
	handler, db := newSCMWebhookHandlerTest(t)
	body := gitHubPullRequestBody(t, 999999)

	recorder := httptest.NewRecorder()
	handler.ReceiveGitHubWebhook(recorder, gitHubWebhookRequest(testGitHubWebhookKey, "pull_request", "delivery-mismatch", signGitHubBody(testGitHubWebhookSecret, body), body))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := countWebhookDeliveries(t, db, testGitHubWebhookKey); got != 0 {
		t.Fatalf("expected no recorded deliveries, got %d", got)
	}
}

func TestReceiveGitHubWebhookIgnoresPingAndUnknownEvents(t *testing.T) {
	handler, db := newSCMWebhookHandlerTest(t)
	body := []byte(`{"zen":"Keep it logically awesome.","hook_id":1,"repository":{"id":424242}}`)

	for _, event := range []string{"ping", "star", ""} {
		recorder := httptest.NewRecorder()
		handler.ReceiveGitHubWebhook(recorder, gitHubWebhookRequest(testGitHubWebhookKey, event, "delivery-"+event, signGitHubBody(testGitHubWebhookSecret, body), body))
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("event %q: expected 202, got %d: %s", event, recorder.Code, recorder.Body.String())
		}
		var response map[string]bool
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if !response["ignored"] {
			t.Fatalf("event %q: expected ignored response, got %s", event, recorder.Body.String())
		}
	}
	// An unsigned ping is still rejected: GitHub signs every delivery.
	recorder := httptest.NewRecorder()
	handler.ReceiveGitHubWebhook(recorder, gitHubWebhookRequest(testGitHubWebhookKey, "ping", "delivery-unsigned-ping", "", body))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned ping: expected 401, got %d", recorder.Code)
	}
	if got := countWebhookDeliveries(t, db, testGitHubWebhookKey); got != 0 {
		t.Fatalf("expected no recorded deliveries, got %d", got)
	}
}

func TestReceiveGitHubWebhookRejectsUnknownKeyAndWrongProvider(t *testing.T) {
	handler, _ := newSCMWebhookHandlerTest(t)
	body := gitHubPullRequestBody(t, 424242)

	recorder := httptest.NewRecorder()
	handler.ReceiveGitHubWebhook(recorder, gitHubWebhookRequest("no-such-key", "pull_request", "d", signGitHubBody(testGitHubWebhookSecret, body), body))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown key: expected 404, got %d", recorder.Code)
	}

	// A GitLab repository's key posted to the GitHub endpoint is refused
	// before any signature check.
	recorder = httptest.NewRecorder()
	handler.ReceiveGitHubWebhook(recorder, gitHubWebhookRequest(testGitLabWebhookKey, "pull_request", "d", signGitHubBody(testGitLabWebhookSecret, body), body))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("gitlab key on github endpoint: expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestReceiveGitLabWebhookStillAcceptsTokenDelivery(t *testing.T) {
	handler, db := newSCMWebhookHandlerTest(t)
	body := []byte(`{"object_kind":"merge_request","project":{"id":777,"path_with_namespace":"acme/gitlab"},"object_attributes":{"iid":5,"action":"open"}}`)

	req := httptest.NewRequest(http.MethodPost, "/api/scm/webhooks/gitlab/"+testGitLabWebhookKey, bytes.NewReader(body))
	req.SetPathValue("webhookKey", testGitLabWebhookKey)
	req.Header.Set("X-Gitlab-Token", testGitLabWebhookSecret)
	req.Header.Set("X-Gitlab-Event-UUID", "gitlab-delivery-1")
	recorder := httptest.NewRecorder()
	handler.ReceiveGitLabWebhook(recorder, req)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	waitForWebhookDeliveries(t, db, testGitLabWebhookKey)
	if got := countWebhookDeliveries(t, db, testGitLabWebhookKey); got != 1 {
		t.Fatalf("expected 1 recorded delivery, got %d", got)
	}

	// Wrong token is refused.
	req = httptest.NewRequest(http.MethodPost, "/api/scm/webhooks/gitlab/"+testGitLabWebhookKey, bytes.NewReader(body))
	req.SetPathValue("webhookKey", testGitLabWebhookKey)
	req.Header.Set("X-Gitlab-Token", "wrong")
	recorder = httptest.NewRecorder()
	handler.ReceiveGitLabWebhook(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
}

func TestWebhookCallbackURLUsesProviderSegment(t *testing.T) {
	handler, _ := newSCMWebhookHandlerTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/workspace-repositories/1/webhook", nil)

	github, _ := scmWebhookProviderFor(string(models.SCMProviderTypeGitHub))
	if got := handler.webhookCallbackURL(req, github, "abc"); got != "https://windshift.example.test/api/scm/webhooks/github/abc" {
		t.Fatalf("unexpected GitHub callback URL %q", got)
	}
	gitlab, _ := scmWebhookProviderFor(string(models.SCMProviderTypeGitLab))
	if got := handler.webhookCallbackURL(req, gitlab, "abc"); got != "https://windshift.example.test/api/scm/webhooks/gitlab/abc" {
		t.Fatalf("unexpected GitLab callback URL %q", got)
	}
	if _, ok := scmWebhookProviderFor(string(models.SCMProviderTypeGitea)); ok {
		t.Fatal("gitea must not advertise a manual webhook receiver")
	}

	// Without a configured base URL the callback is derived from the request.
	handler.baseURL = ""
	req.Host = "internal.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "public.example.test")
	if got := handler.webhookCallbackURL(req, github, "abc"); got != "https://public.example.test/api/scm/webhooks/github/abc" {
		t.Fatalf("unexpected forwarded callback URL %q", got)
	}
}
