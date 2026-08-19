package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type BuildEvent struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	CommitSHA string `json:"commit_sha"`
}

type SignupRequest struct {
	DeveloperID string     `json:"developer_id"`
	Email       string     `json:"email"`
	ReleaseID   string     `json:"release_id"`
	Build       BuildEvent `json:"build_event"`
}

type ReleaseResult struct {
	Operation  string   `json:"operation"`
	Status     string   `json:"status"`
	MessageID  string   `json:"message_id,omitempty"`
	Diagnostic string   `json:"diagnostic"`
	Checks     []string `json:"checks"`
}

type EmailSender interface {
	Send(ctx context.Context, idempotencyKey, to, subject, htmlBody string) (string, error)
}

type VerificationService struct {
	Mailer        EmailSender
	SigningSecret []byte
	PublicBaseURL string
	TokenTTL      time.Duration
	Now           func() time.Time
}

func (s *VerificationService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var input SignupRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, ReleaseResult{Operation: "email_verification", Status: "rejected", Diagnostic: "invalid signup event"})
		return
	}

	result, status, err := s.Process(r.Context(), input)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, ReleaseResult{Operation: "email_verification", Status: "deferred", Diagnostic: err.Error()})
		return
	}
	writeJSON(w, status, result)
}

func (s *VerificationService) Process(ctx context.Context, input SignupRequest) (ReleaseResult, int, error) {
	checks := []string{"build_event_received", "release_identity_bound"}
	if input.Build.Status != "succeeded" {
		return ReleaseResult{Operation: "email_verification", Status: "blocked", Diagnostic: "build must succeed before developer verification", Checks: checks}, http.StatusConflict, nil
	}
	if input.Build.ID == "" || input.Build.CommitSHA == "" || input.DeveloperID == "" || input.ReleaseID == "" || !validEmail(input.Email) {
		return ReleaseResult{Operation: "email_verification", Status: "rejected", Diagnostic: "developer, release, build, commit, and email are required", Checks: checks}, http.StatusUnprocessableEntity, nil
	}

	expires := s.Now().UTC().Add(s.TokenTTL).Unix()
	token := s.signToken(input.DeveloperID, input.ReleaseID, input.Email, expires)
	link := strings.TrimRight(s.PublicBaseURL, "/") + "/verify-email?token=" + url.QueryEscape(token)
	body := fmt.Sprintf("<p>Confirm the email for release <strong>%s</strong>.</p><p><a href=\"%s\">Verify developer email</a></p><p>This link expires at %s.</p>", html.EscapeString(input.ReleaseID), html.EscapeString(link), time.Unix(expires, 0).UTC().Format(time.RFC3339))

	messageID, err := s.Mailer.Send(ctx, input.Build.ID, input.Email, "Verify your developer email", body)
	if err != nil {
		return ReleaseResult{}, 0, fmt.Errorf("send verification email: %w", err)
	}
	checks = append(checks, "verification_dispatched")
	return ReleaseResult{Operation: "email_verification", Status: "pending_verification", MessageID: messageID, Diagnostic: "verification link dispatched", Checks: checks}, http.StatusAccepted, nil
}

func (s *VerificationService) signToken(developerID, releaseID, email string, expires int64) string {
	payload := developerID + "\n" + releaseID + "\n" + email + "\n" + strconv.FormatInt(expires, 10)
	mac := hmac.New(sha256.New, s.SigningSecret)
	_, _ = mac.Write([]byte(payload))
	data := payload + "\n" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(data))
}

func validEmail(value string) bool {
	at := strings.LastIndex(value, "@")
	return at > 0 && at < len(value)-1 && !strings.ContainsAny(value, "\r\n")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type InfraiEmailClient struct {
	Endpoint   string
	APIKey     string
	HTTPClient *http.Client
	MaxRetries int
	Sleep      func(time.Duration)
}

type emailSendRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

type emailSendData struct {
	MessageID string `json:"message_id"`
}

type apiEnvelope struct {
	OK       bool            `json:"ok"`
	Data     emailSendData   `json:"data"`
	Error    json.RawMessage `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

func (c *InfraiEmailClient) Send(ctx context.Context, idempotencyKey, to, subject, htmlBody string) (string, error) {
	payload, err := json.Marshal(emailSendRequest{To: to, Subject: subject, HTML: htmlBody})
	if err != nil {
		return "", err
	}

	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idempotencyKey)

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < c.MaxRetries {
			delay := retryDelay(resp.Header.Get("Retry-After"), attempt)
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			c.Sleep(delay)
			continue
		}

		var envelope apiEnvelope
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return "", fmt.Errorf("decode email response: %w", decodeErr)
		}
		if !envelope.OK {
			if len(envelope.Error) == 0 || string(envelope.Error) == "null" {
				return "", fmt.Errorf("email request rejected with status %d", resp.StatusCode)
			}
			return "", fmt.Errorf("email request rejected: %s", string(envelope.Error))
		}
		if envelope.Data.MessageID == "" {
			return "", errors.New("email response omitted message_id")
		}
		return envelope.Data.MessageID, nil
	}
	return "", errors.New("email retry budget exhausted")
}

func retryDelay(value string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return time.Duration(1<<attempt) * 250 * time.Millisecond
}
