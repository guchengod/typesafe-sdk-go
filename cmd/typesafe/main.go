// Command typesafe is a command-line client for the TypeSafe AI API.
//
// It is a thin shell over the SDK in this module: same configuration, same
// retries, same typed errors, and JSON on stdout so it composes with jq and
// the rest of a pipeline.
//
//	typesafe models
//	typesafe ask --state ticket.json --noul billing="Is this about billing?"
//
// Run "typesafe help" for the full command reference.
package main

import (
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

// Exit codes, so a script can tell a bad invocation from a failed call.
const (
	exitOK         = 0
	exitUsage      = 1 // the command line or the input was wrong; nothing was sent
	exitAPI        = 2 // the API answered with an unsuccessful status
	exitConnection = 3 // the request never reached the API

	maxCLIRetries      = 20
	maxAPIKeyFileBytes = 4096
)

func main() {
	// Ctrl-C cancels the in-flight request instead of leaving it to time out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

// run dispatches a command and returns the process exit code.
//
// Connection flags are accepted before the command as well as after it, so
// both "typesafe --api-key k models" and "typesafe models --api-key k" work.
// Flags given to the command win over the ones given before it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	inherited := &connectionFlags{}
	root := flag.NewFlagSet("typesafe", flag.ContinueOnError)
	root.SetOutput(stderr)
	root.Usage = func() {}
	inherited.register(root)
	showVersion := root.Bool("version", false, "print the CLI and runtime version")
	root.BoolVar(showVersion, "v", false, "print the CLI and runtime version (shorthand)")
	switch err := root.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		usage(stdout)
		return exitOK
	case err != nil:
		usage(stderr)
		return exitUsage
	}
	rootChanged := changedFlags(root)
	inherited.applyRootChanges(rootChanged)
	if *showVersion {
		writef(stdout, "typesafe %s (%s)\n", typesafe.SDKVersion, typesafe.RuntimeString())
		return exitOK
	}
	rest := root.Args()
	if len(rest) == 0 {
		usage(stderr)
		return exitUsage
	}

	switch rest[0] {
	case "ask":
		return runAsk(ctx, rest[1:], stdout, stderr, stdin, *inherited)
	case "models":
		return runModels(ctx, rest[1:], stdout, stderr, *inherited)
	case "version", "--version", "-v":
		writef(stdout, "typesafe %s (%s)\n", typesafe.SDKVersion, typesafe.RuntimeString())
		return exitOK
	case "help", "--help", "-h":
		usage(stdout)
		return exitOK
	default:
		writef(stderr, "typesafe: unknown command %q\n\n", rest[0])
		usage(stderr)
		return exitUsage
	}
}

const usageText = `typesafe - ask TypeSafe AI questions about your content

Usage:
  typesafe <command> [flags]

Commands:
  ask       Ask named questions about a state (System One)
  models    List the models available to the account
  version   Print the CLI and runtime version
  help      Print this help

Connection flags, accepted by every command:
  --api-key string        API key (prefer $TYPESAFE_API_KEY or --api-key-file)
  --api-key-file string   Read API key from a file
  --base-url string       API root (default $TYPESAFE_BASE_URL)
  --allow-insecure-http   Allow cleartext http:// base URLs on non-loopback hosts
  --timeout duration      Timeout for one attempt (default 10s)
  --retries int           Retries after the first attempt, 0..20 (default 2)
  --verbose               Log requests and responses to stderr

ask flags:
  --state value           Content to ask about: JSON, a plain string, @file, or - for stdin
  --state-raw value       Literal content to ask about (never interprets @file or -)
  --state-file path       Read state content from a regular file (or - for stdin)
  --model string          Model to use (default $TYPESAFE_DEFAULT_MODEL)
  --questions value       Named questions as JSON: inline, @file, or -
  --questions-file path   Read named questions JSON from a regular file (or - for stdin)
  --noul value            Repeatable: name=instruction
  --choice value          Repeatable: name=label[:description],label[:description],...
  --score value           Repeatable: name=level,level,level
  --box value             Repeatable: name=instruction
  --long-context          Enable long context (262k)
  --images                Enable danger zone images
  --format string         Output format: json or text (default json)

models flags:
  --format string      Output format: json or text (default json)

Examples:
  typesafe models
  typesafe ask --state '{"body":"I was charged twice."}' --noul billing="Is this about billing?"
  typesafe ask --state @ticket.json --choice queue=billing:Charges,technical,other
  typesafe ask --state - --questions questions.json < ticket.txt
  cat ticket.txt | typesafe ask --state - --noul spam="Is this spam?" --format text

Exit codes:
  0  success
  1  usage or input error; nothing was sent
  2  the API returned an unsuccessful status
  3  the request never reached the API
`

