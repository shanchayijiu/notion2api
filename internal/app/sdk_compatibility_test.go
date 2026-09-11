package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sdkSmokeScript = `
import os
import json
from openai import OpenAI

base = os.environ["SDK_BASE_URL"]
client = OpenAI(base_url=base, api_key=os.environ["SDK_API_KEY"])

models = client.models.list()
assert len(models.data) >= 1, "no models returned"
model = models.data[0].id
print("MODELS_OK", model)

r = client.chat.completions.create(model=model, messages=[{"role": "user", "content": "ping"}])
assert r.object == "chat.completion", r.object
assert r.choices and r.choices[0].message.content, "empty non-stream content"
assert r.choices[0].finish_reason in ("stop", "length"), r.choices[0].finish_reason
assert r.usage and r.usage.total_tokens >= 0, "missing usage"
print("NONSTREAM_OK", r.choices[0].message.content.strip()[:32])

chunks = []
finish = None
for ev in client.chat.completions.create(model=model, messages=[{"role": "user", "content": "ping"}], stream=True):
    if not ev.choices:
        continue
    delta = ev.choices[0].delta
    if delta and delta.content:
        chunks.append(delta.content)
    if ev.choices[0].finish_reason:
        finish = ev.choices[0].finish_reason
streamed = "".join(chunks)
assert streamed, "empty streamed content"
assert finish == "stop", finish
print("STREAM_OK", finish, streamed.strip()[:32])

tools = [{
    "type": "function",
    "function": {
        "name": "read_file",
        "description": "read a file",
        "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]},
    },
}]
r = client.chat.completions.create(
    model=model,
    messages=[{"role": "user", "content": "read x"}],
    tools=tools,
)
assert r.choices[0].finish_reason == "tool_calls", r.choices[0].finish_reason
msg = r.choices[0].message
assert msg.tool_calls and msg.tool_calls[0].function.name == "read_file", msg
args = json.loads(msg.tool_calls[0].function.arguments)
assert "path" in args, args
print("TOOLS_OK", msg.tool_calls[0].id, msg.tool_calls[0].function.name)

print("ALL_OK")
`

func findPythonInterpreter() string {
	candidates := []string{"python", "python3", "py"}
	if runtime.GOOS == "windows" {
		candidates = []string{"python", "py", "python3"}
	}
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func TestOpenAIPythonSDKCompatibility(t *testing.T) {
	python := findPythonInterpreter()
	if python == "" {
		t.Skip("python interpreter not available")
	}
	if out, err := exec.Command(python, "-c", "import openai").CombinedOutput(); err != nil {
		t.Skipf("openai python sdk not installed: %v %s", err, out)
	}

	oldFlushDelay := chatCompletionInitialFlushDelay
	chatCompletionInitialFlushDelay = 0
	t.Cleanup(func() { chatCompletionInitialFlushDelay = oldFlushDelay })

	app := newAuditHTTPApp(t)
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		if len(request.ToolsRaw) > 0 {
			return InferenceResult{Text: `{"name":"read_file","arguments":{"path":"~/x.txt"}}`, Prompt: request.Prompt}, nil
		}
		return InferenceResult{Text: "pong", Prompt: request.Prompt}, nil
	}
	app.runPromptStreamSinkOverride = func(_ *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		if err := sink.EmitText("pong"); err != nil {
			return InferenceResult{}, err
		}
		return InferenceResult{Text: "pong", Prompt: request.Prompt}, nil
	}

	server := httptest.NewServer(app)
	defer server.Close()

	scriptPath := filepath.Join(t.TempDir(), "sdk_smoke.py")
	if err := os.WriteFile(scriptPath, []byte(sdkSmokeScript), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	cmd := exec.Command(python, scriptPath)
	cmd.Env = append(os.Environ(), "SDK_BASE_URL="+server.URL+"/v1", "SDK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("openai sdk smoke failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ALL_OK") {
		t.Fatalf("openai sdk smoke incomplete:\n%s", out)
	}
}
