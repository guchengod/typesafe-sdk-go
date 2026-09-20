package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

var (
	testRouterMu sync.Mutex
	testRoutes   = make(map[string]http.Handler)
	nextAPIID    atomic.Int64
)

type memoryTransport struct{}

func (m *memoryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	testRouterMu.Lock()
	handler := testRoutes[req.URL.Host]
	testRouterMu.Unlock()
	if handler == nil {
		return nil, fmt.Errorf("no mock handler for host %s", req.URL.Host)
	}
	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
	}
	reqCopy := req.Clone(req.Context())
	if bodyBytes != nil {
		reqCopy.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	} else {
		reqCopy.Body = http.NoBody
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, reqCopy)
	return rec.Result(), nil
}

func init() {
	clientOptions = []typesafe.Option{typesafe.WithTransport(&memoryTransport{})}
}

// cliResult is what one CLI invocation produced.
type cliResult struct {
	code   int
	stdout string
	stderr string
}

type cliAPIServer struct {
	URL string
}

func (s *cliAPIServer) Close() {}

// cliAPI is a stub API the CLI talks to.
type cliAPI struct {
	server *cliAPIServer
	// requests records the decoded body of every request the CLI sent.
	requests []map[string]any
	// headers records the headers of every request.
	headers []http.Header
	respond func(w http.ResponseWriter, r *http.Request)
}

func newCLIAPI(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *cliAPI {
	t.Helper()
	id := nextAPIID.Add(1)
	host := fmt.Sprintf("mock-api-%d.test", id)
	server := &cliAPIServer{URL: "http://" + host}
	api := &cliAPI{
		server:  server,
		respond: respond,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			if len(body) > 0 {
				var decoded map[string]any
				_ = json.Unmarshal(body, &decoded)
				api.requests = append(api.requests, decoded)
			}
		}
		api.headers = append(api.headers, r.Header.Clone())
		api.respond(w, r)
	})

	testRouterMu.Lock()
	testRoutes[host] = handler
	testRouterMu.Unlock()

	t.Cleanup(func() {
		testRouterMu.Lock()
		delete(testRoutes, host)
		testRouterMu.Unlock()
	})
	return api
}

// runCLI invokes the CLI against the stub API.
func runCLI(t *testing.T, api *cliAPI, stdin string, args ...string) cliResult {
	t.Helper()
	full := append([]string{"--api-key", "test-key", "--base-url", api.server.URL}, args...)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), full, &stdout, &stderr, strings.NewReader(stdin))
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

const cliAnswerPayload = `{
	"model": "jev-latest",
	"usage": {"input_tokens": 12, "output_tokens": 3},
	"answers": {
		"billing": {"type": "noul", "noul": 0.98},
		"tone": {"type": "choice", "choice": "angry", "confidence": 0.9, "probabilities": {"angry": 0.9, "calm": 0.1}},
		"urgency": {"type": "score", "score": 1.7, "confidence": 0.8, "legend": {"0": "low", "1": "mid", "2": "high"}, "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}}
	}
}`

func respondJSON(payload string, status int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}
}

func TestNoCommandPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr, strings.NewReader(""))

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("stderr = %q, want the usage text", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sing"}, &stdout, &stderr, strings.NewReader(""))

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), `unknown command "sing"`) {
		t.Errorf("stderr = %q, want the unknown-command message", stderr.String())
	}
}

func TestHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != exitOK {
			t.Errorf("run(%v) exit code = %d, want 0", args, code)
		}
		if !strings.Contains(stdout.String(), "typesafe - ask TypeSafe AI questions") {
			t.Errorf("run(%v) stdout = %q, want the usage text", args, stdout.String())
		}
	}

	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != exitOK {
			t.Errorf("run(%v) exit code = %d, want 0", args, code)
		}
		if !strings.Contains(stdout.String(), "typesafe ") || !strings.Contains(stdout.String(), "go/") {
			t.Errorf("run(%v) stdout = %q, want the version and runtime", args, stdout.String())
		}
	}
}

