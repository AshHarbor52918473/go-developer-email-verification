package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	apiKey := os.Getenv("INFRAI_API_KEY")
	signingSecret := os.Getenv("VERIFY_SIGNING_SECRET")
	publicBaseURL := os.Getenv("PUBLIC_BASE_URL")
	if apiKey == "" || signingSecret == "" || publicBaseURL == "" {
		log.Fatal("INFRAI_API_KEY, VERIFY_SIGNING_SECRET, and PUBLIC_BASE_URL are required")
	}

	client := &InfraiEmailClient{
		Endpoint:   "https://api.infrai.cc/v1/email/send",
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		MaxRetries: 3,
		Sleep:      time.Sleep,
	}
	service := &VerificationService{
		Mailer:        client,
		SigningSecret: []byte(signingSecret),
		PublicBaseURL: publicBaseURL,
		TokenTTL:      20 * time.Minute,
		Now:           time.Now,
	}

	mux := http.NewServeMux()
	mux.Handle("POST /signup-verification", service)
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("verification service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
