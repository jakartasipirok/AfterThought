package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/model"
)

const MaxToolOutput = 16 << 10

func Definitions() []model.Tool {
	object := func(properties map[string]any, required ...string) any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	str := map[string]string{"type": "string"}
	integer := map[string]string{"type": "integer"}
	return []model.Tool{
		{Type: "function", Function: model.Function{Name: "read", Description: "Read a UTF-8 workspace file or a skill. Output is bounded; use byte offset for pagination.", Parameters: object(map[string]any{"path": str, "offset": integer}, "path")}},
		{Type: "function", Function: model.Function{Name: "bash", Description: "Run Bash inside the isolated workspace container. Use it for inspection, search, scripts and tests. No host files, model secrets or Docker socket are mounted.", Parameters: object(map[string]any{"command": str, "timeout_seconds": integer}, "command")}},
		{Type: "function", Function: model.Function{Name: "edit", Description: "Replace exactly one occurrence of old_text in an existing workspace file.", Parameters: object(map[string]any{"path": str, "old_text": str, "new_text": str}, "path", "old_text", "new_text")}},
		{Type: "function", Function: model.Function{Name: "write", Description: "Create or replace a workspace file. Skills are read-only. Content is bounded to 64 KiB.", Parameters: object(map[string]any{"path": str, "content": str}, "path", "content")}},
	}
}

