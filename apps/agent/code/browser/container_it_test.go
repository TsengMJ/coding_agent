package browser

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/code/mcp"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// TestContainerBrowser runs ContainerPreset against a real container:
// QUILL_DOCKER_IT=1, and QUILL_BROWSER_IT_IMAGE names an image with npx and
// Chromium (default quill-workspace:latest from deploy/workspace.Dockerfile).
func TestContainerBrowser(t *testing.T) {
	if os.Getenv("QUILL_DOCKER_IT") != "1" {
		t.Skip("set QUILL_DOCKER_IT=1 to run against a real docker daemon")
	}
	image := os.Getenv("QUILL_BROWSER_IT_IMAGE")
	if image == "" {
		image = "quill-workspace:latest"
	}
	name := "quill-browser-it-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")
	if out, err := exec.Command("docker", "run", "-d", "--rm", "--init", "--name", name, "--entrypoint", "sleep", image, "infinity").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s, err := mcp.Connect(ctx, []mcp.ServerConfig{ContainerPreset("docker", name)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tools, err := s.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]tool.Tool{}
	for _, tl := range tools {
		byName[strings.TrimPrefix(tl.Name(), "mcp__playwright__")] = tl
	}
	call := func(name string, args map[string]any) *tool.Result {
		t.Helper()
		tl, ok := byName[name]
		if !ok {
			t.Fatalf("no %s tool", name)
		}
		res, err := tl.Invoke(ctx, &tool.Runtime{}, args)
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		return res
	}
	page := `data:text/html,<button style="position:fixed;left:0;top:0;width:200px;height:100px" onclick="document.title='clicked'">go</button>`
	call("browser_navigate", map[string]any{"url": page})
	shot := call(ScreenshotTool, map[string]any{"type": "jpeg"})
	if !hasImage(shot) {
		t.Fatalf("screenshot has no image: %+v", shot.Content)
	}
	call(ClickTool, map[string]any{"element": "button", "x": 50, "y": 50})
	if snap := call("browser_snapshot", nil); !strings.Contains(textOf(snap), "clicked") {
		t.Fatalf("the click did not reach the page:\n%s", textOf(snap))
	}
}

func hasImage(res *tool.Result) bool {
	for _, b := range res.Content {
		if b.Type == llm.BlockImage && b.Base64 != "" {
			return true
		}
	}
	return false
}

func textOf(res *tool.Result) string {
	var parts []string
	for _, b := range res.Content {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n")
}
