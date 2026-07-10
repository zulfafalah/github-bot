package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// githubRequest performs an authenticated GitHub API call and returns the
// response together with its fully-read body, so callers can inspect the
// status code after the connection is closed.
func githubRequest(token, method, endpoint string, payload any) (*http.Response, []byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, nil, err
	}
	setHeaders(req, token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, respBody, nil
}

type repoInfo struct {
	DefaultBranch string `json:"default_branch"`
}

func getRepoInfo(token, repo string) (*repoInfo, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s", repo)
	resp, body, err := githubRequest(token, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get repo %d: %s", resp.StatusCode, body)
	}
	var out repoInfo
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func getBranchSHA(token, repo, branch string) (string, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/git/ref/heads/%s", repo, branch)
	resp, body, err := githubRequest(token, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get ref %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	return out.Object.SHA, nil
}

func createBranch(token, repo, newBranch, fromSHA string) error {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/git/refs", repo)
	payload := map[string]string{
		"ref": "refs/heads/" + newBranch,
		"sha": fromSHA,
	}
	resp, body, err := githubRequest(token, http.MethodPost, endpoint, payload)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("create ref %d: %s", resp.StatusCode, body)
	}
	return nil
}

type contentEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// listDirContents lists files in a repo directory at ref. It returns an
// empty slice (no error) if the directory doesn't exist.
func listDirContents(token, repo, path, ref string) ([]contentEntry, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", repo, path, ref)
	resp, body, err := githubRequest(token, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list contents %d: %s", resp.StatusCode, body)
	}
	var out []contentEntry
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// getFileContent fetches a file's decoded content and blob sha at ref.
func getFileContent(token, repo, path, ref string) (content string, sha string, err error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", repo, path, ref)
	resp, body, err := githubRequest(token, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("get content %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Content)
	if err != nil {
		return "", "", fmt.Errorf("decode content: %w", err)
	}
	return string(decoded), out.SHA, nil
}

func putFileContent(token, repo, path, branch, message, newContent, sha string) error {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", repo, path)
	payload := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(newContent)),
		"sha":     sha,
		"branch":  branch,
	}
	resp, body, err := githubRequest(token, http.MethodPut, endpoint, payload)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("put content %d: %s", resp.StatusCode, body)
	}
	return nil
}

func createPullRequest(token, repo, title, head, base, body string) (htmlURL string, err error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/pulls", repo)
	payload := map[string]string{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	resp, respBody, err := githubRequest(token, http.MethodPost, endpoint, payload)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create pull %d: %s", resp.StatusCode, respBody)
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", err
	}
	return out.HTMLURL, nil
}

type actionsJob struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

func listRunJobs(token, repo string, runID int64) ([]actionsJob, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d/jobs", repo, runID)
	resp, body, err := githubRequest(token, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list jobs %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Jobs []actionsJob `json:"jobs"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}

type jobAnnotation struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

func listJobAnnotations(token, repo string, jobID int64) ([]jobAnnotation, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/check-runs/%d/annotations", repo, jobID)
	resp, body, err := githubRequest(token, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list annotations %d: %s", resp.StatusCode, body)
	}
	var out []jobAnnotation
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
