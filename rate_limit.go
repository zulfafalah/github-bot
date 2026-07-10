package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

const workflowsDir = ".github/workflows"

type workflowRunEvent struct {
	Action      string `json:"action"`
	WorkflowRun struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Path       string `json:"path"`
		Conclusion string `json:"conclusion"`
		Status     string `json:"status"`
		HTMLURL    string `json:"html_url"`
	} `json:"workflow_run"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// handleWorkflowRun reacts to a completed workflow_run webhook event: if the
// run failed with a conclusion we watch for, and the failure looks like a
// GitHub Actions rate/usage limit, it opens a PR switching that repo's
// workflows to a self-hosted runner.
func handleWorkflowRun(cfg *config, ev workflowRunEvent) {
	if ev.Action != "completed" {
		return
	}
	if !containsFold(cfg.TriggerConclusions, ev.WorkflowRun.Conclusion) {
		return
	}

	repo := ev.Repository.FullName

	if hasSwitchedEntry(repo) {
		log.Printf("workflow_run: %s already switched to self-hosted, skipping", repo)
		return
	}

	token, err := installationToken(cfg, ev.Installation.ID)
	if err != nil {
		log.Printf("workflow_run: get installation token: %v", err)
		return
	}

	matched, reason, err := runFailedDueToLimit(token, repo, ev.WorkflowRun.ID, cfg.RateLimitKeywords)
	if err != nil {
		log.Printf("workflow_run: inspect run %d in %s: %v", ev.WorkflowRun.ID, repo, err)
		return
	}
	if !matched {
		return
	}

	log.Printf("workflow_run: %s run %d failed matching %q, switching runners to self-hosted", repo, ev.WorkflowRun.ID, reason)

	if err := switchRepoToSelfHosted(cfg, token, repo, ev.Installation.ID); err != nil {
		log.Printf("workflow_run: switch %s to self-hosted: %v", repo, err)
	}
}

// runFailedDueToLimit inspects the failed jobs of a run (and their
// annotations) for text matching any of the configured rate-limit keywords.
func runFailedDueToLimit(token, repo string, runID int64, keywords []string) (matched bool, reason string, err error) {
	jobs, err := listRunJobs(token, repo, runID)
	if err != nil {
		return false, "", err
	}

	var sb strings.Builder
	for _, j := range jobs {
		if j.Conclusion != "failure" {
			continue
		}
		sb.WriteString(j.Name)
		sb.WriteString("\n")

		anns, err := listJobAnnotations(token, repo, j.ID)
		if err != nil {
			// Best-effort: a missing annotation shouldn't abort detection, but
			// log it since a permissions gap (the App needs "Checks: Read" to
			// call this endpoint) would otherwise fail silently as "no match".
			log.Printf("runFailedDueToLimit: get annotations for job %d in %s: %v", j.ID, repo, err)
			continue
		}
		for _, a := range anns {
			sb.WriteString(a.Title)
			sb.WriteString(" ")
			sb.WriteString(a.Message)
			sb.WriteString("\n")
		}
	}

	text := strings.ToLower(sb.String())
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(text, strings.ToLower(kw)) {
			return true, kw, nil
		}
	}
	return false, "", nil
}

// switchRepoToSelfHosted patches every ubuntu-latest runner in the repo's
// workflow files to the configured self-hosted label and opens a PR.
func switchRepoToSelfHosted(cfg *config, token, repo string, installationID int64) error {
	info, err := getRepoInfo(token, repo)
	if err != nil {
		return fmt.Errorf("get repo info: %w", err)
	}
	base := info.DefaultBranch

	baseSHA, err := getBranchSHA(token, repo, base)
	if err != nil {
		return fmt.Errorf("get base branch sha: %w", err)
	}

	entries, err := listDirContents(token, repo, workflowsDir, base)
	if err != nil {
		return fmt.Errorf("list workflows dir: %w", err)
	}

	type changedFile struct {
		path    string
		content string
		sha     string
	}
	var changed []changedFile

	for _, e := range entries {
		if e.Type != "file" || !isWorkflowFile(e.Name) {
			continue
		}
		content, sha, err := getFileContent(token, repo, e.Path, base)
		if err != nil {
			return fmt.Errorf("get content of %s: %w", e.Path, err)
		}
		newContent, ok := patchRunnerLabel(content, "ubuntu-latest", cfg.SelfHostedLabel)
		if !ok {
			continue
		}
		changed = append(changed, changedFile{e.Path, newContent, sha})
	}

	if len(changed) == 0 {
		log.Printf("switchRepoToSelfHosted: no ubuntu-latest runners found in %s workflows", repo)
		return nil
	}

	branch := fmt.Sprintf("bot/self-hosted-runner-%d", time.Now().Unix())
	if err := createBranch(token, repo, branch, baseSHA); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}

	var paths []string
	for _, c := range changed {
		msg := fmt.Sprintf("ci: switch %s to self-hosted runner", c.path)
		if err := putFileContent(token, repo, c.path, branch, msg, c.content, c.sha); err != nil {
			return fmt.Errorf("update %s: %w", c.path, err)
		}
		paths = append(paths, c.path)
	}

	title := "ci: switch GitHub Actions runners to self-hosted"
	body := fmt.Sprintf(
		"A recent workflow run failed because it looks like it hit a GitHub Actions rate/usage limit. "+
			"This PR switches `runs-on: ubuntu-latest` to `runs-on: %s` in:\n\n%s\n\n"+
			"On the 1st of next month a follow-up PR will revert these back to `ubuntu-latest`.",
		cfg.SelfHostedLabel, bulletList(paths),
	)

	prURL, err := createPullRequest(token, repo, title, branch, base, body)
	if err != nil {
		return fmt.Errorf("create pull request: %w", err)
	}
	log.Printf("switchRepoToSelfHosted: opened %s", prURL)

	return recordSwitch(repo, installationID, paths)
}

// revertRepoToUbuntu patches the previously-switched workflow files back to
// ubuntu-latest and opens a PR. Called from the monthly scheduler.
func revertRepoToUbuntu(cfg *config, token, repo string, entry switchedEntry) error {
	info, err := getRepoInfo(token, repo)
	if err != nil {
		return fmt.Errorf("get repo info: %w", err)
	}
	base := info.DefaultBranch

	baseSHA, err := getBranchSHA(token, repo, base)
	if err != nil {
		return fmt.Errorf("get base branch sha: %w", err)
	}

	type changedFile struct {
		path    string
		content string
		sha     string
	}
	var changed []changedFile

	for _, path := range entry.Paths {
		content, sha, err := getFileContent(token, repo, path, base)
		if err != nil {
			log.Printf("revertRepoToUbuntu: get content of %s in %s: %v", path, repo, err)
			continue
		}
		newContent, ok := patchRunnerLabel(content, cfg.SelfHostedLabel, "ubuntu-latest")
		if !ok {
			continue
		}
		changed = append(changed, changedFile{path, newContent, sha})
	}

	if len(changed) == 0 {
		log.Printf("revertRepoToUbuntu: nothing to revert in %s", repo)
		return nil
	}

	branch := fmt.Sprintf("bot/ubuntu-latest-runner-%d", time.Now().Unix())
	if err := createBranch(token, repo, branch, baseSHA); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}

	var paths []string
	for _, c := range changed {
		msg := fmt.Sprintf("ci: revert %s to ubuntu-latest runner", c.path)
		if err := putFileContent(token, repo, c.path, branch, msg, c.content, c.sha); err != nil {
			return fmt.Errorf("update %s: %w", c.path, err)
		}
		paths = append(paths, c.path)
	}

	title := "ci: revert GitHub Actions runners to ubuntu-latest"
	body := fmt.Sprintf(
		"Monthly automated reset: reverting `runs-on: %s` back to `runs-on: ubuntu-latest` in:\n\n%s",
		cfg.SelfHostedLabel, bulletList(paths),
	)

	prURL, err := createPullRequest(token, repo, title, branch, base, body)
	if err != nil {
		return fmt.Errorf("create pull request: %w", err)
	}
	log.Printf("revertRepoToUbuntu: opened %s", prURL)
	return nil
}

func bulletList(items []string) string {
	var sb strings.Builder
	for _, it := range items {
		sb.WriteString("- `")
		sb.WriteString(it)
		sb.WriteString("`\n")
	}
	return sb.String()
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
