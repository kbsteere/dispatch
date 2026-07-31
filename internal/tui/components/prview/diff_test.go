package prview

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// realWorldDiff is taken from gh-dash commit 49f37e4.
const realWorldDiff = `diff --git a/internal/config/parser.go b/internal/config/parser.go
index 90075a1..96a2283 100644
--- a/internal/config/parser.go
+++ b/internal/config/parser.go
@@ -33,8 +33,6 @@ var conf = koanf.Conf{

 const DashDir = "gh-dash"

-const RepoConfigFileName = ".gh-dash.yml"
-
 const ConfigYmlFileName = "config.yml"

 // TODO: use this
@@ -624,8 +622,10 @@ func (parser ConfigParser) getProvidedConfigPath(location Location) string {
 		repoConfigYaml := basename + ".yaml"
 		if _, err := os.Stat(repoConfigYml); err == nil {
 			userProvidedCfgPath = repoConfigYml
+			log.Info("Using config at repo root", "path", repoConfigYml)
 		} else if _, err := os.Stat(repoConfigYaml); err == nil {
 			userProvidedCfgPath = repoConfigYaml
+			log.Info("Using config at repo root", "path", repoConfigYaml)
 		}
 	}
`

func TestClassifyDiffLine(t *testing.T) {
	tests := []struct {
		line string
		want diffLineKind
	}{
		{"diff --git a/internal/config/parser.go b/internal/config/parser.go", diffLineHeader},
		{"index 90075a1..96a2283 100644", diffLineHeader},
		{"--- a/internal/config/parser.go", diffLineHeader},
		{"+++ b/internal/config/parser.go", diffLineHeader},
		{"new file mode 100644", diffLineHeader},
		{"deleted file mode 100644", diffLineHeader},
		{"rename from internal/tui/keys/prKeys.go", diffLineHeader},
		{"rename to internal/tui/keys/pr_keys.go", diffLineHeader},
		{"Binary files a/logo.png and b/logo.png differ", diffLineHeader},
		{"@@ -33,8 +33,6 @@ var conf = koanf.Conf{", diffLineHunk},
		{`-const RepoConfigFileName = ".gh-dash.yml"`, diffLineDeletion},
		{`+			log.Info("Using config at repo root", "path", repoConfigYml)`, diffLineAddition},
		{` const DashDir = "gh-dash"`, diffLineContext},
		{"", diffLineContext},
	}

	for _, tt := range tests {
		if got := classifyDiffLine(tt.line); got != tt.want {
			t.Errorf("classifyDiffLine(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestRenderDiff(t *testing.T) {
	styles := diffStyles{
		Header:   lipgloss.NewStyle(),
		Hunk:     lipgloss.NewStyle(),
		Addition: lipgloss.NewStyle(),
		Deletion: lipgloss.NewStyle(),
		Context:  lipgloss.NewStyle(),
		Hint:     lipgloss.NewStyle(),
	}

	t.Run("renders every line of a small diff", func(t *testing.T) {
		got := renderDiff(realWorldDiff, 120, maxDiffRenderedLines, styles)

		wantLines := len(strings.Split(strings.TrimRight(realWorldDiff, "\n"), "\n"))
		if gotLines := len(strings.Split(got, "\n")); gotLines != wantLines {
			t.Errorf("rendered %d lines, want %d", gotLines, wantLines)
		}
		if !strings.Contains(got, "diff --git a/internal/config/parser.go") {
			t.Error("rendered diff is missing the file header")
		}
	})

	t.Run("truncates long diffs with a hint", func(t *testing.T) {
		maxLines := 5
		got := renderDiff(realWorldDiff, 120, maxLines, styles)

		want := fmt.Sprintf("Diff truncated at %d lines.", maxLines)
		if !strings.Contains(got, want) {
			t.Errorf("rendered diff is missing truncation hint %q", want)
		}
		// maxLines of diff + a blank line + the hint line.
		if gotLines := len(strings.Split(got, "\n")); gotLines != maxLines+2 {
			t.Errorf("rendered %d lines, want %d", gotLines, maxLines+2)
		}
	})

	t.Run("clips lines wider than the pane", func(t *testing.T) {
		width := 20
		got := renderDiff(realWorldDiff, width, maxDiffRenderedLines, styles)

		for i, line := range strings.Split(got, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("line %d has width %d, want <= %d", i, w, width)
			}
		}
	})

	t.Run("shows placeholder for empty diff", func(t *testing.T) {
		if got := renderDiff("", 120, maxDiffRenderedLines, styles); got != "No changes." {
			t.Errorf("renderDiff(empty) = %q, want %q", got, "No changes.")
		}
	})
}
