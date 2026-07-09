// fal-github-bot is a GitHub App webhook server: it greets a user with a
// comment the first time they open an issue in any repo the app is
// installed on.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

type issueEvent struct {
	Action string `json:"action"`
	Issue  struct {
		Number int `json:"number"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"issue"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/webhook", webhookHandler(cfg))
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("listening on :%s", cfg.Port)
	log.Fatal(http.ListenAndServe(":"+cfg.Port, nil))
}

func webhookHandler(cfg *config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}

		if !verifySignature(cfg.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		if r.Header.Get("X-GitHub-Event") != "issues" {
			w.WriteHeader(http.StatusOK)
			return
		}

		var ev issueEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)

		if ev.Action != "opened" {
			return
		}

		go handleIssueOpened(cfg, ev)
	}
}

func handleIssueOpened(cfg *config, ev issueEvent) {
	token, err := installationToken(cfg, ev.Installation.ID)
	if err != nil {
		log.Printf("get installation token: %v", err)
		return
	}

	repo := ev.Repository.FullName
	author := ev.Issue.User.Login
	number := ev.Issue.Number

	first, err := isFirstIssue(token, repo, author)
	if err != nil {
		log.Printf("check first issue: %v", err)
		return
	}
	if !first {
		log.Printf("%s already has other issues in %s, skipping", author, repo)
		return
	}

	msg := cfg.CommentMessage
	if msg == "" {
		msg = fmt.Sprintf("Selamat datang @%s! 👋 Ini issue pertama kamu di repo ini, terima kasih sudah lapor.", author)
	}

	if err := postComment(token, repo, number, msg); err != nil {
		log.Printf("post comment: %v", err)
		return
	}

	log.Printf("greeted %s on %s#%d", author, repo, number)
}

func verifySignature(secret string, body []byte, sigHeader string) bool {
	if sigHeader == "" || secret == "" {
		return false
	}
	const prefix = "sha256="
	if len(sigHeader) <= len(prefix) || sigHeader[:len(prefix)] != prefix {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)

	got, err := hex.DecodeString(sigHeader[len(prefix):])
	if err != nil {
		return false
	}

	return subtle.ConstantTimeCompare(expected, got) == 1
}

type config struct {
	Port           string
	WebhookSecret  string
	AppID          string
	PrivateKeyPath string
	PrivateKeyPEM  string
	CommentMessage string
}

func loadConfig() (*config, error) {
	cfg := &config{
		Port:           envOr("PORT", "8080"),
		WebhookSecret:  os.Getenv("GITHUB_WEBHOOK_SECRET"),
		AppID:          os.Getenv("GITHUB_APP_ID"),
		PrivateKeyPath: os.Getenv("GITHUB_PRIVATE_KEY_PATH"),
		PrivateKeyPEM:  os.Getenv("GITHUB_PRIVATE_KEY"),
		CommentMessage: os.Getenv("COMMENT_MESSAGE"),
	}

	if cfg.WebhookSecret == "" {
		return nil, fmt.Errorf("GITHUB_WEBHOOK_SECRET not set")
	}
	if cfg.AppID == "" {
		return nil, fmt.Errorf("GITHUB_APP_ID not set")
	}
	if cfg.PrivateKeyPath == "" && cfg.PrivateKeyPEM == "" {
		return nil, fmt.Errorf("GITHUB_PRIVATE_KEY_PATH or GITHUB_PRIVATE_KEY must be set")
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
