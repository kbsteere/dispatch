package git

import "testing"

func TestGetRepoShortName(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "https url", url: "https://github.com/kbsteere/dispatch", want: "kbsteere/dispatch"},
		{
			name: "https url with .git suffix",
			url:  "https://github.com/kbsteere/dispatch.git",
			want: "kbsteere/dispatch",
		},
		{name: "already short name", url: "owner/repo", want: "owner/repo"},
		{
			name: "ssh url only strips suffix",
			url:  "git@github.com:foo/bar.git",
			want: "git@github.com:foo/bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetRepoShortName(tt.url); got != tt.want {
				t.Errorf("GetRepoShortName(%q): got = %q, want = %q", tt.url, got, tt.want)
			}
		})
	}
}
