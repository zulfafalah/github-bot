package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type searchResult struct {
	TotalCount int `json:"total_count"`
}

// isFirstIssue reports whether author has exactly one issue (the one that
// just triggered this run) in repo, via the GitHub Search API.
func isFirstIssue(token, repo, author string) (bool, error) {
	q := fmt.Sprintf("repo:%s type:issue author:%s", repo, author)
	endpoint := "https://api.github.com/search/issues?q=" + url.QueryEscape(q)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	setHeaders(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("search API %d: %s", resp.StatusCode, body)
	}

	var res searchResult
	if err := json.Unmarshal(body, &res); err != nil {
		return false, err
	}
	return res.TotalCount <= 1, nil
}

func postComment(token, repo string, number int, body string) error {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repo, number)
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	setHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("comments API %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

func setHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "fal-github-bot")
}