func usage(w io.Writer) {
	writef(w, "%s", usageText)
}

// writef writes a formatted line, discarding the error: for a command-line tool there is
// nothing useful left to do when stdout or stderr cannot be written to, and the callers that
// matter (the tabwriter paths) report the error from Flush instead.
func writef(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// connectionFlags are the flags every command shares.
type connectionFlags struct {
	apiKey            string
	apiKeyFile        string
	conflictKeyFlags  bool
	baseURL           string
	allowInsecureHTTP bool
	timeout           time.Duration
	retries           int
	retriesSet        bool
	verbose           bool
}

func (c *connectionFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.apiKey, "api-key", "", "API key (prefer $TYPESAFE_API_KEY or --api-key-file)")
	fs.StringVar(&c.apiKeyFile, "api-key-file", "", "read API key from a file")
	fs.StringVar(&c.baseURL, "base-url", "", "API root (default $TYPESAFE_BASE_URL)")
	fs.BoolVar(&c.allowInsecureHTTP, "allow-insecure-http", false, "allow non-loopback http:// base URLs")
	fs.DurationVar(&c.timeout, "timeout", 0, "timeout for one attempt (default 10s)")
	fs.IntVar(&c.retries, "retries", -1, "retries after the first attempt (default 2)")
	fs.BoolVar(&c.verbose, "verbose", false, "log requests and responses to stderr")
}

func (c *connectionFlags) applyRootChanges(changed map[string]bool) {
	if changed["api-key"] && changed["api-key-file"] {
		c.conflictKeyFlags = true
	}
	if changed["retries"] {
		c.retriesSet = true
	}
}

// overlay copies the connection flags the command actually set, so a flag given
// to the command wins over the same flag given before it.
func (c *connectionFlags) overlay(set *connectionFlags, changed map[string]bool) {
	if changed["api-key"] && changed["api-key-file"] {
		c.conflictKeyFlags = true
	}
	if changed["api-key"] {
		c.apiKey = set.apiKey
		if !changed["api-key-file"] {
			c.apiKeyFile = ""
			c.conflictKeyFlags = false
		}
	}
	if changed["api-key-file"] {
		c.apiKeyFile = set.apiKeyFile
		if !changed["api-key"] {
			c.apiKey = ""
			c.conflictKeyFlags = false
		}
	}
	if changed["base-url"] {
		c.baseURL = set.baseURL
	}
	if changed["allow-insecure-http"] {
		c.allowInsecureHTTP = set.allowInsecureHTTP
	}
	if changed["timeout"] {
		c.timeout = set.timeout
	}
	if changed["retries"] {
		c.retries = set.retries
		c.retriesSet = true
	}
	if changed["verbose"] {
		c.verbose = set.verbose
	}
}

// changedFlags reports which flags the user actually passed. Registering a flag
// writes its default into the bound variable, so the values a command inherits
// are restored from this set afterwards.
func changedFlags(fs *flag.FlagSet) map[string]bool {
	changed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { changed[f.Name] = true })
	return changed
}

// clientOptions allows injecting client options (such as mock transports for tests).
var clientOptions []typesafe.Option

