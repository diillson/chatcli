/*
 * ChatCLI - Persona System Tests (skills)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package persona

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// newSkillLoader builds a Loader pointed at temp global+project skills dirs.
func newSkillLoader(t *testing.T) (*Loader, string, string) {
	t.Helper()
	root := t.TempDir()
	globalSkills := filepath.Join(root, "global", "skills")
	projectDir := filepath.Join(root, "project")
	projectSkills := filepath.Join(projectDir, ".agent", "skills")
	for _, d := range []string{globalSkills, projectSkills} {
		assert.NoError(t, os.MkdirAll(d, 0o755))
	}
	l := &Loader{
		logger:     zap.NewNop(),
		agentsDir:  filepath.Join(root, "global", "agents"),
		skillsDir:  globalSkills,
		projectDir: projectDir,
	}
	return l, globalSkills, projectSkills
}

// writeSkillPackage creates a directory skill (SKILL.md + optional subskill + script).
func writeSkillPackage(t *testing.T, baseDir, dirName, name, desc, source string) string {
	t.Helper()
	pkgDir := filepath.Join(baseDir, dirName)
	assert.NoError(t, os.MkdirAll(pkgDir, 0o755))

	fm := "---\nname: " + name + "\ndescription: " + desc + "\n"
	if source != "" {
		fm += "source: " + source + "\n"
	}
	fm += "---\n# " + name + "\nBody for " + name + ".\n"
	assert.NoError(t, os.WriteFile(filepath.Join(pkgDir, "SKILL.md"), []byte(fm), 0o644))

	// Add a subskill and a script to exercise those mapping paths.
	assert.NoError(t, os.WriteFile(filepath.Join(pkgDir, "advanced.md"), []byte("# advanced\n"), 0o644))
	scriptsDir := filepath.Join(pkgDir, "scripts")
	assert.NoError(t, os.MkdirAll(scriptsDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(scriptsDir, "run.sh"), []byte("echo hi\n"), 0o755))
	return pkgDir
}

func writeSkillFile(t *testing.T, dir, filename, name, desc string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\nContent.\n"
	assert.NoError(t, os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644))
}

func TestLoadSkillFromPackage(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	writeSkillPackage(t, globalSkills, "frontend-design", "frontend-design", "Frontend skill", "local")

	skill, err := l.loadSkillFromPackage(filepath.Join(globalSkills, "frontend-design"))
	assert.NoError(t, err)
	assert.Equal(t, "frontend-design", skill.Name)
	assert.Equal(t, "Frontend skill", skill.Description)
	// Subskill mapped.
	assert.Contains(t, skill.Subskills, "advanced.md")
	// Script mapped under "scripts/run.sh".
	assert.Contains(t, skill.Scripts, filepath.Join("scripts", "run.sh"))
}

func TestLoadSkillFromPackage_MissingSkillMD(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	empty := filepath.Join(globalSkills, "empty")
	assert.NoError(t, os.MkdirAll(empty, 0o755))

	_, err := l.loadSkillFromPackage(empty)
	assert.Error(t, err)
}

func TestGetSkill_PackageExactName(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	writeSkillPackage(t, globalSkills, "clean-code", "clean-code", "Clean code", "")

	skill, err := l.GetSkill("clean-code")
	assert.NoError(t, err)
	assert.Equal(t, "clean-code", skill.Name)
}

func TestGetSkill_FileExactName(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	writeSkillFile(t, globalSkills, "tdd.md", "tdd", "Test driven dev")

	skill, err := l.GetSkill("tdd")
	assert.NoError(t, err)
	assert.Equal(t, "tdd", skill.Name)
	assert.Equal(t, "Test driven dev", skill.Description)
}

func TestGetSkill_ProjectPrecedence(t *testing.T) {
	l, globalSkills, projectSkills := newSkillLoader(t)
	writeSkillFile(t, globalSkills, "review.md", "review", "Global review")
	writeSkillFile(t, projectSkills, "review.md", "review", "Project review")

	skill, err := l.GetSkill("review")
	assert.NoError(t, err)
	assert.Equal(t, "Project review", skill.Description)
}

func TestGetSkill_EmptyName(t *testing.T) {
	l, _, _ := newSkillLoader(t)
	_, err := l.GetSkill("  ")
	assert.Error(t, err)
}

func TestGetSkill_NotFound(t *testing.T) {
	l, _, _ := newSkillLoader(t)
	_, err := l.GetSkill("missing")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "skill not found")
}

func TestFindSkillByBaseName(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	// Qualified directory: "anthropics-skills--frontend-design".
	writeSkillPackage(t, globalSkills, "anthropics-skills--frontend-design", "frontend-design", "Qualified frontend", "skills.sh")

	// GetSkill("frontend-design") should find it via base-name fallback.
	skill, err := l.GetSkill("frontend-design")
	assert.NoError(t, err)
	assert.Equal(t, "frontend-design", skill.Name)
}

func TestFindSkillByBaseName_NoMatch(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	_, err := l.findSkillByBaseName(globalSkills, "nope")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestFindSkillByBaseName_MultipleCandidatesPrefersSource(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	// Two qualified dirs share the base name "design" but different sources.
	writeSkillPackage(t, globalSkills, "vendor-a--design", "design", "Vendor A design", "vendor-a")
	writeSkillPackage(t, globalSkills, "vendor-b--design", "design", "Vendor B design", "vendor-b")

	// User prefers vendor-b.
	prefsPath := filepath.Join(filepath.Dir(globalSkills), "skill-preferences.yaml")
	assert.NoError(t, os.WriteFile(prefsPath, []byte("preferences:\n  design: vendor-b\n"), 0o644))

	skill, err := l.findSkillByBaseName(globalSkills, "design")
	assert.NoError(t, err)
	assert.Equal(t, "Vendor B design", skill.Description)
}

func TestGetSkill_PreferredRegistryVersionFirst(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	// A qualified registry dir for "review".
	writeSkillPackage(t, globalSkills, "skills.sh--review", "review", "Registry review", "skills.sh")

	prefsPath := filepath.Join(filepath.Dir(globalSkills), "skill-preferences.yaml")
	assert.NoError(t, os.WriteFile(prefsPath, []byte("preferences:\n  review: skills.sh\n"), 0o644))

	skill, err := l.GetSkill("review")
	assert.NoError(t, err)
	assert.Equal(t, "Registry review", skill.Description)
}

func TestListSkills_MergesPackagesAndFiles(t *testing.T) {
	l, globalSkills, projectSkills := newSkillLoader(t)
	writeSkillPackage(t, globalSkills, "pkg-skill", "pkg-skill", "A package skill", "")
	writeSkillFile(t, globalSkills, "file-skill.md", "file-skill", "A file skill")
	writeSkillFile(t, projectSkills, "local-skill.md", "local-skill", "Project skill")

	skills, err := l.ListSkills()
	assert.NoError(t, err)

	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	assert.True(t, names["pkg-skill"])
	assert.True(t, names["file-skill"])
	assert.True(t, names["local-skill"])
}

func TestListSkills_DedupByName(t *testing.T) {
	l, globalSkills, projectSkills := newSkillLoader(t)
	// Same frontmatter name in project and global — only one should survive.
	writeSkillFile(t, projectSkills, "shared.md", "shared", "Project shared")
	writeSkillFile(t, globalSkills, "shared.md", "shared", "Global shared")

	skills, err := l.ListSkills()
	assert.NoError(t, err)

	count := 0
	for _, s := range skills {
		if s.Name == "shared" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestExtractSourceFromFrontmatter(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	pkg := writeSkillPackage(t, globalSkills, "srcskill", "srcskill", "Has source", "skills.sh")

	got := l.extractSourceFromFrontmatter(pkg)
	assert.Equal(t, "skills.sh", got)

	// Missing SKILL.md => empty.
	assert.Equal(t, "", l.extractSourceFromFrontmatter(filepath.Join(globalSkills, "does-not-exist")))
}

func TestLoadSkillPreferences(t *testing.T) {
	l, globalSkills, _ := newSkillLoader(t)
	// preferences file lives alongside the skills dir.
	prefsPath := filepath.Join(filepath.Dir(globalSkills), "skill-preferences.yaml")
	body := "preferences:\n  frontend-design: skills.sh\n  tdd: local\n"
	assert.NoError(t, os.WriteFile(prefsPath, []byte(body), 0o644))

	prefs := l.loadSkillPreferences()
	assert.Equal(t, "skills.sh", prefs["frontend-design"])
	assert.Equal(t, "local", prefs["tdd"])
}

func TestLoadSkillPreferences_Missing(t *testing.T) {
	l, _, _ := newSkillLoader(t)
	assert.Nil(t, l.loadSkillPreferences())
}

func TestEnsureDirectoriesAndGetters(t *testing.T) {
	root := t.TempDir()
	l := &Loader{
		logger:    zap.NewNop(),
		agentsDir: filepath.Join(root, "agents"),
		skillsDir: filepath.Join(root, "skills"),
	}
	assert.NoError(t, l.EnsureDirectories())
	assert.DirExists(t, l.GetAgentsDir())
	assert.DirExists(t, l.GetSkillsDir())
}

func TestSetProjectDir(t *testing.T) {
	l := NewLoader(zap.NewNop())
	l.SetProjectDir("relative/path")
	assert.True(t, filepath.IsAbs(l.projectDir), "project dir should be absolutized")

	// Empty is a no-op.
	prev := l.projectDir
	l.SetProjectDir("")
	assert.Equal(t, prev, l.projectDir)
}