func TestAskRequiresStateAndQuestions(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))

	result := runCLI(t, api, "", "ask", "--noul", "q=Is this spam?")
	if result.code != exitUsage {
		t.Errorf("missing --state: exit code = %d, want %d", result.code, exitUsage)
	}
	if !strings.Contains(result.stderr, "--state is required") {
		t.Errorf("missing --state: stderr = %q", result.stderr)
	}

	result = runCLI(t, api, "", "ask", "--state", "hello")
	if result.code != exitUsage {
		t.Errorf("missing questions: exit code = %d, want %d", result.code, exitUsage)
	}
	if !strings.Contains(result.stderr, "at least one question is required") {
		t.Errorf("missing questions: stderr = %q", result.stderr)
	}

	if len(api.requests) != 0 {
		t.Errorf("the CLI sent %d requests, want none for a usage error", len(api.requests))
	}
}

func TestAskSendsQuestionsAndDecodesAnswers(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))

	result := runCLI(t, api, "",
		"ask",
		"--state", `{"body":"I was charged twice."}`,
		"--model", "jev-preview",
		"--noul", "billing=Is this about billing?",
		"--choice", "tone=calm:Measured,angry:Hostile",
		"--score", "urgency=low,mid,high",
	)
	if result.code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr=%s)", result.code, result.stderr)
	}

	if len(api.requests) != 1 {
		t.Fatalf("sent %d requests, want 1", len(api.requests))
	}
	body := api.requests[0]
	if body["model"] != "jev-preview" {
		t.Errorf("model = %v, want jev-preview", body["model"])
	}
	if state, ok := body["state"].(map[string]any); !ok || state["body"] != "I was charged twice." {
		t.Errorf("state = %#v, want the decoded JSON object", body["state"])
	}

	questions, ok := body["questions"].(map[string]any)
	if !ok {
		t.Fatalf("questions = %#v, want an object", body["questions"])
	}
	billing, _ := questions["billing"].(map[string]any)
	if billing["type"] != "noul" || billing["instructions"] != "Is this about billing?" {
		t.Errorf("billing question = %#v", questions["billing"])
	}
	tone, _ := questions["tone"].(map[string]any)
	criteria, _ := tone["criteria"].(map[string]any)
	if criteria["calm"] != "Measured" || criteria["angry"] != "Hostile" {
		t.Errorf("tone criteria = %#v, want label descriptions", tone["criteria"])
	}
	urgency, _ := questions["urgency"].(map[string]any)
	levels, _ := urgency["criteria"].([]any)
	if len(levels) != 3 || levels[2] != "high" {
		t.Errorf("urgency criteria = %#v, want the ordered rubric", urgency["criteria"])
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(result.stdout), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v (%s)", err, result.stdout)
	}
	if decoded["model"] != "jev-latest" {
		t.Errorf("output model = %v", decoded["model"])
	}
	answers, _ := decoded["answers"].(map[string]any)
	if len(answers) != 3 {
		t.Errorf("output answers = %#v, want three", answers)
	}
	if _, present := decoded["request_id"]; present {
		t.Errorf("output = %s, want only the API payload", result.stdout)
	}
}

func TestAskAuthHeaderUsesTheFlag(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))

	if result := runCLI(t, api, "", "ask", "--state", "hello", "--noul", "q=Is this spam?"); result.code != exitOK {
		t.Fatalf("exit code = %d (stderr=%s)", result.code, result.stderr)
	}
	if got := api.headers[0].Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want the key from --api-key", got)
	}
}

func TestAskReadsQuestionsJSONFromFileAndStateFromStdin(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))
	dir := t.TempDir()

	questions := filepath.Join(dir, "questions.json")
	content := `{"spam":{"type":"noul","instructions":"Is this spam?"},"queue":{"type":"choice","criteria":{"billing":null,"other":null}}}`
	if err := os.WriteFile(questions, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state.txt")
	if err := os.WriteFile(state, []byte("I was charged twice."), 0o600); err != nil {
		t.Fatal(err)
	}

	result := runCLI(t, api, "", "ask", "--state", "@"+state, "--questions", "@"+questions)
	if result.code != exitOK {
		t.Fatalf("exit code = %d (stderr=%s)", result.code, result.stderr)
	}
	if got := api.requests[0]["state"]; got != "I was charged twice." {
		t.Errorf("state = %#v, want the file content as a plain string", got)
	}
	names, _ := api.requests[0]["questions"].(map[string]any)
	if len(names) != 2 {
		t.Errorf("questions = %#v, want the two from the file", names)
	}

	// --state - reads the content from stdin.
	result = runCLI(t, api, "ticket body from stdin", "ask", "--state", "-", "--noul", "q=Is this spam?")
	if result.code != exitOK {
		t.Fatalf("stdin state: exit code = %d (stderr=%s)", result.code, result.stderr)
	}
	if got := api.requests[1]["state"]; got != "ticket body from stdin" {
		t.Errorf("stdin state = %#v", got)
	}
}

