package reviewing

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coverageWorktree lays out a checkout like a mono bot: code, its test,
// its docs, and a lockfile.
func coverageWorktree(t *testing.T) string {
	t.Helper()
	wt := t.TempDir()
	for _, f := range []string{
		"bots/kev/sync.go", "bots/kev/sync_test.go", "bots/kev/README.md", "go.sum",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(wt, f)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(wt, f), []byte("x\n"), 0o600))
	}
	return wt
}

func TestReviewGaps(t *testing.T) {
	wt := coverageWorktree(t)
	diff := "/state/sessions/s1/pr.diff"
	in := func(mut func(*coverageInput)) coverageInput {
		c := coverageInput{
			Pass:     PassFull,
			Worktree: wt,
			Diff:     diff,
			Changed:  []string{"bots/kev/sync.go", "go.sum", "bots/kev/old.go"},
			Reads: []string{
				diff,
				filepath.Join(wt, "bots/kev/sync.go"),
				filepath.Join(wt, "bots/kev/sync_test.go"),
				filepath.Join(wt, "bots/kev/README.md"),
				"/home/u/Dev/cg/mono/main/docs/version-pipeline/overview.svg",
			},
			Searches: 3,
		}
		if mut != nil {
			mut(&c)
		}
		return c
	}

	tests := []struct {
		name string
		in   coverageInput
		want []string
	}{
		{"complete; lockfile and deleted file need no read", in(nil), nil},
		{
			"changed code file never read",
			in(
				func(c *coverageInput) { c.Reads = without(c.Reads, filepath.Join(wt, "bots/kev/sync.go")) },
			),
			[]string{"never read changed file bots/kev/sync.go"},
		},
		{
			"no tests read for changed code",
			in(
				func(c *coverageInput) { c.Reads = without(c.Reads, filepath.Join(wt, "bots/kev/sync_test.go")) },
			),
			[]string{"read no existing tests for the changed code"},
		},
		{
			"no docs read on a full pass",
			in(func(c *coverageInput) { c.Reads = c.Reads[:3] }),
			[]string{"read no documentation or design docs"},
		},
		{
			"never searched for usages",
			in(func(c *coverageInput) { c.Searches = 0 }),
			[]string{"never searched for usages of the changed code"},
		},
		{
			"diff never read",
			in(func(c *coverageInput) { c.Reads = c.Reads[1:] }),
			[]string{"never read the PR diff"},
		},
		{
			"comments-only pass has no requirements",
			in(func(c *coverageInput) { c.Pass = PassLedger; c.Reads = nil }),
			nil,
		},
		{
			"pr-only repo needs only the diff",
			in(func(c *coverageInput) { c.PROnly = true; c.Reads, c.Searches = []string{diff}, 0 }),
			nil,
		},
		{
			"data-only change needs no tests or searches",
			in(func(c *coverageInput) {
				c.Changed = []string{"bots/kev/README.md"}
				c.Reads = []string{diff, filepath.Join(wt, "bots/kev/README.md")}
				c.Searches = 0
			}),
			nil,
		},
		{
			// mono#66370: the author moved a cron schedule in a template.
			"follow-up with a config-only change needs only the diff since last review",
			in(func(c *coverageInput) {
				c.Pass, c.SinceLast = PassIncremental, "/state/sessions/s1/since-last-review.diff"
				c.Changed = []string{"env/400-alert-slack-bot/policies.yaml.tpl"}
				c.Reads, c.Searches = []string{c.SinceLast}, 0
			}),
			nil,
		},
		{
			"follow-up that changes code still needs tests and usages, not docs",
			in(func(c *coverageInput) {
				c.Pass, c.SinceLast = PassIncremental, "/state/sessions/s1/since-last-review.diff"
				c.Changed = []string{"bots/kev/sync.go"}
				c.Reads = []string{c.SinceLast, filepath.Join(wt, "bots/kev/sync.go")}
				c.Searches = 0
			}),
			[]string{
				"read no existing tests for the changed code",
				"never searched for usages of the changed code",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, reviewGaps(tt.in))
		})
	}
}

