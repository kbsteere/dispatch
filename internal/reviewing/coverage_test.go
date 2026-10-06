package reviewing

import (
	"os"
	"path/filepath"
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
			Examined: Examined{
				Docs: []string{
					"bots/kev/README.md",
					"/home/u/Dev/cg/mono/main/docs/version-pipeline/overview.svg",
				},
				Code:  []string{"bots/kev/sync.go"},
				Tests: []string{"bots/kev/sync_test.go"},
			},
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
		{
			"complete; lockfile and deleted file need no read",
			in(nil),
			nil,
		},
		{
			"changed file never read",
			in(
				func(c *coverageInput) { c.Reads = without(c.Reads, filepath.Join(wt, "bots/kev/sync.go")) },
			),
			[]string{
				"listed bots/kev/sync.go as examined but never opened it",
				"never read changed file bots/kev/sync.go",
			},
		},
		{
			"no tests read for changed code",
			in(func(c *coverageInput) {
				c.Reads = without(c.Reads, filepath.Join(wt, "bots/kev/sync_test.go"))
				c.Examined.Tests = nil
			}),
			[]string{"read no existing tests for the changed code"},
		},
		{
			"no docs read",
			in(func(c *coverageInput) {
				c.Reads = c.Reads[:3]
				c.Examined.Docs = nil
			}),
			[]string{"read no documentation or design docs"},
		},
		{
			"never searched for usages",
			in(func(c *coverageInput) { c.Searches = 0 }),
			[]string{"never searched for usages of the changed code"},
		},
		{
			"claimed a read that never happened",
			in(func(c *coverageInput) {
				c.Examined.Usages = []string{"bots/kev/cmd/main.go"}
			}),
			[]string{"listed bots/kev/cmd/main.go as examined but never opened it"},
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
			in(func(c *coverageInput) {
				c.PROnly = true
				c.Reads, c.Searches, c.Examined = []string{diff}, 0, Examined{}
			}),
			nil,
		},
		{
			"pr-only repo still checks claimed reads",
			in(func(c *coverageInput) {
				c.PROnly = true
				c.Reads, c.Examined = []string{
					diff,
				}, Examined{
					Code: []string{"version_data/consul-k8s.yaml"},
				}
			}),
			[]string{"listed version_data/consul-k8s.yaml as examined but never opened it"},
		},
		{
			"data-only change needs no tests or searches",
			in(func(c *coverageInput) {
				c.Changed = []string{"bots/kev/README.md"}
				c.Reads = []string{diff, filepath.Join(wt, "bots/kev/README.md")}
				c.Searches, c.Examined = 0, Examined{}
			}),
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, reviewGaps(tt.in))
		})
	}
}

func TestReviewGapsAreCapped(t *testing.T) {
	ex := Examined{}
	for i := range 15 {
		ex.Code = append(ex.Code, filepath.Join("pkg", string(rune('a'+i))+".go"))
	}
	gaps := reviewGaps(
		coverageInput{
			Pass:     PassFull,
			PROnly:   true,
			Diff:     "/d",
			Reads:    []string{"/d"},
			Examined: ex,
		},
	)
	require.Len(t, gaps, maxGaps+1)
	assert.Equal(t, "and 5 more", gaps[maxGaps])
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
