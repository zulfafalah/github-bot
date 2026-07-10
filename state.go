package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// switchedEntry records which workflow files in a repo were switched to a
// self-hosted runner, so the monthly job knows what to revert.
type switchedEntry struct {
	InstallationID int64    `json:"installation_id"`
	Paths          []string `json:"paths"`
}

type stateData struct {
	// Switched is keyed by repo full_name ("owner/repo").
	Switched map[string]switchedEntry `json:"switched"`
	// LastMonthlyRun is the "YYYY-MM" the monthly revert last ran, so a
	// restart mid-month doesn't trigger it twice.
	LastMonthlyRun string `json:"last_monthly_run"`
}

var (
	stateMu   sync.Mutex
	state     stateData
	statePath string
)

func initState(path string) error {
	stateMu.Lock()
	defer stateMu.Unlock()

	statePath = path
	state = stateData{Switched: map[string]switchedEntry{}}

	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return err
	}
	if state.Switched == nil {
		state.Switched = map[string]switchedEntry{}
	}
	return nil
}

// saveStateLocked persists state to disk. Caller must hold stateMu.
func saveStateLocked() error {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath, b, 0o644)
}

func hasSwitchedEntry(repo string) bool {
	stateMu.Lock()
	defer stateMu.Unlock()
	_, ok := state.Switched[repo]
	return ok
}

func recordSwitch(repo string, installationID int64, paths []string) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	state.Switched[repo] = switchedEntry{InstallationID: installationID, Paths: paths}
	return saveStateLocked()
}

func clearSwitch(repo string) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	delete(state.Switched, repo)
	return saveStateLocked()
}

func snapshotSwitched() map[string]switchedEntry {
	stateMu.Lock()
	defer stateMu.Unlock()
	out := make(map[string]switchedEntry, len(state.Switched))
	for k, v := range state.Switched {
		out[k] = v
	}
	return out
}

// runMonthlyScheduler runs forever, checking once an hour whether it's the
// 1st of the month and the monthly revert hasn't run yet this month.
func runMonthlyScheduler(cfg *config) {
	for {
		checkAndRunMonthly(cfg)
		time.Sleep(1 * time.Hour)
	}
}

func checkAndRunMonthly(cfg *config) {
	now := time.Now().UTC()
	if now.Day() != 1 {
		return
	}
	monthKey := now.Format("2006-01")

	stateMu.Lock()
	alreadyRan := state.LastMonthlyRun == monthKey
	stateMu.Unlock()
	if alreadyRan {
		return
	}

	runMonthlyRevert(cfg)

	stateMu.Lock()
	state.LastMonthlyRun = monthKey
	err := saveStateLocked()
	stateMu.Unlock()
	if err != nil {
		log.Printf("checkAndRunMonthly: save state: %v", err)
	}
}

func runMonthlyRevert(cfg *config) {
	for repo, entry := range snapshotSwitched() {
		token, err := installationToken(cfg, entry.InstallationID)
		if err != nil {
			log.Printf("runMonthlyRevert: get installation token for %s: %v", repo, err)
			continue
		}
		if err := revertRepoToUbuntu(cfg, token, repo, entry); err != nil {
			log.Printf("runMonthlyRevert: revert %s: %v", repo, err)
			continue
		}
		if err := clearSwitch(repo); err != nil {
			log.Printf("runMonthlyRevert: clear state for %s: %v", repo, err)
		}
	}
}