// options turns the flags into SDK client options.
func (c *connectionFlags) options(stderr io.Writer) ([]typesafe.Option, error) {
	if c.conflictKeyFlags {
		return nil, errors.New("--api-key and --api-key-file are mutually exclusive")
	}
	if c.retriesSet && (c.retries < 0 || c.retries > maxCLIRetries) {
		return nil, fmt.Errorf("--retries must be between 0 and %d", maxCLIRetries)
	}
	var options []typesafe.Option
	if c.apiKeyFile != "" {
		rawKey, err := readRegularFileBounded(c.apiKeyFile, maxAPIKeyFileBytes)
		if err != nil {
			return nil, fmt.Errorf("reading --api-key-file: %w", err)
		}
		trimmedKey := strings.TrimSpace(rawKey)
		if trimmedKey == "" {
			return nil, errors.New("--api-key-file is empty")
		}
		options = append(options, typesafe.WithAPIKey(trimmedKey))
	} else if c.apiKey != "" {
		options = append(options, typesafe.WithAPIKey(c.apiKey))
	}
	if c.baseURL != "" {
		options = append(options, typesafe.WithBaseURL(c.baseURL))
	}
	if c.allowInsecureHTTP {
		options = append(options, typesafe.WithAllowInsecureHTTP(true))
	}
	if c.timeout > 0 {
		options = append(options, typesafe.WithTimeout(c.timeout))
	}
	if c.retriesSet {
		policy := typesafe.DefaultRetryPolicy()
		policy.MaxRetries = c.retries
		options = append(options, typesafe.WithRetryPolicy(policy))
	}
	if c.verbose {
		options = append(options, typesafe.WithLogger(slogDebug(stderr)))
	}
	if len(clientOptions) > 0 {
		options = append(options, clientOptions...)
	}
	return options, nil
}

// newClient builds a client from the connection flags.
func (c *connectionFlags) newClient(stderr io.Writer) (*typesafe.Client, error) {
	opts, err := c.options(stderr)
	if err != nil {
		return nil, err
	}
	return typesafe.NewClient(opts...)
}

// slogDebug returns a logger that writes debug records to stderr, which is what
// --verbose turns on.
func slogDebug(stderr io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// stringList collects a repeatable string flag.
type stringList []string

func (s *stringList) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func runModels(ctx context.Context, args []string, stdout, stderr io.Writer, inherited connectionFlags) int {
	var own connectionFlags
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	own.register(fs)
	format := fs.String("format", "json", "output format: json or text")

	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		usage(stdout)
		return exitOK
	case err != nil:
		usage(stderr)
		return exitUsage
	}
	connection := inherited
	connection.overlay(&own, changedFlags(fs))
	if fs.NArg() > 0 {
		return failf(stderr, exitUsage, "models takes no arguments")
	}

	client, err := connection.newClient(stderr)
	if err != nil {
		return fail(stderr, exitUsage, err)
	}
	defer func() { _ = client.Close() }()

	response, err := client.Models.List(ctx)
	if err != nil {
		return fail(stderr, codeFor(err), err)
	}

	if *format == "text" {
		writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		writef(writer, "NAME\tRELEASE\tDESCRIPTION\n")
		for _, model := range response.Models {
			writef(writer, "%s\t%s\t%s\n",
				sanitizeTerminalText(model.Name),
				sanitizeTerminalText(model.ReleaseDate),
				sanitizeTerminalText(model.Description),
			)
		}
		if err := writer.Flush(); err != nil {
			return fail(stderr, exitUsage, err)
		}
		return exitOK
	}
	if *format != "json" {
		return failf(stderr, exitUsage, "unknown format %q, want json or text", *format)
	}
	return writeJSON(stdout, stderr, response)
}

