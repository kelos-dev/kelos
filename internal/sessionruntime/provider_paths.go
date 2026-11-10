package sessionruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func prepareProviderPaths(agentType string, environment, mountPoints []string) error {
	home := environmentValue(environment, "HOME")
	if !filepath.IsAbs(home) {
		return fmt.Errorf("HOME must be an absolute path")
	}
	type providerPath struct {
		relative  string
		target    string
		directory bool
	}
	var paths []providerPath
	switch agentType {
	case "claude-code":
		configDir := environmentValue(environment, "CLAUDE_CONFIG_DIR")
		if !filepath.IsAbs(configDir) {
			return fmt.Errorf("CLAUDE_CONFIG_DIR must be an absolute path")
		}
		paths = []providerPath{
			{".claude", configDir, true},
			{".claude.json", filepath.Join(configDir, ".claude.json"), false},
		}
	case "codex":
		paths = []providerPath{{".codex", environmentValue(environment, "CODEX_HOME"), true}}
	case "opencode":
		dataHome := environmentValue(environment, "XDG_DATA_HOME")
		if !filepath.IsAbs(dataHome) {
			return fmt.Errorf("XDG_DATA_HOME must be an absolute path")
		}
		paths = []providerPath{
			{filepath.Join(".config", "opencode"), environmentValue(environment, "OPENCODE_CONFIG_DIR"), true},
			{filepath.Join(".local", "share", "opencode"), filepath.Join(dataHome, "opencode"), true},
		}
	default:
		return fmt.Errorf("unsupported Session agent type %q", agentType)
	}
	for _, path := range paths {
		if !filepath.IsAbs(path.target) {
			return fmt.Errorf("provider path %q must have an absolute target", path.relative)
		}
		if err := rejectProviderMounts(filepath.Join(home, path.relative), mountPoints); err != nil {
			return err
		}
	}
	for _, path := range paths {
		if err := linkProviderPath(filepath.Join(home, path.relative), path.target, path.directory); err != nil {
			return fmt.Errorf("preparing Session provider path %q: %w", path.relative, err)
		}
	}
	return nil
}

func linkProviderPath(path, target string, directory bool) error {
	if filepath.Clean(path) == filepath.Clean(target) {
		return nil
	}
	if sourceInfo, err := os.Stat(path); err == nil {
		if targetInfo, err := os.Lstat(target); err == nil && os.SameFile(sourceInfo, targetInfo) {
			return nil
		}
	}
	if link, err := os.Readlink(path); err == nil {
		resolved := link
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(path), resolved)
		}
		if filepath.Clean(resolved) == filepath.Clean(target) {
			return nil
		}
		return fmt.Errorf("path already links to %q instead of %q", link, target)
	}
	if directory {
		if err := os.MkdirAll(target, 0700); err != nil {
			return err
		}
	} else if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.IsDir() != directory {
			return fmt.Errorf("path has an unexpected file type")
		}
		if err := seedProviderPath(path, target); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("cannot replace %q with a link; ensure it is writable and has no volume mounts at or below it: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.Symlink(target, path)
}

// Atomic file replacement in setup can replace the link instead of its target.
func finishProviderSetup(agentType string, environment []string) error {
	if agentType != "claude-code" {
		return nil
	}
	path := filepath.Join(environmentValue(environment, "HOME"), ".claude.json")
	target := filepath.Join(environmentValue(environment, "CLAUDE_CONFIG_DIR"), ".claude.json")
	if path == target {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		if targetInfo, err := os.Lstat(target); err == nil && os.SameFile(info, targetInfo) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		temporary, err := os.CreateTemp(filepath.Dir(target), ".claude-config-*")
		if err != nil {
			return err
		}
		defer os.Remove(temporary.Name())
		defer temporary.Close()
		if err := temporary.Chmod(info.Mode().Perm()); err != nil {
			return err
		}
		if _, err := temporary.Write(data); err != nil {
			return err
		}
		if err := temporary.Close(); err != nil {
			return err
		}
		if err := os.Rename(temporary.Name(), target); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return linkProviderPath(path, target, false)
}

// Seed missing files from the image without replacing retained Session state.
func seedProviderPath(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	existing, err := os.Lstat(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if info.Mode().Type() != existing.Mode().Type() {
			return fmt.Errorf("retained path %q has a different file type than %q", target, source)
		}
		if !info.IsDir() {
			return nil
		}
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := seedProviderPath(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(link, target)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, info.Mode().Perm())
}