type Tools struct{ Workspace, Skills, Image, Network string }
type ToolResult struct {
	Output    string `json:"output"`
	IsError   bool   `json:"is_error"`
	Truncated bool   `json:"truncated,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
}

func (t Tools) Execute(ctx context.Context, call model.Call) ToolResult {
	var out ToolResult
	var err error
	switch call.Function.Name {
	case "read":
		var a struct {
			Path   string `json:"path"`
			Offset int64  `json:"offset"`
		}
		err = graph.Decode([]byte(call.Function.Arguments), &a)
		if err == nil {
			out, err = t.read(a.Path, a.Offset)
		}
	case "write":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		err = graph.Decode([]byte(call.Function.Arguments), &a)
		if err == nil {
			err = t.write(a.Path, []byte(a.Content))
			out.Output = "File written."
		}
	case "edit":
		var a struct {
			Path string `json:"path"`
			Old  string `json:"old_text"`
			New  string `json:"new_text"`
		}
		err = graph.Decode([]byte(call.Function.Arguments), &a)
		if err == nil {
			err = t.edit(a.Path, a.Old, a.New)
			out.Output = "File edited."
		}
	case "bash":
		var a struct {
			Command string `json:"command"`
			Timeout int    `json:"timeout_seconds"`
		}
		err = graph.Decode([]byte(call.Function.Arguments), &a)
		if err == nil {
			out, err = t.bash(ctx, a.Command, a.Timeout)
		}
	default:
		err = fmt.Errorf("tool is not available")
	}
	if err != nil {
		out.IsError = true
		out.Output = err.Error()
	}
	return out
}

func cleanPath(p string) (string, error) {
	if p == "" || strings.Contains(p, "\\") || path.IsAbs(p) {
		return "", fmt.Errorf("use a relative workspace path")
	}
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") || p == "." {
		return "", fmt.Errorf("path escapes the workspace")
	}
	return p, nil
}
func (t Tools) root(p string, mutate bool) (*os.Root, string, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, "", err
	}
	dir := t.Workspace
	if p == "skills" || strings.HasPrefix(p, "skills/") {
		if mutate {
			return nil, "", fmt.Errorf("skills are read-only")
		}
		dir = t.Skills
		p = strings.TrimPrefix(p, "skills/")
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("workspace unavailable")
	}
	return r, p, nil
}

// Nonblocking open prevents an agent-created FIFO from trapping the Activity
// before the regular-file check can run. Supported hosts are macOS and Linux.
func openRegular(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot open workspace file")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("operation requires a regular file")
	}
	return f, nil
}
func (t Tools) read(p string, offset int64) (ToolResult, error) {
	var out ToolResult
	if offset < 0 {
		return out, fmt.Errorf("offset must be nonnegative")
	}
	r, p, err := t.root(p, false)
	if err != nil {
		return out, err
	}
	defer r.Close()
	f, err := openRegular(r, p)
	if err != nil {
		return out, fmt.Errorf("cannot read workspace file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return out, fmt.Errorf("read requires a regular file")
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return out, err
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxToolOutput+1))
	if err != nil {
		return out, err
	}
	if len(b) > MaxToolOutput {
		out.Truncated = true
		b = b[:MaxToolOutput]
	}
	out.Output = string(b)
	return out, nil
}
func (t Tools) write(p string, b []byte) error {
	if len(b) > 64<<10 {
		return fmt.Errorf("file content exceeds 64 KiB")
	}
	r, p, err := t.root(p, true)
	if err != nil {
		return err
	}
	defer r.Close()
	if err = r.MkdirAll(path.Dir(p), 0755); err != nil {
		return fmt.Errorf("cannot create workspace directory")
	}
	tmp := path.Join(path.Dir(p), ".staging-"+rand.Text())
	defer r.Remove(tmp)
	f, err := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("cannot create workspace file")
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = r.Rename(tmp, p); err != nil {
		return fmt.Errorf("cannot replace workspace file")
	}
	return nil
}
func (t Tools) edit(p, old, replacement string) error {
	if old == "" {
		return fmt.Errorf("old_text must not be empty")
	}
	r, name, err := t.root(p, true)
	if err != nil {
		return err
	}
	f, err := openRegular(r, name)
	if err != nil {
		r.Close()
		return fmt.Errorf("cannot read edit target")
	}
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	f.Close()
	r.Close()
	if err != nil {
		return err
	}
	if len(b) > 64<<10 {
		return fmt.Errorf("edit target exceeds 64 KiB")
	}
	if bytes.Count(b, []byte(old)) != 1 {
		return fmt.Errorf("old_text must match exactly once")
	}
	return t.write(p, bytes.Replace(b, []byte(old), []byte(replacement), 1))
}

type boundedOutput struct {
	buffer    bytes.Buffer
	Truncated bool
}

func (b *boundedOutput) String() string { return b.buffer.String() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.buffer.Len()+n > MaxToolOutput {
		b.Truncated = true
	}
	left := MaxToolOutput - b.buffer.Len()
	if left > len(p) {
		left = len(p)
	}
	if left > 0 {
		b.buffer.Write(p[:left])
	}
	return n, nil
}

func (t Tools) bash(ctx context.Context, command string, seconds int) (ToolResult, error) {
	var out ToolResult
	if command == "" || len(command) > 64<<10 {
		return out, fmt.Errorf("command must contain 1..65536 bytes")
	}
	if seconds == 0 {
		seconds = 30
	}
	if seconds < 1 || seconds > 120 {
		return out, fmt.Errorf("timeout must be 1..120 seconds")
	}
	if !graph.ValidDigest(t.Image) {
		return out, fmt.Errorf("sandbox image is not pinned")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	name := "afterthought-tool-" + strings.ToLower(rand.Text())
	network := t.Network
	if network == "" {
		network = "none"
	}
	workspace, err := filepath.Abs(t.Workspace)
	if err != nil {
		return out, err
	}
	skills, err := filepath.Abs(t.Skills)
	if err != nil {
		return out, err
	}
	args := []string{"run", "--rm", "--name", name, "--label", "afterthought.tool=true", "--label", "afterthought.deadline=" + strconv.FormatInt(time.Now().Add(time.Duration(seconds)*time.Second).Unix(), 10), "--network", network, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--cpus=1", "--memory=512m", "--pids-limit=128", "--tmpfs", "/tmp:rw,nosuid,size=64m", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--env", "HOME=/tmp", "--env", "PYTHONDONTWRITEBYTECODE=1", "--mount", "type=bind,src=" + workspace + ",dst=/workspace", "--mount", "type=bind,src=" + skills + ",dst=/workspace/skills,readonly", "--workdir", "/workspace", t.Image, "bash", "-lc", command}
	cmd := exec.CommandContext(ctx, "docker", args...)
	var logs boundedOutput
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	}()
	err = cmd.Run()
	out.Output = logs.String()
	out.Truncated = logs.Truncated
	if err != nil {
		out.IsError = true
		out.ExitCode = 1
		if e, ok := err.(*exec.ExitError); ok {
			out.ExitCode = e.ExitCode()
		}
		if ctx.Err() != nil {
			out.Output += "\nCommand timed out or was cancelled."
		}
	}
	return out, nil
}

func ResolveImage(ctx context.Context, image string) (string, error) {
	b, err := exec.CommandContext(ctx, "docker", "image", "inspect", image, "--format", "{{.Id}}").Output()
	if err != nil {
		return "", fmt.Errorf("sandbox image unavailable; docker pull the configured image first")
	}
	id := strings.TrimSpace(string(b))
	if !graph.ValidDigest(id) {
		return "", fmt.Errorf("Docker did not return an immutable image ID")
	}
	if err = exec.CommandContext(ctx, "docker", "tag", id, "afterthought-sandbox:retained-"+strings.TrimPrefix(id, "sha256:")).Run(); err != nil {
		return "", err
	}
	return id, nil
}

func Reap(ctx context.Context) {
	b, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=afterthought.tool=true").Output()
	if err != nil {
		return
	}
	for _, id := range strings.Fields(string(b)) {
		b, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "afterthought.deadline"}}`, id).Output()
		if err != nil {
			continue
		}
		deadline, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err == nil && deadline < time.Now().Unix() {
			_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
		}
	}
}

// MarshalResult is shared by the transcript and the durable tool journal.
func MarshalResult(r ToolResult) string { b, _ := json.Marshal(r); return string(b) }
