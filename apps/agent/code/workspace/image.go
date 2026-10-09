package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultImage runs workspaces whose repository names no image; build it
// from deploy/workspace.Dockerfile.
const DefaultImage = "quill-workspace:latest"

// ProjectDockerfile is the repository's own image for its workspaces.
const ProjectDockerfile = ".quill/Dockerfile"

// imageSource says where a workspace's image comes from.
type imageSource struct {
	// Dockerfile, when set, is built from the repository root.
	Dockerfile string
	Image      string
}

// resolveImage picks the image for a cloned repository: .quill/Dockerfile,
// else the "image" of .devcontainer/devcontainer.json, else fallback.
func resolveImage(dir, fallback string) (imageSource, error) {
	if fi, err := os.Stat(filepath.Join(dir, ProjectDockerfile)); err == nil && fi.Mode().IsRegular() {
		return imageSource{Dockerfile: ProjectDockerfile}, nil
	}
	for _, name := range []string{".devcontainer/devcontainer.json", ".devcontainer.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return imageSource{}, err
		}
		img, err := devcontainerImage(raw)
		if err != nil {
			return imageSource{}, fmt.Errorf("%s: %w", name, err)
		}
		if img != "" {
			return imageSource{Image: img}, nil
		}
	}
	return imageSource{Image: fallback}, nil
}

var lineComment = regexp.MustCompile(`(?m)^\s*//.*$`)
var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

// devcontainerImage reads "image" from devcontainer.json, which allows
// whole-line comments and trailing commas (JSONC).
func devcontainerImage(raw []byte) (string, error) {
	clean := lineComment.ReplaceAll(raw, nil)
	clean = trailingComma.ReplaceAll(clean, []byte("$1"))
	var dc struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal(clean, &dc); err != nil {
		return "", err
	}
	return strings.TrimSpace(dc.Image), nil
}

// prepareImage builds the repository's Dockerfile when it has one and
// returns the image to run.
func prepareImage(ctx context.Context, d Docker, dir, id, fallback string) (string, error) {
	src, err := resolveImage(dir, fallback)
	if err != nil {
		return "", err
	}
	if src.Dockerfile == "" {
		return src.Image, nil
	}
	tag := "quill-ws-" + id + ":latest"
	if err := d.Build(ctx, dir, src.Dockerfile, tag); err != nil {
		return "", fmt.Errorf("build %s: %w", src.Dockerfile, err)
	}
	return tag, nil
}
