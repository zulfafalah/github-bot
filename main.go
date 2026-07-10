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
	"strings"
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

	if err := initState(cfg.StateFilePath); err != nil {
		log.Fatalf("init state: %v", err)
	}
	go runMonthlyScheduler(cfg)

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

		switch r.Header.Get("X-GitHub-Event") {
		case "issues":
			var ev issueEvent
			if err := json.Unmarshal(body, &ev); err != nil {
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			if ev.Action == "opened" {
				go handleIssueOpened(cfg, ev)
			}

		case "workflow_run":
			var ev workflowRunEvent
			if err := json.Unmarshal(body, &ev); err != nil {
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			go handleWorkflowRun(cfg, ev)

		default:
			w.WriteHeader(http.StatusOK)
		}
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

	// SelfHostedLabel is the runs-on label used when switching a repo's
	// workflows off ubuntu-latest.
	SelfHostedLabel string
	// RateLimitKeywords are matched (case-insensitively) against failed
	// jobs' names and annotations to decide whether a failure was caused
	// by a GitHub Actions rate/usage limit.
	RateLimitKeywords []string
	// TriggerConclusions are the workflow_run conclusions that trigger a
	// rate-limit check at all.
	TriggerConclusions []string
	// StateFilePath persists which repos/workflows were switched to
	// self-hosted, so the monthly job knows what to revert.
	StateFilePath string
}

var defaultRateLimitKeywords = []string{
	"rate limit",
	"secondary rate limit",
	"api rate limit",
	"usage limit",
	"spending limit",
	"concurrency limit",
	"concurrent jobs",
	"exceeded the number of",
	"you have exceeded",
	"insufficient funds",
	"minutes quota",
	"included minutes",
}

func loadConfig() (*config, error) {
	cfg := &config{
		Port:               envOr("PORT", "8080"),
		WebhookSecret:      os.Getenv("GITHUB_WEBHOOK_SECRET"),
		AppID:              os.Getenv("GITHUB_APP_ID"),
		PrivateKeyPath:     os.Getenv("GITHUB_PRIVATE_KEY_PATH"),
		PrivateKeyPEM:      os.Getenv("GITHUB_PRIVATE_KEY"),
		CommentMessage:     os.Getenv("COMMENT_MESSAGE"),
		SelfHostedLabel:    envOr("SELF_HOSTED_RUNNER_LABEL", "self-hosted"),
		RateLimitKeywords:  envListOr("RATE_LIMIT_KEYWORDS", defaultRateLimitKeywords),
		TriggerConclusions: envListOr("TRIGGER_CONCLUSIONS", []string{"failure", "startup_failure"}),
		StateFilePath:      envOr("STATE_FILE_PATH", "./state.json"),
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

// envListOr reads a comma-separated env var into a trimmed string slice,
// or returns fallback if unset.
func envListOr(key string, fallback []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