func TestReviewGapsAreCapped(t *testing.T) {
	wt := t.TempDir()
	var changed []string
	for i := range 15 {
		f := filepath.Join("pkg", string(rune('a'+i))+".go")
		require.NoError(t, os.MkdirAll(filepath.Join(wt, "pkg"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(wt, f), []byte("package pkg\n"), 0o600))
		changed = append(changed, f)
	}
	gaps := reviewGaps(coverageInput{
		Pass: PassFull, Worktree: wt, Diff: "/d", Reads: []string{"/d"}, Changed: changed,
	})
	require.Len(t, gaps, maxGaps+1)
	assert.Equal(t, "and 8 more", gaps[maxGaps])
}

func TestVerifiedExaminedDropsUnopenedPaths(t *testing.T) {
	// mono#66370's follow-up listed files it had read in an earlier session.
	got := verifiedExamined(Examined{
		Docs: []string{"bots/sustaining-automation/docs/alert-slack-bot.md"},
		Code: []string{"env/400-alert-slack-bot/policies.yaml.tpl", "/s/since-last-review.diff"},
	}, "/wt", []string{"/s/since-last-review.diff"})
	assert.Empty(t, got.Docs)
	assert.Equal(t, []string{"/s/since-last-review.diff"}, got.Code)
}

func TestFileKinds(t *testing.T) {
	assert.True(t, isTest("bots/kev/sync_test.go"))
	assert.True(t, isTest("bots/kev/testdata/feed.json"))
	assert.True(t, isTest("web/src/app.spec.ts"))
	assert.False(t, isTest("bots/kev/sync.go"))
	assert.True(t, isDoc("docs/arch/pipeline.drawio"))
	assert.True(t, isDoc("bots/kev/README.md"))
	assert.False(t, isDoc("bots/kev/sync.go"))
	assert.True(t, skippable("go.sum"))
	assert.True(t, skippable("pkg/api/v1/zz_generated.deepcopy.go"))
	assert.False(t, skippable("go.mod"))
	assert.True(t, isCode("cmd/main.go"))
	assert.False(t, isCode("main.tf"))
}

func without(s []string, drop string) []string {
	var out []string
	for _, v := range s {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}

func TestReviewGapsConfigChangesNeedOnlyTheDiff(t *testing.T) {
	// mono#65969 changed Go code alongside CODEOWNERS, a service catalog
	// manifest, a Terraform lock file, and boilerplate Terraform.
	wt := coverageWorktree(t)
	for _, f := range []string{
		".github/CODEOWNERS", "bots/catalog/roots.json",
		"env/400-package-metadata/.terraform.lock.hcl", "env/400-package-metadata/providers.tf",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(wt, f)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(wt, f), []byte("x\n"), 0o600))
	}
	diff := "/state/sessions/s1/pr.diff"
	gaps := reviewGaps(coverageInput{
		Pass:     PassFull,
		Worktree: wt,
		Diff:     diff,
		Changed: []string{
			"bots/kev/sync.go", ".github/CODEOWNERS", "bots/catalog/roots.json",
			"env/400-package-metadata/.terraform.lock.hcl", "env/400-package-metadata/providers.tf",
		},
		Reads: []string{
			diff,
			filepath.Join(wt, "bots/kev/sync.go"),
			filepath.Join(wt, "bots/kev/sync_test.go"),
			filepath.Join(wt, "bots/kev/README.md"),
		},
		Searches: 2,
	})
	assert.Empty(t, gaps)
}

