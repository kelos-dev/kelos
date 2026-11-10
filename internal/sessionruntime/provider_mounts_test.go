package sessionruntime

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseProviderMountPoints(t *testing.T) {
	data := "24 1 0:32 / / rw - overlay overlay rw\n" +
		`25 24 0:32 /team /home/team\040tools/.claude/skills rw - overlay overlay rw` + "\n" +
		`26 24 0:32 /config /home/team\011tools/line\012break\134040 rw - overlay overlay rw` + "\n"
	got, err := parseProviderMountPoints(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/", "/home/team tools/.claude/skills", "/home/team\ttools/line\nbreak\\040"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mount points = %q, want %q", got, want)
	}
	if _, err := parseProviderMountPoints("invalid mount information"); err == nil {
		t.Fatal("expected invalid mount information to fail")
	}
}

func TestPrepareProviderPathsRejectsMountsBeforeModification(t *testing.T) {
	for _, mountPath := range []string{".claude", ".claude/skills", ".claude/settings.json", ".claude.json"} {
		t.Run(mountPath, func(t *testing.T) {
			tmp, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(tmp, "home")
			configDir := filepath.Join(tmp, "state", "claude-config")
			writeProviderTestFile(t, filepath.Join(home, ".claude", "skills", "team", "SKILL.md"), "shared skill")
			writeProviderTestFile(t, filepath.Join(home, ".claude", "settings.json"), "shared settings")
			writeProviderTestFile(t, filepath.Join(home, ".claude.json"), "shared metadata")
			mount := filepath.Join(home, mountPath)
			err = prepareProviderPaths("claude-code", []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + configDir}, []string{mount})
			if err == nil || !strings.Contains(err.Error(), "volume mount \""+mount+"\"") {
				t.Fatalf("mounted path error = %v", err)
			}
			assertProviderTestFile(t, filepath.Join(home, ".claude", "skills", "team", "SKILL.md"), "shared skill")
			assertProviderTestFile(t, filepath.Join(home, ".claude", "settings.json"), "shared settings")
			assertProviderTestFile(t, filepath.Join(home, ".claude.json"), "shared metadata")
			if _, err := os.Lstat(configDir); !os.IsNotExist(err) {
				t.Fatalf("provider target was modified before mount validation: %v", err)
			}
		})
	}
}

func TestPrepareProviderPathsAllowsAncestorAndSiblingMounts(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	configDir := filepath.Join(tmp, "state", "codex-home")
	writeProviderTestFile(t, filepath.Join(home, ".codex", "AGENTS.md"), "image instructions")
	if err := prepareProviderPaths("codex", []string{"HOME=" + home, "CODEX_HOME=" + configDir}, []string{home, filepath.Join(home, ".codex-other")}); err != nil {
		t.Fatal(err)
	}
	assertProviderTestFile(t, filepath.Join(configDir, "AGENTS.md"), "image instructions")
	if link, err := os.Readlink(filepath.Join(home, ".codex")); err != nil || link != configDir {
		t.Fatalf("provider link = %q, %v, want %q", link, err, configDir)
	}
}

func TestPrepareProviderPathsChecksMountsThroughHomeSymlinks(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	alias := filepath.Join(tmp, "alias")
	writeProviderTestFile(t, filepath.Join(home, ".codex", "config.toml"), "mounted config")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(tmp, "state", "codex-home")
	err = prepareProviderPaths("codex", []string{"HOME=" + alias, "CODEX_HOME=" + configDir}, []string{filepath.Join(home, ".codex", "config.toml")})
	if err == nil || !strings.Contains(err.Error(), "volume mount") {
		t.Fatalf("mounted path through home symlink error = %v", err)
	}
	assertProviderTestFile(t, filepath.Join(home, ".codex", "config.toml"), "mounted config")
}
