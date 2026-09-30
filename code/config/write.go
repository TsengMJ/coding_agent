package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

var (
	tableHeaderRe   = regexp.MustCompile(`^\s*\[\s*([A-Za-z0-9_.-]+)\s*\]`)
	modelsDefaultRe = regexp.MustCompile(`^(\s*default\s*=\s*)("(?:[^"\\]|\\.)*"|'[^']*')(.*)$`)
	dottedDefaultRe = regexp.MustCompile(`^(\s*models\.default\s*=\s*)("(?:[^"\\]|\\.)*"|'[^']*')(.*)$`)
)

// SetDefaultModel sets models.default in the config.toml at path, creating
// the file if needed. Only that one line changes, so comments and other keys
// survive. The file is left untouched if it does not parse, before or after.
func SetDefaultModel(path, spec string) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var probe map[string]any
	if _, err := toml.Decode(string(old), &probe); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	updated := setModelsDefault(string(old), strconv.Quote(spec))
	var check fileConfig
	if _, err := toml.Decode(updated, &check); err != nil || check.Models.Default != spec {
		return fmt.Errorf("config %s: could not set models.default safely (%v); edit it by hand", path, err)
	}
	return writeFileAtomic(path, []byte(updated))
}

func setModelsDefault(text, quoted string) string {
	lines := strings.Split(text, "\n")
	table, modelsHeader := "", -1
	for i, line := range lines {
		if m := tableHeaderRe.FindStringSubmatch(line); m != nil {
			table = m[1]
			if table == "models" {
				modelsHeader = i
			}
			continue
		}
		re := dottedDefaultRe
		if table == "models" {
			re = modelsDefaultRe
		} else if table != "" {
			continue
		}
		if m := re.FindStringSubmatch(line); m != nil {
			lines[i] = m[1] + quoted + m[3]
			return strings.Join(lines, "\n")
		}
	}
	entry := "default = " + quoted
	if modelsHeader >= 0 {
		lines = append(lines[:modelsHeader+1], append([]string{entry}, lines[modelsHeader+1:]...)...)
		return strings.Join(lines, "\n")
	}
	text = strings.TrimRight(text, "\n")
	if text != "" {
		text += "\n\n"
	}
	return text + "[models]\n" + entry + "\n"
}

func writeFileAtomic(path string, data []byte) error {
	mode := fs.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