func TestReviewGapsSecurityGuideReferences(t *testing.T) {
	wt := coverageWorktree(t)
	diff := "/state/sessions/s1/pr.diff"
	refs := []string{
		"/state/sessions/s1/security/command-injection.md",
		"/state/sessions/s1/security/fail-open.md",
	}
	in := coverageInput{
		Pass: PassFull, Worktree: wt, Diff: diff, Changed: []string{"bots/kev/sync.go"},
		Reads: []string{
			diff, filepath.Join(wt, "bots/kev/sync.go"), filepath.Join(wt, "bots/kev/sync_test.go"),
			filepath.Join(wt, "bots/kev/README.md"),
		},
		Searches: 1, SecurityRefs: refs,
	}
	assert.Equal(t, []string{"read none of the security review guide's references"}, reviewGaps(in))

	in.Reads = append(in.Reads, refs[1])
	assert.Empty(t, reviewGaps(in), "reading the references that match the change is enough")

	in.Changed, in.Reads = []string{
		"bots/kev/README.md",
	}, []string{
		diff,
		filepath.Join(wt, "bots/kev/README.md"),
	}
	assert.Empty(t, reviewGaps(in), "a docs-only change needs no security references")
}

// pr58771Files are mono#58771's changed files, with the line each one's
// section starts at in its 4,003-line diff.
var pr58771Files = []struct {
	path string
	line int
}{
	{"agents/skills/cve-remediation/SKILL.md", 1},
	{"agents/skills/java-cve-remedy/SKILL.md", 122},
	{"bots/cve-remediation/cmd/eval-cve-remediation/main.go", 135},
	{"bots/cve-remediation/cmd/eval-cve-remediation/main_test.go", 477},
	{"bots/cve-remediation/internal/cveremediation/eval_export.go", 899},
	{"bots/cve-remediation/internal/cveremediation/evasion.go", 949},
	{"bots/cve-remediation/internal/cveremediation/evasion_test.go", 2367},
	{"bots/cve-remediation/internal/cveremediation/testdata/eval/README.md", 3643},
	{
		"bots/cve-remediation/internal/cveremediation/testdata/eval/bundled-jar-no-clean-fix/case.yaml",
		3753,
	},
	{
		"bots/cve-remediation/internal/cveremediation/testdata/eval/bundled-jar-no-clean-fix/golden.yaml",
		3787,
	},
	{
		"bots/cve-remediation/internal/cveremediation/testdata/eval/bundled-jar-no-clean-fix/repo/example-java-app.yaml",
		3822,
	},
	{
		"bots/cve-remediation/internal/cveremediation/testdata/eval/malcontent-finding-escalate/case.yaml",
		3871,
	},
	{
		"bots/cve-remediation/internal/cveremediation/testdata/eval/malcontent-finding-escalate/golden.yaml",
		3928,
	},
	{
		"bots/cve-remediation/internal/cveremediation/testdata/eval/malcontent-finding-escalate/repo/example-llm-proxy.yaml",
		3962,
	},
}

// writePR58771 lays out mono#58771's worktree and a diff with its sections
// at the same lines, and returns both paths.
func writePR58771(t *testing.T) (worktree, diff string) {
	t.Helper()
	wt, dir := t.TempDir(), t.TempDir()
	var b strings.Builder
	line := 1
	for i, f := range pr58771Files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(wt, f.path)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(wt, f.path), []byte("x\n"), 0o600))
		end := 4003
		if i+1 < len(pr58771Files) {
			end = pr58771Files[i+1].line - 1
		}
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", f.path, f.path)
		for line = f.line + 1; line <= end; line++ {
			b.WriteString("+\n")
		}
	}
	diff = filepath.Join(dir, "pr.diff")
	require.NoError(t, os.WriteFile(diff, []byte(b.String()), 0o600))
	return wt, diff
}

