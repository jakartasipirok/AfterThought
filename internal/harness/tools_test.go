package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/model"
)

func call(name string, args any) model.Call {
	b, _ := json.Marshal(args)
	return model.Call{ID: "test-call", Type: "function", Function: model.FunctionCall{Name: name, Arguments: string(b)}}
}

func TestFileToolsRejectFIFOWithoutBlocking(t *testing.T) {
	workspace := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(workspace, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	tools := Tools{Workspace: workspace, Skills: t.TempDir()}
	for _, name := range []string{"read", "edit"} {
		done := make(chan ToolResult, 1)
		args := map[string]string{"path": "pipe"}
		if name == "edit" {
			args["old_text"] = "a"
			args["new_text"] = "b"
		}
		go func(name string) { done <- tools.Execute(context.Background(), call(name, args)) }(name)
		select {
		case result := <-done:
			if !result.IsError {
				t.Fatal("FIFO accepted")
			}
		case <-time.After(time.Second):
			t.Fatalf("%s blocked on FIFO", name)
		}
	}
}
func TestMinimalVisibilityAndLazySkills(t *testing.T) {
	tools := Definitions()
	names := []string{}
	for _, tool := range tools {
		names = append(names, tool.Function.Name)
	}
	if strings.Join(names, ",") != "read,bash,edit,write" {
		t.Fatalf("unexpected model tools: %v", names)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sample"), 0755); err != nil {
		t.Fatal(err)
	}
	data := "---\nname: sample\ndescription: A generic skill\n---\nBODY_SHOULD_NOT_BE_PRELOADED\n"
	if err := os.WriteFile(filepath.Join(dir, "sample", "SKILL.md"), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	skills, err := Catalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	prompt := SystemPrompt(skills)
	if !strings.Contains(prompt, "skills/sample/SKILL.md") || strings.Contains(prompt, "BODY_SHOULD_NOT_BE_PRELOADED") {
		t.Fatal("skill body leaked into initial prompt")
	}
}
func TestFileToolsRespectBoundaryAndExactEdits(t *testing.T) {
	workspace := t.TempDir()
	tools := Tools{Workspace: workspace, Skills: t.TempDir()}
	ctx := context.Background()
	if r := tools.Execute(ctx, call("write", map[string]string{"path": "nodes/test.py", "content": "value = 1\n"})); r.IsError {
		t.Fatal(r.Output)
	}
	if r := tools.Execute(ctx, call("edit", map[string]string{"path": "nodes/test.py", "old_text": "value = 1", "new_text": "value = 2"})); r.IsError {
		t.Fatal(r.Output)
	}
	if r := tools.Execute(ctx, call("read", map[string]string{"path": "nodes/test.py"})); r.IsError || r.Output != "value = 2\n" {
		t.Fatalf("wrong edit: %+v", r)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("not-visible"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../secret", "/etc/passwd", "escape"} {
		if r := tools.Execute(ctx, call("read", map[string]string{"path": p})); !r.IsError {
			t.Fatalf("escaped root: %s", p)
		}
	}
	if r := tools.Execute(ctx, call("write", map[string]string{"path": "skills/override.md", "content": "change"})); !r.IsError {
		t.Fatal("modified trusted skills")
	}
	if r := tools.Execute(ctx, call("edit", map[string]string{"path": "nodes/test.py", "old_text": "missing", "new_text": "x"})); !r.IsError {
		t.Fatal("ambiguous edit accepted")
	}
}
func TestIntegrationBashIsolationAndBounds(t *testing.T) {
	if os.Getenv("AFTERTHOUGHT_INTEGRATION") != "1" {
		t.Skip("requires Docker and configured sandbox image")
	}
	ctx := context.Background()
	image, err := ResolveImage(ctx, envTest("AFTERTHOUGHT_SANDBOX_IMAGE", "python:3.12-bookworm"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("../../.afterthought", "tool-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	skills, err := filepath.Abs("../../harness/skills")
	if err != nil {
		t.Fatal(err)
	}
	tools := Tools{Workspace: root, Skills: skills, Image: image}
	t.Setenv("AFTERTHOUGHT_TEST_SECRET", "worker-only-secret")
	r := tools.Execute(ctx, call("bash", map[string]any{"command": `test ! -e /workspace/.env && test ! -S /var/run/docker.sock && test "${AFTERTHOUGHT_TEST_SECRET-unset}" = unset && python3 -c 'print("sandbox-ok")'`}))
	if r.IsError || !strings.HasSuffix(strings.TrimSpace(r.Output), "sandbox-ok") {
		t.Fatalf("sandbox failed: %+v", r)
	}
	r = tools.Execute(ctx, call("bash", map[string]any{"command": `python3 -c 'print("x" * 20000)'`}))
	if r.IsError || !r.Truncated || len(r.Output) > MaxToolOutput {
		t.Fatalf("tool output bound failed: bytes=%d truncated=%v error=%v", len(r.Output), r.Truncated, r.IsError)
	}
	r = tools.Execute(ctx, call("bash", map[string]any{"command": "sleep 20", "timeout_seconds": 1}))
	if !r.IsError {
		t.Fatal("timeout was ignored")
	}
}
func envTest(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
