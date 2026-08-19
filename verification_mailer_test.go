package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordingMailer struct {
	calls int
	key   string
}

func (m *recordingMailer) Send(_ context.Context, key, _, _, _ string) (string, error) {
	m.calls++
	m.key = key
	return "msg_release_42", nil
}

func TestReleaseDecision(t *testing.T) {
	tests := []struct {
		name       string
		buildState string
		wantStatus string
		wantCalls  int
	}{
		{name: "successful build dispatches verification", buildState: "succeeded", wantStatus: "pending_verification", wantCalls: 1},
		{name: "failed build blocks release operation", buildState: "failed", wantStatus: "blocked", wantCalls: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mailer := &recordingMailer{}
			service := VerificationService{Mailer: mailer, SigningSecret: []byte("test-secret"), PublicBaseURL: "https://developers.example.test", TokenTTL: 20 * time.Minute, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
			input := SignupRequest{DeveloperID: "dev_7", Email: "engineer@example.test", ReleaseID: "release_42", Build: BuildEvent{ID: "build_evt_42", Status: tt.buildState, CommitSHA: "abc123"}}

			result, _, err := service.Process(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tt.wantStatus || mailer.calls != tt.wantCalls {
				t.Fatalf("status=%q calls=%d, want status=%q calls=%d", result.Status, mailer.calls, tt.wantStatus, tt.wantCalls)
			}
			if mailer.calls == 1 && mailer.key != input.Build.ID {
				t.Fatalf("idempotency key=%q, want %q", mailer.key, input.Build.ID)
			}
		})
	}
}

func TestEmailRequestBoundaryAndRateLimitRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			t.Errorf("method=%s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Idempotency-Key") != "build_evt_9" {
			t.Errorf("request headers were not propagated")
		}
		body, _ := io.ReadAll(r.Body)
		var fields map[string]any
		_ = json.Unmarshal(body, &fields)
		if len(fields) != 3 || fields["to"] != "dev@example.test" || fields["subject"] != "Verify" || !strings.Contains(fields["html"].(string), "Confirm") {
			t.Errorf("unexpected body: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"data":{},"error":{"message":"rate limit"},"metadata":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"message_id":"msg_9"},"error":null,"metadata":{"provider":"active"}}`))
	}))
	defer server.Close()

	client := InfraiEmailClient{Endpoint: server.URL, APIKey: "test-key", HTTPClient: server.Client(), MaxRetries: 2, Sleep: func(time.Duration) {}}
	messageID, err := client.Send(context.Background(), "build_evt_9", "dev@example.test", "Verify", "<p>Confirm</p>")
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "msg_9" || requests != 2 {
		t.Fatalf("messageID=%q requests=%d", messageID, requests)
	}
}