func runAsk(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader, inherited connectionFlags) int {
	var own connectionFlags
	var nouls, choices, scores, boxes stringList

	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	own.register(fs)
	state := fs.String("state", "", "content to ask about: JSON, a plain string, @file, or - for stdin")
	stateRaw := fs.String("state-raw", "", "literal content to ask about (never interprets @file or -)")
	stateFile := fs.String("state-file", "", "read state content from a regular file (or - for stdin)")
	model := fs.String("model", "", "model to use (default $TYPESAFE_DEFAULT_MODEL)")
	questions := fs.String("questions", "", "named questions as JSON: inline, @file, or -")
	questionsFile := fs.String("questions-file", "", "read named questions JSON from a regular file (or - for stdin)")
	format := fs.String("format", "json", "output format: json or text")
	longContext := fs.Bool("long-context", false, "enable long context (262k)")
	images := fs.Bool("images", false, "enable danger zone images")
	fs.Var(&nouls, "noul", "repeatable: name=instruction")
	fs.Var(&choices, "choice", "repeatable: name=label[:description],...")
	fs.Var(&scores, "score", "repeatable: name=level,level,level")
	fs.Var(&boxes, "box", "repeatable: name=instruction")

	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		usage(stdout)
		return exitOK
	case err != nil:
		usage(stderr)
		return exitUsage
	}
	askChanged := changedFlags(fs)
	connection := inherited
	connection.overlay(&own, askChanged)
	if fs.NArg() > 0 {
		return failf(stderr, exitUsage, "ask takes no positional arguments; use --state")
	}
	if *format != "json" && *format != "text" {
		return failf(stderr, exitUsage, "unknown format %q, want json or text", *format)
	}

	stateFlagsCount := 0
	for _, flagName := range []string{"state", "state-raw", "state-file"} {
		if askChanged[flagName] {
			stateFlagsCount++
		}
	}
	if stateFlagsCount > 1 {
		return failf(stderr, exitUsage, "choose only one of --state, --state-raw, or --state-file")
	}
	if askChanged["questions"] && askChanged["questions-file"] {
		return failf(stderr, exitUsage, "choose only one of --questions or --questions-file")
	}

	stdinUsed := false
	var stateValue any
	var err error
	switch {
	case askChanged["state-raw"]:
		if *stateRaw == "" {
			return failf(stderr, exitUsage, "--state-raw cannot be empty")
		}
		stateValue = *stateRaw
	case askChanged["state-file"]:
		if *stateFile == "" {
			return failf(stderr, exitUsage, "--state-file requires a file path or -")
		}
		stateValue, stdinUsed, err = loadFileOrStdin(*stateFile, stdin, stdinUsed)
	default:
		stateValue, stdinUsed, err = loadInput(*state, stdin, stdinUsed)
	}
	if err != nil {
		return fail(stderr, exitUsage, err)
	}
	if stateValue == nil {
		return failf(stderr, exitUsage, "--state is required")
	}
	parsedState := parseState(stateValue)

	var questionSource questionInputSpec
	switch {
	case askChanged["questions-file"]:
		if *questionsFile == "" {
			return failf(stderr, exitUsage, "--questions-file requires a file path or -")
		}
		questionSource = questionInputSpec{value: *questionsFile, fileOnly: true}
	case *questions != "":
		questionSource = questionInputSpec{value: *questions, fileOnly: false}
	}
	named, err := buildQuestions(questionSource, stdin, stdinUsed, nouls, choices, scores, boxes)
	if err != nil {
		return fail(stderr, exitUsage, err)
	}

	client, err := connection.newClient(stderr)
	if err != nil {
		return fail(stderr, exitUsage, err)
	}
	defer func() { _ = client.Close() }()

	var options []typesafe.RequestOption
	if *model != "" {
		options = append(options, typesafe.WithModel(*model))
	}
	if *longContext {
		options = append(options, typesafe.WithCallLongContext(true))
	}
	if *images {
		options = append(options, typesafe.WithCallImages(true))
	}

	response, err := client.SystemOne(ctx, parsedState, named, options...)
	if err != nil {
		return fail(stderr, codeFor(err), err)
	}

	if *format == "text" {
		return writeAnswers(stdout, stderr, response)
	}
	return writeJSON(stdout, stderr, response)
}

type questionInputSpec struct {
	value    string
	fileOnly bool
}

