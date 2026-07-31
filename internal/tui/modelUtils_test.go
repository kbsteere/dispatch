package tui

import "testing"

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty becomes empty quotes", in: "", want: "''"},
		{name: "safe branch name unquoted", in: "feature/foo-1.2_bar", want: "feature/foo-1.2_bar"},
		{name: "repo path unquoted", in: "/home/user/repo", want: "/home/user/repo"},
		{name: "semicolon injection quoted", in: "x; rm -rf /", want: "'x; rm -rf /'"},
		{name: "command substitution quoted", in: "$(whoami)", want: "'$(whoami)'"},
		{name: "backtick quoted", in: "a`id`b", want: "'a`id`b'"},
		{name: "embedded single quote escaped", in: "it's", want: `'it'\''s'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellQuote(tt.in); got != tt.want {
				t.Errorf("shellQuote(%q): got = %q, want = %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveTemplateInputQuotesValues(t *testing.T) {
	ctxData := map[string]any{
		"RepoName":    "owner/repo",
		"HeadRefName": "x; rm -rf /",
		"IssueNumber": 42,
	}

	got := resolveTemplateInput(&ctxData, nil, "/home/user/repo")

	if got["HeadRefName"] != "'x; rm -rf /'" {
		t.Errorf("HeadRefName: got = %v, want quoted injection", got["HeadRefName"])
	}
	if got["RepoName"] != "owner/repo" {
		t.Errorf("RepoName: got = %v, want unquoted safe value", got["RepoName"])
	}
	if got["RepoPath"] != "/home/user/repo" {
		t.Errorf("RepoPath: got = %v, want fallback repo path", got["RepoPath"])
	}
	// Non-string values are left untouched for the template to render.
	if got["IssueNumber"] != 42 {
		t.Errorf("IssueNumber: got = %v, want = 42", got["IssueNumber"])
	}
}
