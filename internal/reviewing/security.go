package reviewing

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// skillRoots are where repositories keep agent skills.
var skillRoots = []string{"agents/skills", ".claude/skills"}

const (
	// maxSecurityRefs and maxSecurityRefBytes bound what a guide may put in
	// a review session.
	maxSecurityRefs     = 30
	maxSecurityRefBytes = 64 << 10
)

// refNameRe limits the reference file names written into a session.
var refNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.md$`)

// SecurityGuide is a repository's security review skill, read at the base
// commit so a PR cannot weaken the guide it is reviewed against.
type SecurityGuide struct {
	// Dir is the skill's directory in the repository.
	Dir string
	// Skill is the content of its SKILL.md.
	Skill string
	// References are the session paths of its reference files.
	References []string
}

// LoadSecurityGuide finds a security review skill in repo at baseOID and
// writes its reference files into dir/security for the reviewer. It returns
// false when the repository has none.
func LoadSecurityGuide(
	ctx context.Context,
	repo LocalRepo,
	baseOID, dir string,
) (SecurityGuide, bool) {
	if !ValidSHA(baseOID) {
		return SecurityGuide{}, false
	}
	out, err := gitCmd(ctx, repo.GitDir,
		append([]string{"ls-tree", "-r", "--name-only", baseOID, "--"}, skillRoots...)...)
	if err != nil {
		return SecurityGuide{}, false
	}
	files := strings.FieldsFunc(string(out), func(r rune) bool { return r == '\n' })
	skillDir, ok := pickSecuritySkill(files)
	if !ok {
		return SecurityGuide{}, false
	}
	skill, err := ShowFile(ctx, repo, baseOID, path.Join(skillDir, "SKILL.md"))
	if err != nil {
		return SecurityGuide{}, false
	}
	g := SecurityGuide{Dir: skillDir, Skill: string(skill)}

	refsDir := filepath.Join(dir, "security")
	refPrefix := path.Join(skillDir, "references") + "/"
	for _, f := range files {
		name, isRef := strings.CutPrefix(f, refPrefix)
		if !isRef || !refNameRe.MatchString(name) || len(g.References) >= maxSecurityRefs {
			continue
		}
		content, err := ShowFile(ctx, repo, baseOID, f)
		if err != nil || len(content) > maxSecurityRefBytes {
			continue
		}
		if err := os.MkdirAll(refsDir, 0o700); err != nil {
			break
		}
		p := filepath.Join(refsDir, name)
		if os.WriteFile(p, content, 0o600) == nil {
			g.References = append(g.References, p)
		}
	}
	return g, true
}

// pickSecuritySkill chooses the security review skill among repository
// files: a directory directly under a skill root whose name contains
// "security" and that holds a SKILL.md. A name ending in "security-review"
// wins over other security skills, which are usually component-specific.
func pickSecuritySkill(files []string) (string, bool) {
	var dirs []string
	for _, f := range files {
		d, base := path.Split(f)
		d = strings.TrimSuffix(d, "/")
		if base != "SKILL.md" || !slices.Contains(skillRoots, path.Dir(d)) ||
			!strings.Contains(path.Base(d), "security") {
			continue
		}
		dirs = append(dirs, d)
	}
	if len(dirs) == 0 {
		return "", false
	}
	slices.SortStableFunc(dirs, func(a, b string) int {
		ar, br := strings.HasSuffix(a, "security-review"), strings.HasSuffix(b, "security-review")
		switch {
		case ar && !br:
			return -1
		case br && !ar:
			return 1
		default:
			return strings.Compare(a, b)
		}
	})
	return dirs[0], true
}