func TestAskRejectsDuplicateQuestionNames(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))

	result := runCLI(t, api, "", "ask", "--state", "x",
		"--noul", "q=First?", "--noul", "q=Second?")
	if result.code != exitUsage {
		t.Errorf("exit code = %d, want %d", result.code, exitUsage)
	}
	if !strings.Contains(result.stderr, `question "q" is defined more than once`) {
		t.Errorf("stderr = %q", result.stderr)
	}
}

func TestAskRejectsMalformedSpecs(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))

	for _, testCase := range []struct {
		name  string
		args  []string
		match string
	}{
		{"noul without a value", []string{"--noul", "billing"}, "expected name=value"},
		{"noul without an instruction", []string{"--noul", "billing="}, "needs an instruction"},
		{"choice without labels", []string{"--choice", "tone="}, "at least one label"},
		{"score without levels", []string{"--score", "urgency="}, "at least one level"},
		{"unknown format", []string{"--noul", "q=x", "--format", "yaml"}, "unknown format"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			args := append([]string{"ask", "--state", "x"}, testCase.args...)
			result := runCLI(t, api, "", args...)
			if result.code != exitUsage {
				t.Errorf("exit code = %d, want %d (stderr=%s)", result.code, exitUsage, result.stderr)
			}
			if !strings.Contains(result.stderr, testCase.match) {
				t.Errorf("stderr = %q, want it to mention %q", result.stderr, testCase.match)
			}
		})
	}
}

func TestAskTextFormat(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))

	result := runCLI(t, api, "", "ask", "--state", "hello", "--noul", "q=Is this spam?", "--format", "text")
	if result.code != exitOK {
		t.Fatalf("exit code = %d (stderr=%s)", result.code, result.stderr)
	}
	for _, want := range []string{"model", "jev-latest", "usage", "billing", "0.9800", "tone", "angry", "urgency", "1.70"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("text output %q is missing %q", result.stdout, want)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result.stdout), &decoded); err == nil {
		t.Errorf("text output parsed as JSON = %q, want a human-readable summary", result.stdout)
	}
}

func TestAskAPIErrorExitCode(t *testing.T) {
	api := newCLIAPI(t, respondJSON(`{"detail":{"message":"Invalid request."}}`, http.StatusBadRequest))

	result := runCLI(t, api, "", "ask", "--state", "x", "--noul", "q=Is this spam?")
	if result.code != exitAPI {
		t.Errorf("exit code = %d, want %d", result.code, exitAPI)
	}
	if !strings.Contains(result.stderr, "Invalid request.") {
		t.Errorf("stderr = %q, want the server message", result.stderr)
	}
	if result.stdout != "" {
		t.Errorf("stdout = %q, want nothing on failure", result.stdout)
	}
}

func TestAskConnectionErrorExitCode(t *testing.T) {
	// A server that is closed immediately: the request never reaches the API.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"ask", "--api-key", "k", "--base-url", url, "--state", "x", "--noul", "q=Is this spam?", "--retries", "0"},
		&stdout, &stderr, strings.NewReader(""))

	if code != exitConnection {
		t.Errorf("exit code = %d, want %d (stderr=%s)", code, exitConnection, stderr.String())
	}
	if !strings.Contains(stderr.String(), "typesafe:") {
		t.Errorf("stderr = %q, want a prefixed message", stderr.String())
	}
}

func TestModelsJSONAndText(t *testing.T) {
	payload := `{"models":[{"name":"jev-latest","description":"General purpose","release_date":"2026-09-10"}]}`
	api := newCLIAPI(t, respondJSON(payload, http.StatusOK))

	result := runCLI(t, api, "", "models")
	if result.code != exitOK {
		t.Fatalf("exit code = %d (stderr=%s)", result.code, result.stderr)
	}
	var decoded struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v (%s)", err, result.stdout)
	}
	if len(decoded.Models) != 1 || decoded.Models[0].Name != "jev-latest" {
		t.Errorf("models = %#v", decoded.Models)
	}

	result = runCLI(t, api, "", "models", "--format", "text")
	if result.code != exitOK {
		t.Fatalf("text: exit code = %d (stderr=%s)", result.code, result.stderr)
	}
	for _, want := range []string{"NAME", "jev-latest", "General purpose", "2026-09-10"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("text output %q is missing %q", result.stdout, want)
		}
	}
}