// buildQuestions assembles the question map from the JSON input and the
// convenience flags. Names must be unique across both.
func buildQuestions(source questionInputSpec, stdin io.Reader, stdinUsed bool, nouls, choices, scores, boxes stringList) (map[string]any, error) {
	named := map[string]any{}

	if source.value != "" {
		var raw any
		var err error
		allowStatHint := false
		if source.fileOnly {
			raw, _, err = loadFileOrStdin(source.value, stdin, stdinUsed)
		} else {
			allowStatHint = source.value != "-" && !strings.HasPrefix(source.value, "@")
			raw, _, err = loadInput(source.value, stdin, stdinUsed)
		}
		if err != nil {
			return nil, err
		}
		text, ok := raw.(string)
		if !ok {
			encoded, err := jsonv2.Marshal(raw, jsonv2.Deterministic(true))
			if err != nil {
				return nil, err
			}
			text = string(encoded)
		}
		var decoded map[string]any
		if err := jsonv2.Unmarshal([]byte(text), &decoded); err != nil {
			trimmed := strings.TrimSpace(text)
			if allowStatHint && len(trimmed) > 0 && len(trimmed) <= 256 && !strings.ContainsAny(trimmed, "\r\n\x00") {
				if _, statErr := os.Stat(trimmed); statErr == nil {
					return nil, fmt.Errorf("--questions %s names a file; use --questions-file %s or prefix it with @ to read it: @%s", trimmed, trimmed, trimmed)
				}
			}
			return nil, fmt.Errorf("--questions must be a JSON object of name to question: %w", err)
		}
		for name, question := range decoded {
			named[name] = question
		}
	}

	add := func(name string, question any) error {
		if _, exists := named[name]; exists {
			return fmt.Errorf("question %q is defined more than once", name)
		}
		named[name] = question
		return nil
	}

	for _, spec := range nouls {
		name, instruction, err := splitSpec(spec)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(instruction) == "" {
			return nil, fmt.Errorf("--noul %s needs an instruction, as name=instruction", name)
		}
		if err := add(name, typesafe.NewNoul(instruction)); err != nil {
			return nil, err
		}
	}

	for _, spec := range choices {
		name, labels, err := splitSpec(spec)
		if err != nil {
			return nil, err
		}
		criteria, err := parseLabels(labels)
		if err != nil {
			return nil, fmt.Errorf("--choice %s: %w", name, err)
		}
		if err := add(name, typesafe.NewChoice(criteria)); err != nil {
			return nil, err
		}
	}

	for _, spec := range scores {
		name, levels, err := splitSpec(spec)
		if err != nil {
			return nil, err
		}
		criteria, err := parseLevels(levels)
		if err != nil {
			return nil, fmt.Errorf("--score %s: %w", name, err)
		}
		if err := add(name, typesafe.NewScore(criteria)); err != nil {
			return nil, err
		}
	}

	for _, spec := range boxes {
		name, instruction, err := splitSpec(spec)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(instruction) == "" {
			return nil, fmt.Errorf("--box %s needs an instruction, as name=instruction", name)
		}
		if err := add(name, typesafe.NewBox(instruction)); err != nil {
			return nil, err
		}
	}

	if len(named) == 0 {
		return nil, errors.New("at least one question is required; use --questions, --noul, --choice, --score, or --box")
	}
	return named, nil
}

// splitSpec splits "name=value" on the first equals sign.
func splitSpec(spec string) (string, string, error) {
	name, value, found := strings.Cut(spec, "=")
	name = strings.TrimSpace(name)
	if !found || name == "" {
		return "", "", fmt.Errorf("expected name=value, got %q", spec)
	}
	return name, value, nil
}

// parseLabels turns "calm,angry:A hostile message" into choice criteria.
func parseLabels(value string) (map[string]any, error) {
	criteria := map[string]any{}
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		label, description, hasDescription := strings.Cut(entry, ":")
		label = strings.TrimSpace(label)
		if label == "" {
			return nil, fmt.Errorf("empty label in %q", value)
		}
		if hasDescription {
			criteria[label] = strings.TrimSpace(description)
			continue
		}
		criteria[label] = nil
	}
	if len(criteria) == 0 {
		return nil, errors.New("at least one label is required, as name=label,label")
	}
	return criteria, nil
}

// parseLevels turns "routine,elevated,critical" into an ordered rubric.
func parseLevels(value string) ([]any, error) {
	var levels []any
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		levels = append(levels, entry)
	}
	if len(levels) == 0 {
		return nil, errors.New("at least one level is required, as name=level,level")
	}
	return levels, nil
}

// loadInput reads a flag value that may be inline, a file reference, or stdin.
// It returns nil for an empty value.
func loadInput(value string, stdin io.Reader, stdinUsed bool) (any, bool, error) {
	switch {
	case value == "":
		return nil, stdinUsed, nil
	case value == "-":
		if stdinUsed {
			return nil, stdinUsed, errors.New("stdin (-) cannot be read by multiple flags")
		}
		content, err := readBounded(stdin, typesafe.MaxRequestBodyLimit)
		if err != nil {
			return nil, true, fmt.Errorf("reading stdin: %w", err)
		}
		return content, true, nil
	case strings.HasPrefix(value, "@"):
		path := strings.TrimPrefix(value, "@")
		content, err := readRegularFileBounded(path, typesafe.MaxRequestBodyLimit)
		if err != nil {
			return nil, stdinUsed, err
		}
		return content, stdinUsed, nil
	default:
		return value, stdinUsed, nil
	}
}

