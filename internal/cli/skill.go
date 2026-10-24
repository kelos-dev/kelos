package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func parseSkillFlag(value string) (kelos.SkillDefinition, error) {
	parts := strings.SplitN(value, "=", 2)
	if len(parts) == 2 && parts[0] != "" && strings.HasPrefix(parts[1], "@") {
		dir, err := expandHome(parts[1][1:])
		if err != nil {
			return kelos.SkillDefinition{}, err
		}
		info, err := os.Stat(dir)
		if err != nil {
			return kelos.SkillDefinition{}, fmt.Errorf("resolving --skill %q: %w", parts[0], err)
		}
		if info.IsDir() {
			return readSkillDirectory(parts[0], dir)
		}
	}
	name, content, err := parseNameContent(value, "skill")
	return kelos.SkillDefinition{Name: name, Content: content}, err
}

func readSkillDirectory(name, dir string) (kelos.SkillDefinition, error) {
	skill := kelos.SkillDefinition{Name: name}
	foundSkill := false
	err := filepath.WalkDir(dir, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("file %q must be a regular text file; symbolic links are not supported", filePath)
		}
		rel, err := filepath.Rel(dir, filePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.Contains(rel, "\\") {
			return fmt.Errorf("file %q must use forward slashes", filePath)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		if !utf8.Valid(content) {
			return fmt.Errorf("file %q must contain UTF-8 text", filePath)
		}
		if rel == "SKILL.md" {
			skill.Content = string(content)
			foundSkill = true
			return nil
		}
		skill.Files = append(skill.Files, kelos.SkillFile{
			Path: rel, Content: string(content), Executable: info.Mode().Perm()&0o111 != 0,
		})
		return nil
	})
	if err != nil {
		return kelos.SkillDefinition{}, fmt.Errorf("reading --skill %q directory %q: %w", name, dir, err)
	}
	if !foundSkill {
		return kelos.SkillDefinition{}, fmt.Errorf("--skill %q directory %q must contain SKILL.md", name, dir)
	}
	return skill, nil
}
