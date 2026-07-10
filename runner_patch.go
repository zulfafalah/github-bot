package main

import (
	"regexp"
	"strings"
)

// patchRunnerLabel rewrites a plain-scalar `runs-on: <from>` line to use
// <to> instead, preserving indentation and optional quoting. It reports
// whether any replacement was made. Array-style (`runs-on: [ubuntu-latest]`)
// and matrix-expression runners are intentionally left untouched.
func patchRunnerLabel(content, from, to string) (string, bool) {
	re := regexp.MustCompile(`(?m)^(\s*runs-on:\s*)(['"]?)` + regexp.QuoteMeta(from) + `(['"]?)\s*$`)
	if !re.MatchString(content) {
		return content, false
	}
	// Escape "$" in the replacement label so it can't be read back as a
	// capture-group reference by ReplaceAllString.
	safeTo := strings.ReplaceAll(to, "$", "$$")
	newContent := re.ReplaceAllString(content, "${1}${2}"+safeTo+"${3}")
	return newContent, true
}

// hasRunnerLabel reports whether content has a plain-scalar `runs-on: label` line.
func hasRunnerLabel(content, label string) bool {
	re := regexp.MustCompile(`(?m)^(\s*runs-on:\s*)(['"]?)` + regexp.QuoteMeta(label) + `(['"]?)\s*$`)
	return re.MatchString(content)
}

func isWorkflowFile(name string) bool {
	return len(name) > 4 && (name[len(name)-4:] == ".yml" || (len(name) > 5 && name[len(name)-5:] == ".yaml"))
}