func loadFileOrStdin(path string, stdin io.Reader, stdinUsed bool) (any, bool, error) {
	if path == "-" {
		if stdinUsed {
			return nil, stdinUsed, errors.New("stdin (-) cannot be read by multiple flags")
		}
		content, err := readBounded(stdin, typesafe.MaxRequestBodyLimit)
		if err != nil {
			return nil, true, fmt.Errorf("reading stdin: %w", err)
		}
		return content, true, nil
	}
	content, err := readRegularFileBounded(path, typesafe.MaxRequestBodyLimit)
	if err != nil {
		return nil, stdinUsed, err
	}
	return content, stdinUsed, nil
}

func readBounded(r io.Reader, limit int64) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limit {
		return "", fmt.Errorf("input exceeds the %d-byte limit", limit)
	}
	return string(data), nil
}

func readRegularFileBounded(path string, limit int64) (string, error) {
	preInfo, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !preInfo.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", path)
	}
	if preInfo.Size() > limit {
		return "", fmt.Errorf("file %q (%d bytes) exceeds the %d-byte limit", path, preInfo.Size(), limit)
	}
	// #nosec G304 -- CLI file input flags explicitly load a user-designated local file.
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", path)
	}
	if info.Size() > limit {
		return "", fmt.Errorf("file %q (%d bytes) exceeds the %d-byte limit", path, info.Size(), limit)
	}
	return readBounded(file, limit)
}

// parseState decodes a state value: JSON when it parses, a plain string otherwise.
func parseState(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	var decoded any
	if err := jsonv2.Unmarshal([]byte(text), &decoded); err != nil {
		return text
	}
	return decoded
}

// writeJSON prints a response as indented JSON.
func writeJSON(stdout, stderr io.Writer, value any) int {
	encoder := jsontext.NewEncoder(stdout, jsontext.WithIndent("  "))
	if err := jsonv2.MarshalEncode(encoder, value, jsonv2.Deterministic(true)); err != nil {
		return fail(stderr, exitUsage, err)
	}
	return exitOK
}

// writeAnswers prints a human-readable summary, one line per question.
func writeAnswers(stdout, stderr io.Writer, response *typesafe.SystemOneResponse) int {
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	writef(writer, "model\t%s\n", sanitizeTerminalText(response.Model))
	writef(writer, "usage\t%s\n", response.Usage)

	for _, name := range slices.Sorted(maps.Keys(response.Answers)) {
		safeName := sanitizeTerminalText(name)
		switch answer := response.Answers[name].(type) {
		case *typesafe.NoulAnswer:
			writef(writer, "%s\tnoul %.4f\n", safeName, answer.Noul)
		case *typesafe.ChoiceAnswer:
			writef(writer, "%s\tchoice %s (confidence %.2f)\n", safeName, sanitizeTerminalText(answer.Choice), answer.Confidence)
		case *typesafe.ScoreAnswer:
			writef(writer, "%s\tscore %.2f of %d (confidence %.2f)\n",
				safeName, answer.Score, len(answer.Legend)-1, answer.Confidence)
		case *typesafe.BoxAnswer:
			if answer.Confidence != nil {
				writef(writer, "%s\tbox [%.2f, %.2f, %.2f, %.2f] (confidence %.2f)\n",
					safeName, answer.Box.Top, answer.Box.Bottom, answer.Box.Left, answer.Box.Right, *answer.Confidence)
			} else {
				writef(writer, "%s\tbox [%.2f, %.2f, %.2f, %.2f]\n",
					safeName, answer.Box.Top, answer.Box.Bottom, answer.Box.Left, answer.Box.Right)
			}
		}
	}
	if err := writer.Flush(); err != nil {
		return fail(stderr, exitUsage, err)
	}
	return exitOK
}

// codeFor maps a failure onto an exit code.
func codeFor(err error) int {
	if _, ok := typesafe.AsAPIError(err); ok {
		return exitAPI
	}
	return exitConnection
}

func fail(stderr io.Writer, code int, err error) int {
	writef(stderr, "typesafe: %s\n", sanitizeTerminalText(err.Error()))
	return code
}

func failf(stderr io.Writer, code int, format string, args ...any) int {
	writef(stderr, "typesafe: %s\n", sanitizeTerminalText(fmt.Sprintf(format, args...)))
	return code
}

func sanitizeTerminalText(s string) string {
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) {
			switch s[i+1] {
			case '[':
				i += 2
				for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
					i++
				}
				for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
					i++
				}
				if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
					i++
				}
				continue
			case ']':
				i += 2
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsControl(r) {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}
