package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DotEnvFile is the file name searched for in the project tree.
const DotEnvFile = ".env"

// LoadDotEnv sets environment variables from the nearest project .env
// (searching upward from cwd) and the global ~/.deepagents/.env. Variables
// already present in the environment are never overridden.
func LoadDotEnv(p Paths, cwd string) error {
	values, err := ResolveDotEnv(p, cwd, os.LookupEnv)
	if err != nil {
		return err
	}
	for k, v := range values {
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set %s from .env: %w", k, err)
		}
	}
	return nil
}

// ResolveDotEnv returns the variables LoadDotEnv would set. Precedence is
// environment > project .env > global .env.
func ResolveDotEnv(p Paths, cwd string, lookup func(string) (string, bool)) (map[string]string, error) {
	merged := map[string]string{}
	files := []string{p.GlobalDotEnv}
	if project, ok := FindDotEnv(cwd); ok {
		files = append(files, project)
	}
	for _, path := range files {
		if path == "" {
			continue
		}
		values, err := readDotEnvFile(path)
		if err != nil {
			return nil, err
		}
		for k, v := range values {
			merged[k] = v
		}
	}
	for k := range merged {
		if _, set := lookup(k); set {
			delete(merged, k)
		}
	}
	return merged, nil
}

// FindDotEnv returns the nearest .env file in start or any parent directory.
func FindDotEnv(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(dir, DotEnvFile)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func readDotEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	values, err := ParseDotEnv(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return values, nil
}

// ParseDotEnv parses KEY=VALUE lines. It accepts blank lines, # comments, an
// optional "export " prefix, single-quoted literals, double-quoted values
// with \n \t \" \\ escapes, and trailing " # comment" on unquoted values.
// Variable interpolation is not supported.
func ParseDotEnv(r io.Reader) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(r)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("line %d: want KEY=VALUE", lineNo)
		}
		value, err := parseDotEnvValue(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseDotEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	switch raw[0] {
	case '\'':
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated single quote")
		}
		return raw[1 : end+1], nil
	case '"':
		return parseDoubleQuoted(raw[1:])
	}
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(raw), nil
}

func parseDoubleQuoted(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			return b.String(), nil
		case c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", errors.New("unterminated double quote")
}