func TestReviewGapsCreditsChangesReadInTheDiff(t *testing.T) {
	wt, diff := writePR58771(t)
	sections := diffSections(mustRead(t, diff))
	require.Len(t, sections, len(pr58771Files))
	assert.Equal(t, LineRange{From: 477, To: 898},
		sections["bots/cve-remediation/cmd/eval-cve-remediation/main_test.go"])
	assert.Equal(
		t,
		LineRange{From: 3962, To: 4003},
		sections["bots/cve-remediation/internal/cveremediation/testdata/eval/malcontent-finding-escalate/repo/example-llm-proxy.yaml"],
	)

	at := func(p string) string { return filepath.Join(wt, p) }
	changed := make([]string, 0, len(pr58771Files))
	for _, f := range pr58771Files {
		changed = append(changed, f.path)
	}
	// What the session read: the diff's first 950 lines and its tail, the
	// two new files in full, the start of main.go, the docs, and a caller.
	in := coverageInput{
		Pass:     PassFull,
		Worktree: wt,
		Diff:     diff,
		Changed:  changed,
		Reads: []string{
			diff,
			at("bots/cve-remediation/internal/cveremediation/evasion.go"),
			at("bots/cve-remediation/internal/cveremediation/evasion_test.go"),
			at("bots/cve-remediation/cmd/eval-cve-remediation/main.go"),
			at("bots/cve-remediation/internal/cveremediation/reconciler.go"),
			at("agents/skills/cve-remediation/SKILL.md"),
		},
		Ranges: map[string][]LineRange{
			diff: {readRange(0, 950), readRange(3643, 0)},
		},
		Searches:     4,
		SecurityRefs: []string{"/state/sessions/s1/security/path-traversal.md"},
	}
	assert.Equal(t, []string{"read none of the security review guide's references"},
		reviewGaps(in), "main_test.go and eval_export.go were read in the diff")

	in.Ranges = nil
	assert.Equal(t, []string{
		"never read changed file bots/cve-remediation/cmd/eval-cve-remediation/main_test.go",
		"never read changed file bots/cve-remediation/internal/cveremediation/eval_export.go",
		"read none of the security review guide's references",
	}, reviewGaps(in), "without line ranges only opened files count")

	in.Ranges = map[string][]LineRange{diff: {readRange(0, 900)}}
	assert.Contains(t, reviewGaps(in),
		"never read changed file bots/cve-remediation/internal/cveremediation/eval_export.go",
		"part of a file's section is not its whole change")
}

func TestReviewGapsLargePRsAreJudgedOnTheDiff(t *testing.T) {
	wt, dir := t.TempDir(), t.TempDir()
	diff := filepath.Join(dir, "pr.diff")
	require.NoError(t, os.WriteFile(diff, []byte("diff --git a/x b/x\n"), 0o600))
	var changed []string
	for i := range maxPerFileChecks + 1 {
		p := fmt.Sprintf("pkg/f%d.go", i)
		require.NoError(t, os.MkdirAll(filepath.Join(wt, "pkg"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(wt, p), []byte("x\n"), 0o600))
		changed = append(changed, p)
	}
	in := coverageInput{
		Pass: PassIncremental, Worktree: wt, Diff: diff, Changed: changed,
		Reads: []string{diff, filepath.Join(wt, "pkg/f0_test.go")}, Searches: 1,
	}
	assert.Empty(t, reviewGaps(in), "41 changed code files need not each be opened")

	in.Changed = changed[:maxPerFileChecks]
	assert.Len(t, reviewGaps(in), maxGaps+1, "40 still must be, capped at maxGaps plus a count")
}

func TestCovers(t *testing.T) {
	want := LineRange{From: 10, To: 20}
	assert.True(t, covers([]LineRange{{1, 30}}, want))
	assert.True(t, covers([]LineRange{{15, 20}, {10, 14}}, want), "adjacent reads join")
	assert.False(t, covers([]LineRange{{10, 14}, {16, 20}}, want), "line 15 was skipped")
	assert.False(t, covers([]LineRange{{11, 20}}, want))
	assert.False(t, covers(nil, want))
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}