func TestModelsAPIErrorExitCode(t *testing.T) {
	api := newCLIAPI(t, respondJSON(`{"message":"nope"}`, http.StatusUnauthorized))

	result := runCLI(t, api, "", "models")
	if result.code != exitAPI {
		t.Errorf("exit code = %d, want %d", result.code, exitAPI)
	}
	if !strings.Contains(result.stderr, "nope") {
		t.Errorf("stderr = %q, want the server message", result.stderr)
	}
}

func TestUnknownFlagsAreUsageErrors(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))

	result := runCLI(t, api, "", "ask", "--state", "x", "--noul", "q=?", "--nope")
	if result.code != exitUsage {
		t.Errorf("exit code = %d, want %d", result.code, exitUsage)
	}
}

func TestEnvConfiguration(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))
	questionFile := filepath.Join(t.TempDir(), "questions.json")
	if err := os.WriteFile(questionFile, []byte(`{"q":{"type":"noul","instructions":"?"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TYPESAFE_API_KEY", "env-key")
	t.Setenv("TYPESAFE_BASE_URL", api.server.URL)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"ask", "--state", "hello", "--questions", "@" + questionFile},
		&stdout, &stderr, strings.NewReader(""))

	if code != exitOK {
		t.Fatalf("exit code = %d (stderr=%s)", code, stderr.String())
	}
	if got := api.headers[0].Get("Authorization"); got != "Bearer env-key" {
		t.Errorf("Authorization = %q, want the key from TYPESAFE_API_KEY", got)
	}
}

func TestQuestionsPathWithoutAtIsExplained(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))
	questions := filepath.Join(t.TempDir(), "questions.json")
	if err := os.WriteFile(questions, []byte(`{"q":{"type":"noul","instructions":"?"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result := runCLI(t, api, "", "ask", "--state", "hello", "--questions", questions)
	if result.code != exitUsage {
		t.Fatalf("exit code = %d, want %d", result.code, exitUsage)
	}
	if !strings.Contains(result.stderr, "prefix it with @") {
		t.Errorf("stderr = %q, want the @ hint", result.stderr)
	}
}

func TestGlobalFlagsBeforeTheCommand(t *testing.T) {
	api := newCLIAPI(t, respondJSON(cliAnswerPayload, http.StatusOK))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"--api-key", "before-key", "--base-url", api.server.URL, "ask", "--state", "hello", "--noul", "q=?"},
		&stdout, &stderr, strings.NewReader(""))

	if code != exitOK {
		t.Fatalf("exit code = %d (stderr=%s)", code, stderr.String())
	}
	if got := api.headers[0].Get("Authorization"); got != "Bearer before-key" {
		t.Errorf("Authorization = %q, want the key given before the command", got)
	}
}

func TestCommandFlagsWinOverGlobalFlags(t *testing.T) {
	modelsPayload := `{"models":[{"name":"jev-latest","description":"General purpose","release_date":"2026-09-10"}]}`
	first := newCLIAPI(t, respondJSON(modelsPayload, http.StatusOK))
	second := newCLIAPI(t, respondJSON(modelsPayload, http.StatusOK))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"--api-key", "k", "--base-url", first.server.URL, "models", "--base-url", second.server.URL},
		&stdout, &stderr, strings.NewReader(""))

	if code != exitOK {
		t.Fatalf("exit code = %d (stderr=%s)", code, stderr.String())
	}
	if len(first.headers) != 0 {
		t.Errorf("the first server saw %d requests, want none", len(first.headers))
	}
	if len(second.headers) != 1 {
		t.Errorf("the second server saw %d requests, want 1", len(second.headers))
	}
}

func TestMissingAPIKeyIsAUsageError(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"models"}, &stdout, &stderr, strings.NewReader(""))

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "No API key") {
		t.Errorf("stderr = %q, want the missing-key message", stderr.String())
	}
}
