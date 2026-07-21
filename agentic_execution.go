package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	agenticCommandDefaultTimeout     = 20 * time.Minute
	agenticCommandDefaultOutputLimit = 2 * 1024 * 1024
	agenticCommandDefaultGracePeriod = 3 * time.Second
	agenticCommandAIExcerptLimit     = 20 * 1024
)

const (
	agenticExecutionStatusCompleted  = "completed"
	agenticExecutionStatusFailed     = "failed"
	agenticExecutionStatusTimedOut   = "timed_out"
	agenticExecutionStatusCancelled  = "cancelled"
	agenticExecutionStatusBlocked    = "blocked"
	agenticExecutionStatusStartError = "start_error"
	agenticExecutionStreamStdout     = "stdout"
	agenticExecutionStreamStderr     = "stderr"
)

type agenticExecutionOptions struct {
	Timeout     time.Duration
	OutputLimit int
	GracePeriod time.Duration
	OnEvent     func(agenticExecutionLiveEvent)
}

type agenticExecutionLiveEvent struct {
	Kind      string
	Stream    string
	Status    string
	Text      string
	Timestamp string
}

type agenticExecutionRequest struct {
	ProjectName string                 `json:"projectName"`
	TaskID      string                 `json:"taskId"`
	Command     workModeAgenticCommand `json:"command"`
}

type agenticCapturedOutput struct {
	Text          string `json:"text"`
	TotalBytes    int64  `json:"totalBytes"`
	RetainedBytes int64  `json:"retainedBytes"`
	OmittedBytes  int64  `json:"omittedBytes,omitempty"`
	Truncated     bool   `json:"truncated"`
}

type agenticExecutionOutputRecord struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type agenticExecutionStatusRecord struct {
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	Timestamp string `json:"timestamp"`
}

type agenticExecutionResult struct {
	Status            string                         `json:"status"`
	Command           workModeAgenticCommand         `json:"command"`
	Workspace         string                         `json:"workspace"`
	WorkingDirectory  string                         `json:"workingDirectory,omitempty"`
	ExecutablePath    string                         `json:"executablePath,omitempty"`
	StartedAt         string                         `json:"startedAt,omitempty"`
	FinishedAt        string                         `json:"finishedAt"`
	DurationMillis    int64                          `json:"durationMillis"`
	ExitCode          *int                           `json:"exitCode,omitempty"`
	Stdout            agenticCapturedOutput          `json:"stdout"`
	Stderr            agenticCapturedOutput          `json:"stderr"`
	OutputRecords     []agenticExecutionOutputRecord `json:"outputRecords,omitempty"`
	StatusRecords     []agenticExecutionStatusRecord `json:"statusRecords"`
	AIOutputExcerpt   string                         `json:"aiOutputExcerpt,omitempty"`
	AIOutputTruncated bool                           `json:"aiOutputTruncated,omitempty"`
	Error             string                         `json:"error,omitempty"`
	BlockedPath       string                         `json:"blockedPath,omitempty"`
	TimedOut          bool                           `json:"timedOut,omitempty"`
	Cancelled         bool                           `json:"cancelled,omitempty"`
}

type agenticPreparedCommand struct {
	Command          workModeAgenticCommand
	WorkspaceRoot    string
	WorkingDirectory string
	ExecutablePath   string
	Args             []string
	Environment      []string
	Configured       []terminalEnvironment
}

type agenticVisiblePathError struct {
	Path    string
	Message string
}

func (e *agenticVisiblePathError) Error() string {
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	return fmt.Sprintf("visible path %q resolves outside the staged workspace", e.Path)
}

func defaultAgenticExecutionOptions() agenticExecutionOptions {
	return agenticExecutionOptions{Timeout: agenticCommandDefaultTimeout, OutputLimit: agenticCommandDefaultOutputLimit, GracePeriod: agenticCommandDefaultGracePeriod}
}

func normalizeAgenticExecutionOptions(options agenticExecutionOptions) agenticExecutionOptions {
	defaults := defaultAgenticExecutionOptions()
	if options.Timeout <= 0 || options.Timeout > defaults.Timeout {
		options.Timeout = defaults.Timeout
	}
	if options.OutputLimit <= 0 || options.OutputLimit > defaults.OutputLimit {
		options.OutputLimit = defaults.OutputLimit
	}
	if options.GracePeriod <= 0 {
		options.GracePeriod = defaults.GracePeriod
	}
	return options
}

type agenticOutputSegment struct {
	Stream string
	Data   []byte
}

type agenticOutputCollector struct {
	mu        sync.Mutex
	headLimit int
	tailLimit int
	head      []agenticOutputSegment
	tail      []agenticOutputSegment
	headBytes int
	tailBytes int
	total     map[string]int64
}

type agenticOutputWriter struct {
	collector  *agenticOutputCollector
	stream     string
	configured []terminalEnvironment
	emit       func(agenticExecutionLiveEvent)
	pending    []byte
	overlap    int
	mu         sync.Mutex
}

func newAgenticOutputCollector(limit int) *agenticOutputCollector {
	if limit <= 0 {
		limit = agenticCommandDefaultOutputLimit
	}
	head := limit / 2
	if head == 0 {
		head = limit
	}
	return &agenticOutputCollector{headLimit: head, tailLimit: limit - head, total: map[string]int64{}}
}

func (c *agenticOutputCollector) writer(stream string) io.Writer {
	return &agenticOutputWriter{collector: c, stream: stream}
}

func agenticStreamingOverlap(configured []terminalEnvironment) int {
	maxLen := 0
	for _, item := range configured {
		if len(item.Value) > maxLen {
			maxLen = len(item.Value)
		}
	}
	if maxLen <= 1 {
		return 0
	}
	return maxLen - 1
}

func (c *agenticOutputCollector) writerWithEvents(stream string, configured []terminalEnvironment, emit func(agenticExecutionLiveEvent)) *agenticOutputWriter {
	return &agenticOutputWriter{collector: c, stream: stream, configured: append([]terminalEnvironment(nil), configured...), emit: emit, overlap: agenticStreamingOverlap(configured)}
}

func (w *agenticOutputWriter) emitBytes(data []byte) {
	if w == nil || w.emit == nil || len(data) == 0 {
		return
	}
	text := redactTerminalEnvironmentValues(string(data), w.configured)
	if text == "" {
		return
	}
	w.emit(agenticExecutionLiveEvent{Kind: "output", Stream: w.stream, Text: text, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)})
}

func (w *agenticOutputWriter) Write(p []byte) (int, error) {
	if w == nil {
		return len(p), nil
	}
	if w.collector != nil {
		w.collector.append(w.stream, p)
	}
	if w.emit == nil || len(p) == 0 {
		return len(p), nil
	}
	w.mu.Lock()
	w.pending = append(w.pending, p...)
	w.pending = []byte(redactTerminalEnvironmentValues(string(w.pending), w.configured))
	flushLen := len(w.pending) - w.overlap
	if flushLen > 0 {
		chunk := append([]byte(nil), w.pending[:flushLen]...)
		w.pending = append([]byte(nil), w.pending[flushLen:]...)
		w.mu.Unlock()
		w.emitBytes(chunk)
		return len(p), nil
	}
	w.mu.Unlock()
	return len(p), nil
}

func (w *agenticOutputWriter) flush() {
	if w == nil || w.emit == nil {
		return
	}
	w.mu.Lock()
	chunk := append([]byte(nil), w.pending...)
	w.pending = nil
	w.mu.Unlock()
	w.emitBytes(chunk)
}

func appendAgenticSegment(segments []agenticOutputSegment, stream string, p []byte) []agenticOutputSegment {
	if len(p) == 0 {
		return segments
	}
	if len(segments) > 0 && segments[len(segments)-1].Stream == stream {
		segments[len(segments)-1].Data = append(segments[len(segments)-1].Data, p...)
		return segments
	}
	return append(segments, agenticOutputSegment{Stream: stream, Data: append([]byte(nil), p...)})
}

func trimAgenticSegments(segments []agenticOutputSegment, remove int) []agenticOutputSegment {
	for remove > 0 && len(segments) > 0 {
		if len(segments[0].Data) <= remove {
			remove -= len(segments[0].Data)
			segments = segments[1:]
			continue
		}
		segments[0].Data = append([]byte(nil), segments[0].Data[remove:]...)
		remove = 0
	}
	return segments
}

func (c *agenticOutputCollector) append(stream string, p []byte) {
	if len(p) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total[stream] += int64(len(p))
	remaining := p
	if c.headBytes < c.headLimit {
		take := c.headLimit - c.headBytes
		if take > len(remaining) {
			take = len(remaining)
		}
		c.head = appendAgenticSegment(c.head, stream, remaining[:take])
		c.headBytes += take
		remaining = remaining[take:]
	}
	if len(remaining) == 0 || c.tailLimit == 0 {
		return
	}
	c.tail = appendAgenticSegment(c.tail, stream, remaining)
	c.tailBytes += len(remaining)
	if c.tailBytes > c.tailLimit {
		remove := c.tailBytes - c.tailLimit
		c.tail = trimAgenticSegments(c.tail, remove)
		c.tailBytes -= remove
	}
}

func copyAgenticSegments(in []agenticOutputSegment) []agenticOutputSegment {
	out := make([]agenticOutputSegment, 0, len(in))
	for _, segment := range in {
		out = append(out, agenticOutputSegment{Stream: segment.Stream, Data: append([]byte(nil), segment.Data...)})
	}
	return out
}

func agenticStreamBytes(segments []agenticOutputSegment) map[string][]byte {
	out := map[string][]byte{agenticExecutionStreamStdout: {}, agenticExecutionStreamStderr: {}}
	for _, segment := range segments {
		if _, ok := out[segment.Stream]; ok {
			out[segment.Stream] = append(out[segment.Stream], segment.Data...)
		}
	}
	return out
}

func (c *agenticOutputCollector) snapshot(configured []terminalEnvironment) (agenticCapturedOutput, agenticCapturedOutput, []agenticExecutionOutputRecord) {
	c.mu.Lock()
	head, tail := copyAgenticSegments(c.head), copyAgenticSegments(c.tail)
	totals := map[string]int64{}
	for key, value := range c.total {
		totals[key] = value
	}
	c.mu.Unlock()
	headStreams, tailStreams := agenticStreamBytes(head), agenticStreamBytes(tail)
	makeOutput := func(stream string) agenticCapturedOutput {
		headBytes, tailBytes := headStreams[stream], tailStreams[stream]
		retained := int64(len(headBytes) + len(tailBytes))
		total := totals[stream]
		omitted := total - retained
		if omitted < 0 {
			omitted = 0
		}
		var text string
		if omitted == 0 {
			text = redactTerminalEnvironmentValues(string(append(append([]byte(nil), headBytes...), tailBytes...)), configured)
		} else {
			text = redactTerminalEnvironmentValues(string(headBytes), configured) +
				fmt.Sprintf("\n[AgentGO omitted %d middle bytes from %s output]\n", omitted, stream) +
				redactTerminalEnvironmentValues(string(tailBytes), configured)
		}
		return agenticCapturedOutput{Text: text, TotalBytes: total, RetainedBytes: retained, OmittedBytes: omitted, Truncated: omitted > 0}
	}
	stdout, stderr := makeOutput(agenticExecutionStreamStdout), makeOutput(agenticExecutionStreamStderr)
	records := []agenticExecutionOutputRecord{}
	if stdout.Text != "" {
		records = append(records, agenticExecutionOutputRecord{Stream: agenticExecutionStreamStdout, Text: stdout.Text})
	}
	if stderr.Text != "" {
		records = append(records, agenticExecutionOutputRecord{Stream: agenticExecutionStreamStderr, Text: stderr.Text})
	}
	return stdout, stderr, records
}

func terminalEnvironmentLookup(environment []string, name string) string {
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func setTerminalEnvironmentValue(environment []string, name, value string) []string {
	out := make([]string, 0, len(environment)+1)
	found := false
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, name) {
			if !found {
				out = append(out, name+"="+value)
				found = true
			}
			continue
		}
		out = append(out, item)
	}
	if !found {
		out = append(out, name+"="+value)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, _, _ := strings.Cut(out[i], "=")
		right, _, _ := strings.Cut(out[j], "=")
		return strings.ToUpper(left) < strings.ToUpper(right)
	})
	return out
}

func buildAgenticExecutionEnvironment(base []string, configured []terminalEnvironment, taskRoot, workspaceRoot string) ([]string, error) {
	runtimeRoot, err := safeJoin(taskRoot, "runtime")
	if err != nil {
		return nil, err
	}
	tempRoot, err := safeJoin(runtimeRoot, "tmp")
	if err != nil {
		return nil, err
	}
	cacheRoot, err := safeJoin(runtimeRoot, "cache")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return nil, err
	}
	env := sanitizedTerminalEnvironment(base, configured)
	for _, item := range [][2]string{
		{"AGENTGO_WORKSPACE", workspaceRoot}, {"AGENTGO_TASK_ROOT", taskRoot},
		{"TMPDIR", tempRoot}, {"TMP", tempRoot}, {"TEMP", tempRoot},
		{"XDG_CACHE_HOME", cacheRoot}, {"GOCACHE", filepath.Join(cacheRoot, "go-build")},
	} {
		env = setTerminalEnvironmentValue(env, item[0], item[1])
	}
	return env, nil
}

func agenticPathWithin(root, target string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		rootAbs, targetAbs = strings.ToLower(rootAbs), strings.ToLower(targetAbs)
	}
	return targetAbs == rootAbs || strings.HasPrefix(targetAbs, rootAbs+string(os.PathSeparator))
}

func evalAgenticExistingPrefix(target string) (string, error) {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	current := absolute
	remaining := []string{}
	for {
		if _, statErr := os.Lstat(current); statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", resolveErr
			}
			for i := len(remaining) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, remaining[i])
			}
			return filepath.Abs(resolved)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return absolute, nil
		}
		remaining = append(remaining, filepath.Base(current))
		current = parent
	}
}

func genericAbsolutePath(value string) bool {
	clean := strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "//") {
		return true
	}
	return len(clean) >= 3 && ((clean[0] >= 'A' && clean[0] <= 'Z') || (clean[0] >= 'a' && clean[0] <= 'z')) && clean[1] == ':' && clean[2] == '/'
}

func trimAgenticPathToken(raw string) string {
	value := strings.Trim(strings.TrimSpace(raw), "\"'`()[]{}<>,;")
	if strings.HasPrefix(value, "-") {
		if _, after, ok := strings.Cut(value, "="); ok {
			value = after
		}
	}
	return strings.TrimSpace(value)
}

func agenticTokenIsURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && !strings.EqualFold(parsed.Scheme, "file")
}

func agenticTokenIsDynamic(value string) bool {
	return strings.Contains(value, "${") || strings.Contains(value, "$(") || strings.HasPrefix(value, "$") || strings.HasPrefix(value, "%") || strings.Contains(value, "%")
}

func agenticTokenLooksLikePath(value string) bool {
	if value == ".." {
		return true
	}
	if value == "" || value == "." {
		return false
	}
	if genericAbsolutePath(value) || strings.HasPrefix(value, "~") || strings.HasPrefix(strings.ToLower(value), "file:") {
		return true
	}
	return strings.Contains(value, "/") || strings.Contains(value, "\\") || strings.HasPrefix(value, ".")
}

func normalizeAgenticVisiblePath(workspaceRoot, workingDirectory, raw string) (string, error) {
	value := trimAgenticPathToken(raw)
	if value == "" || agenticTokenIsDynamic(value) || agenticTokenIsURL(value) {
		return "", nil
	}
	if strings.HasPrefix(strings.ToLower(value), "file:") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", &agenticVisiblePathError{Path: raw, Message: fmt.Sprintf("invalid visible file path %q", raw)}
		}
		value = parsed.Path
		if runtime.GOOS == "windows" && len(value) >= 3 && value[0] == '/' && value[2] == ':' {
			value = value[1:]
		}
	}
	if strings.HasPrefix(value, "~") {
		return "", &agenticVisiblePathError{Path: raw, Message: fmt.Sprintf("visible path %q uses home-directory expansion outside the staged workspace", raw)}
	}
	value = filepath.FromSlash(strings.ReplaceAll(value, "\\", "/"))
	candidate := value
	if !filepath.IsAbs(candidate) {
		if genericAbsolutePath(value) {
			return "", &agenticVisiblePathError{Path: raw}
		}
		candidate = filepath.Join(workingDirectory, candidate)
	}
	resolved, err := evalAgenticExistingPrefix(filepath.Clean(candidate))
	if err != nil {
		return "", err
	}
	if !agenticPathWithin(workspaceRoot, resolved) {
		return "", &agenticVisiblePathError{Path: raw}
	}
	return resolved, nil
}

func splitAgenticShellTokens(script string) []string {
	tokens := []string{}
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for _, r := range script {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && runtime.GOOS != "windows" && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' || strings.ContainsRune("|;&<>", r) {
			flush()
			continue
		}
		current.WriteRune(r)
	}
	flush()
	return tokens
}

func expandAgenticShellPathToken(value string, environment []string) string {
	if strings.HasPrefix(value, "${") {
		if end := strings.Index(value, "}"); end > 2 {
			if replacement := terminalEnvironmentLookup(environment, value[2:end]); replacement != "" {
				return replacement + value[end+1:]
			}
		}
	}
	if strings.HasPrefix(value, "$") && len(value) > 1 {
		end := 1
		for end < len(value) {
			b := value[end]
			if !((b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_') {
				break
			}
			end++
		}
		if end > 1 {
			if replacement := terminalEnvironmentLookup(environment, value[1:end]); replacement != "" {
				return replacement + value[end:]
			}
		}
	}
	if strings.HasPrefix(value, "%") {
		if offset := strings.Index(value[1:], "%"); offset >= 0 {
			end := offset + 1
			if replacement := terminalEnvironmentLookup(environment, value[1:end]); replacement != "" {
				return replacement + value[end+1:]
			}
		}
	}
	return value
}

func validateAgenticVisibleTokens(workspaceRoot, workingDirectory string, tokens, environment []string, expandShell bool) error {
	for _, token := range tokens {
		original := trimAgenticPathToken(token)
		candidate := original
		if expandShell {
			candidate = expandAgenticShellPathToken(candidate, environment)
		}
		if candidate == "" || agenticTokenIsURL(candidate) || agenticTokenIsDynamic(candidate) {
			continue
		}
		looks := agenticTokenLooksLikePath(candidate)
		if !looks {
			local := filepath.Join(workingDirectory, filepath.FromSlash(strings.ReplaceAll(candidate, "\\", "/")))
			if _, err := os.Lstat(local); err == nil {
				looks = true
			}
		}
		if looks {
			if _, err := normalizeAgenticVisiblePath(workspaceRoot, workingDirectory, candidate); err != nil {
				var pathErr *agenticVisiblePathError
				if errors.As(err, &pathErr) && original != "" {
					pathErr.Path = original
				}
				return err
			}
		}
	}
	return nil
}

func resolveAgenticWorkingDirectory(workspaceRoot, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("command working directory is required")
	}
	resolved, err := normalizeAgenticVisiblePath(workspaceRoot, workspaceRoot, raw)
	if err != nil {
		return "", err
	}
	if resolved == "" {
		return "", errors.New("command working directory must be a visible path inside the staged workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("command working directory is unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("command working directory is not a directory")
	}
	return resolved, nil
}

func agenticExecutableHasPath(value string) bool {
	return filepath.IsAbs(value) || genericAbsolutePath(value) || strings.Contains(value, "/") || strings.Contains(value, "\\") || strings.HasPrefix(value, ".")
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode()&0o111 != 0
}

func agenticLookPath(file string, environment []string) (string, error) {
	pathValue := terminalEnvironmentLookup(environment, "PATH")
	if pathValue == "" {
		return "", fmt.Errorf("PATH is unavailable while resolving executable %q", file)
	}
	extensions := []string{""}
	if runtime.GOOS == "windows" && filepath.Ext(file) == "" {
		extensions = nil
		for _, extension := range strings.Split(terminalEnvironmentLookup(environment, "PATHEXT"), ";") {
			extension = strings.TrimSpace(extension)
			if extension != "" {
				extensions = append(extensions, strings.ToLower(extension), strings.ToUpper(extension))
			}
		}
		if len(extensions) == 0 {
			extensions = []string{".exe", ".com", ".bat", ".cmd"}
		}
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, extension := range extensions {
			candidate := filepath.Join(dir, file+extension)
			if executableFile(candidate) {
				return filepath.Abs(candidate)
			}
		}
	}
	return "", fmt.Errorf("executable %q was not found in the sanitized PATH", file)
}

func validateAgenticCommandShape(command workModeAgenticCommand) (workModeAgenticCommand, error) {
	command = normalizeWorkModeAgenticCommand(command)
	if command.WorkingDirectory == "" || command.Purpose == "" {
		return command, errors.New("command working directory and purpose are required")
	}
	values := append([]string{command.Type, command.Executable, command.Script, command.WorkingDirectory, command.Purpose}, command.Args...)
	for _, value := range values {
		if strings.ContainsRune(value, '\x00') {
			return command, errors.New("command fields cannot contain null bytes")
		}
	}
	switch command.Type {
	case workModeAgenticCommandDirect:
		if command.Executable == "" {
			return command, errors.New("direct command executable is required")
		}
		if command.Script != "" {
			return command, errors.New("direct command script must be empty")
		}
	case workModeAgenticCommandShell:
		if command.Script == "" {
			return command, errors.New("shell command script is required")
		}
		if command.Executable != "" || len(command.Args) != 0 {
			return command, errors.New("shell command executable and args must be empty")
		}
	default:
		return command, errors.New("command type must be direct or shell")
	}
	return command, nil
}

func agenticShellCommand(script string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		shell := strings.TrimSpace(os.Getenv("COMSPEC"))
		if shell == "" {
			shell = "cmd.exe"
		}
		if !filepath.IsAbs(shell) {
			resolved, err := exec.LookPath(shell)
			if err != nil {
				return "", nil, fmt.Errorf("Windows command shell is unavailable: %w", err)
			}
			shell = resolved
		}
		return shell, []string{shell, "/D", "/S", "/C", script}, nil
	}
	for _, shell := range []string{"/bin/sh", "/usr/bin/sh"} {
		if executableFile(shell) {
			return shell, []string{shell, "-c", script}, nil
		}
	}
	return "", nil, errors.New("POSIX command shell is unavailable")
}

func agenticExecutionShellLabel() string {
	if runtime.GOOS == "windows" {
		shell := strings.TrimSpace(os.Getenv("COMSPEC"))
		if shell == "" {
			return "cmd.exe"
		}
		return filepath.Base(shell)
	}
	for _, shell := range []string{"/bin/sh", "/usr/bin/sh"} {
		if executableFile(shell) {
			return shell
		}
	}
	return "/bin/sh"
}

func agenticExecutionEnvironmentInstruction() string {
	return fmt.Sprintf("Execution environment: %s %s using %s. Generate %s-compatible commands, paths, quoting, and environment variables.", workModeOperatingSystemLabel(), strings.ToUpper(runtime.GOARCH), agenticExecutionShellLabel(), workModeOperatingSystemLabel())
}

func prepareAgenticExecution(a *App, request agenticExecutionRequest) (agenticPreparedCommand, error) {
	command, err := validateAgenticCommandShape(request.Command)
	if err != nil {
		return agenticPreparedCommand{}, err
	}
	task, taskRoot, err := a.loadAgenticWorkspaceTask(request.ProjectName, request.TaskID)
	if err != nil {
		return agenticPreparedCommand{}, err
	}
	if task.ProjectName != request.ProjectName {
		return agenticPreparedCommand{}, errors.New("agentic task does not belong to the requested project")
	}
	workspaceRoot, err := a.agenticWorkspaceFilesRoot(request.ProjectName, request.TaskID)
	if err != nil {
		return agenticPreparedCommand{}, err
	}
	if info, statErr := os.Stat(workspaceRoot); statErr != nil || !info.IsDir() {
		return agenticPreparedCommand{}, errors.New("staged agentic workspace is unavailable")
	}
	workingDirectory, err := resolveAgenticWorkingDirectory(workspaceRoot, command.WorkingDirectory)
	if err != nil {
		return agenticPreparedCommand{}, err
	}
	configuredFile, err := a.loadTerminalEnvironment(request.ProjectName)
	if err != nil {
		return agenticPreparedCommand{}, err
	}
	environment, err := buildAgenticExecutionEnvironment(os.Environ(), configuredFile.Variables, taskRoot, workspaceRoot)
	if err != nil {
		return agenticPreparedCommand{}, err
	}
	prepared := agenticPreparedCommand{
		Command: command, WorkspaceRoot: workspaceRoot, WorkingDirectory: workingDirectory,
		Environment: environment, Configured: append([]terminalEnvironment(nil), configuredFile.Variables...),
	}
	if command.Type == workModeAgenticCommandDirect {
		if agenticExecutableHasPath(command.Executable) {
			executablePath, pathErr := normalizeAgenticVisiblePath(workspaceRoot, workingDirectory, command.Executable)
			if pathErr != nil {
				return agenticPreparedCommand{}, pathErr
			}
			if !executableFile(executablePath) {
				return agenticPreparedCommand{}, fmt.Errorf("direct executable is unavailable or not executable: %s", command.Executable)
			}
			prepared.ExecutablePath = executablePath
		} else {
			prepared.ExecutablePath, err = agenticLookPath(command.Executable, environment)
			if err != nil {
				return agenticPreparedCommand{}, err
			}
		}
		if err := validateAgenticVisibleTokens(workspaceRoot, workingDirectory, command.Args, environment, false); err != nil {
			return agenticPreparedCommand{}, err
		}
		prepared.Args = append([]string{command.Executable}, command.Args...)
	} else {
		if err := validateAgenticVisibleTokens(workspaceRoot, workingDirectory, splitAgenticShellTokens(command.Script), environment, true); err != nil {
			return agenticPreparedCommand{}, err
		}
		prepared.ExecutablePath, prepared.Args, err = agenticShellCommand(command.Script)
		if err != nil {
			return agenticPreparedCommand{}, err
		}
	}
	return prepared, nil
}

func commandFromPrepared(prepared agenticPreparedCommand, collector *agenticOutputCollector, emit func(agenticExecutionLiveEvent)) (*exec.Cmd, []*agenticOutputWriter) {
	stdout := collector.writerWithEvents(agenticExecutionStreamStdout, prepared.Configured, emit)
	stderr := collector.writerWithEvents(agenticExecutionStreamStderr, prepared.Configured, emit)
	return &exec.Cmd{
		Path: prepared.ExecutablePath, Args: append([]string(nil), prepared.Args...),
		Dir: prepared.WorkingDirectory, Env: append([]string(nil), prepared.Environment...),
		Stdout: stdout, Stderr: stderr,
	}, []*agenticOutputWriter{stdout, stderr}
}

func emitAgenticExecutionEvent(emit func(agenticExecutionLiveEvent), kind, stream, status, text string) {
	if emit == nil {
		return
	}
	emit(agenticExecutionLiveEvent{Kind: kind, Stream: stream, Status: status, Text: strings.TrimSpace(text), Timestamp: time.Now().UTC().Format(time.RFC3339Nano)})
}

func truncateAgenticHeadTailText(text string, limit int) (string, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, false
	}

	omitted := len(text) - limit
	var notice string
	var payload int
	for attempt := 0; attempt < 4; attempt++ {
		notice = fmt.Sprintf("\n[AgentGO omitted %d bytes from the AI output excerpt]\n", omitted)
		payload = limit - len(notice)
		if payload < 2 {
			if len(notice) > limit {
				notice = notice[:limit]
			}
			return notice, true
		}
		nextOmitted := len(text) - payload
		if nextOmitted == omitted {
			break
		}
		omitted = nextOmitted
	}

	head, tail := payload/2, payload-payload/2
	return text[:head] + notice + text[len(text)-tail:], true
}

func agenticOutputStats(label string, output agenticCapturedOutput) string {
	return fmt.Sprintf("%s: total=%d retained=%d omitted=%d", label, output.TotalBytes, output.RetainedBytes, output.OmittedBytes)
}

func agenticImportantOutputLines(text string, limit int) string {
	if limit <= 0 || strings.TrimSpace(text) == "" {
		return ""
	}
	keywords := []string{"error", "failed", "failure", "fatal", "panic", "exception", "traceback", "denied", "not found", "undefined", "warning", "warn:", "assert", "timeout"}
	seen := map[string]bool{}
	lines := []string{}
	used := 0
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || seen[line] {
			continue
		}
		lower := strings.ToLower(line)
		important := false
		for _, keyword := range keywords {
			if strings.Contains(lower, keyword) {
				important = true
				break
			}
		}
		if !important {
			continue
		}
		candidate := line
		if used+len(candidate)+1 > limit {
			break
		}
		seen[line] = true
		lines = append(lines, candidate)
		used += len(candidate) + 1
	}
	return strings.Join(lines, "\n")
}

func appendAgenticExcerptSection(parts *[]string, title, text string, budget *int) {
	text = strings.TrimSpace(text)
	if text == "" || *budget <= len(title)+2 {
		return
	}
	available := *budget - len(title) - 2
	bounded, _ := truncateAgenticHeadTailText(text, available)
	section := title + "\n" + bounded
	*parts = append(*parts, section)
	*budget -= len(section) + 2
}

func buildAgenticAIOutputExcerpt(stdout, stderr agenticCapturedOutput) (string, bool) {
	parts := []string{}
	budget := agenticCommandAIExcerptLimit
	stats := agenticOutputStats("STDOUT", stdout) + "\n" + agenticOutputStats("STDERR", stderr)
	appendAgenticExcerptSection(&parts, "OUTPUT RETENTION", stats, &budget)

	// stderr usually carries the highest-value diagnostics, so preserve it first.
	stderrBudget := budget
	if stderrBudget > 10*1024 {
		stderrBudget = 10 * 1024
	}
	if stderrBudget > 0 && strings.TrimSpace(stderr.Text) != "" {
		bounded, _ := truncateAgenticHeadTailText(stderr.Text, stderrBudget)
		appendAgenticExcerptSection(&parts, "STDERR (prioritized)", bounded, &budget)
	}

	// Pull distinct failure-oriented stdout lines before adding general head/tail context.
	importantBudget := budget
	if importantBudget > 6*1024 {
		importantBudget = 6 * 1024
	}
	important := agenticImportantOutputLines(stdout.Text, importantBudget)
	appendAgenticExcerptSection(&parts, "STDOUT IMPORTANT LINES", important, &budget)

	// Remaining budget keeps beginning and ending stdout, preserving command setup and stack-trace endings.
	appendAgenticExcerptSection(&parts, "STDOUT HEAD / TAIL", stdout.Text, &budget)

	joined := strings.Join(parts, "\n\n")
	truncated := stdout.Truncated || stderr.Truncated || len(joined) >= agenticCommandAIExcerptLimit
	if len(joined) > agenticCommandAIExcerptLimit {
		joined, _ = truncateAgenticHeadTailText(joined, agenticCommandAIExcerptLimit)
		truncated = true
	}
	return joined, truncated
}

func agenticStatusRecord(status, message string, at time.Time) agenticExecutionStatusRecord {
	return agenticExecutionStatusRecord{Status: status, Message: strings.TrimSpace(message), Timestamp: at.UTC().Format(time.RFC3339Nano)}
}

func redactAgenticCommand(command workModeAgenticCommand, configured []terminalEnvironment) workModeAgenticCommand {
	command.Type = redactTerminalEnvironmentValues(command.Type, configured)
	command.Executable = redactTerminalEnvironmentValues(command.Executable, configured)
	for i := range command.Args {
		command.Args[i] = redactTerminalEnvironmentValues(command.Args[i], configured)
	}
	command.Script = redactTerminalEnvironmentValues(command.Script, configured)
	command.WorkingDirectory = redactTerminalEnvironmentValues(command.WorkingDirectory, configured)
	command.Purpose = redactTerminalEnvironmentValues(command.Purpose, configured)
	return command
}

func finishAgenticExecutionResult(result *agenticExecutionResult, collector *agenticOutputCollector, configured []terminalEnvironment, started time.Time) {
	finished := time.Now().UTC()
	result.FinishedAt = finished.Format(time.RFC3339Nano)
	if !started.IsZero() {
		result.DurationMillis = finished.Sub(started).Milliseconds()
	}
	result.Stdout, result.Stderr, result.OutputRecords = collector.snapshot(configured)
	result.Command = redactAgenticCommand(result.Command, configured)
	result.Workspace = redactTerminalEnvironmentValues(result.Workspace, configured)
	result.WorkingDirectory = redactTerminalEnvironmentValues(result.WorkingDirectory, configured)
	result.ExecutablePath = redactTerminalEnvironmentValues(result.ExecutablePath, configured)
	result.Error = redactTerminalEnvironmentValues(result.Error, configured)
	result.BlockedPath = redactTerminalEnvironmentValues(result.BlockedPath, configured)
	for i := range result.StatusRecords {
		result.StatusRecords[i].Message = redactTerminalEnvironmentValues(result.StatusRecords[i].Message, configured)
	}
	result.AIOutputExcerpt, result.AIOutputTruncated = buildAgenticAIOutputExcerpt(result.Stdout, result.Stderr)
	result.StatusRecords = append(result.StatusRecords, agenticStatusRecord(result.Status, result.Error, finished))
}

func stopRunningAgenticCommand(controller *agenticProcessController, cmd *exec.Cmd, done <-chan error, grace time.Duration) (error, error) {
	controller.Graceful(cmd)
	select {
	case waitErr := <-done:
		return waitErr, nil
	case <-time.After(grace):
	}
	controller.Force(cmd)
	select {
	case waitErr := <-done:
		return waitErr, nil
	case <-time.After(5 * time.Second):
		return nil, errors.New("process tree did not report termination after force stop")
	}
}

func (a *App) executeAgenticCommand(ctx context.Context, request agenticExecutionRequest, options agenticExecutionOptions) agenticExecutionResult {
	if ctx == nil {
		ctx = context.Background()
	}
	options = normalizeAgenticExecutionOptions(options)
	collector := newAgenticOutputCollector(options.OutputLimit)
	result := agenticExecutionResult{Status: agenticExecutionStatusBlocked, Command: normalizeWorkModeAgenticCommand(request.Command), StatusRecords: []agenticExecutionStatusRecord{}}
	configuredForRedaction := []terminalEnvironment{}
	if configuredFile, err := a.loadTerminalEnvironment(request.ProjectName); err == nil {
		configuredForRedaction = append(configuredForRedaction, configuredFile.Variables...)
	}
	prepared, err := prepareAgenticExecution(a, request)
	if err != nil {
		result.Error = err.Error()
		var pathErr *agenticVisiblePathError
		if errors.As(err, &pathErr) {
			result.BlockedPath = pathErr.Path
		}
		finishAgenticExecutionResult(&result, collector, configuredForRedaction, time.Time{})
		emitAgenticExecutionEvent(options.OnEvent, "status", "", result.Status, result.Error)
		return result
	}
	result.Command, result.Workspace = prepared.Command, prepared.WorkspaceRoot
	result.WorkingDirectory, result.ExecutablePath = prepared.WorkingDirectory, prepared.ExecutablePath
	result.StatusRecords = append(result.StatusRecords, agenticStatusRecord("validated", "Command request validated against the staged workspace.", time.Now()))
	if err := ctx.Err(); err != nil {
		result.Status, result.Cancelled, result.Error = agenticExecutionStatusCancelled, true, err.Error()
		finishAgenticExecutionResult(&result, collector, prepared.Configured, time.Time{})
		emitAgenticExecutionEvent(options.OnEvent, "status", "", result.Status, result.Error)
		return result
	}

	emitAgenticExecutionEvent(options.OnEvent, "status", "", "validated", "Command request validated against the staged workspace.")
	cmd, outputWriters := commandFromPrepared(prepared, collector, options.OnEvent)
	controller, err := newAgenticProcessController(cmd)
	if err != nil {
		result.Status, result.Error = agenticExecutionStatusStartError, err.Error()
		finishAgenticExecutionResult(&result, collector, prepared.Configured, time.Time{})
		emitAgenticExecutionEvent(options.OnEvent, "status", "", result.Status, result.Error)
		return result
	}
	defer controller.Close()

	started := time.Now().UTC()
	result.StartedAt = started.Format(time.RFC3339Nano)
	if err := cmd.Start(); err != nil {
		result.Status, result.Error = agenticExecutionStatusStartError, err.Error()
		emitAgenticExecutionEvent(options.OnEvent, "status", "", result.Status, result.Error)
		finishAgenticExecutionResult(&result, collector, prepared.Configured, started)
		return result
	}
	if err := controller.AfterStart(cmd); err != nil {
		controller.Force(cmd)
		_ = cmd.Wait()
		result.Status, result.Error = agenticExecutionStatusStartError, fmt.Sprintf("could not attach process containment: %v", err)
		finishAgenticExecutionResult(&result, collector, prepared.Configured, started)
		emitAgenticExecutionEvent(options.OnEvent, "status", "", result.Status, result.Error)
		return result
	}
	result.StatusRecords = append(result.StatusRecords, agenticStatusRecord("started", "Command process started.", started))
	emitAgenticExecutionEvent(options.OnEvent, "status", "", "started", "Command process started.")

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(options.Timeout)
	defer timer.Stop()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		result.Status, result.Cancelled = agenticExecutionStatusCancelled, true
		result.StatusRecords = append(result.StatusRecords, agenticStatusRecord("stopping", "Cancellation requested; attempting graceful process-tree termination.", time.Now()))
		emitAgenticExecutionEvent(options.OnEvent, "status", "", "stopping", "Cancellation requested; attempting graceful process-tree termination.")
		var stopErr error
		waitErr, stopErr = stopRunningAgenticCommand(controller, cmd, done, options.GracePeriod)
		result.Error = ctx.Err().Error()
		if stopErr != nil {
			result.Error += "; " + stopErr.Error()
		}
	case <-timer.C:
		result.Status, result.TimedOut = agenticExecutionStatusTimedOut, true
		result.StatusRecords = append(result.StatusRecords, agenticStatusRecord("stopping", fmt.Sprintf("Command runtime limit of %s reached; attempting graceful process-tree termination.", options.Timeout), time.Now()))
		emitAgenticExecutionEvent(options.OnEvent, "status", "", "stopping", fmt.Sprintf("Command runtime limit of %s reached; attempting graceful process-tree termination.", options.Timeout))
		var stopErr error
		waitErr, stopErr = stopRunningAgenticCommand(controller, cmd, done, options.GracePeriod)
		result.Error = fmt.Sprintf("command exceeded runtime limit of %s", options.Timeout)
		if stopErr != nil {
			result.Error += "; " + stopErr.Error()
		}
	}

	if !result.Cancelled && !result.TimedOut {
		exitCode := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
			result.ExitCode, result.Status, result.Error = &exitCode, agenticExecutionStatusFailed, waitErr.Error()
		} else {
			result.ExitCode, result.Status = &exitCode, agenticExecutionStatusCompleted
		}
	} else if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode := exitErr.ExitCode()
			result.ExitCode = &exitCode
		}
	}
	for _, writer := range outputWriters {
		writer.flush()
	}
	finishAgenticExecutionResult(&result, collector, prepared.Configured, started)
	emitAgenticExecutionEvent(options.OnEvent, "status", "", result.Status, result.Error)
	return result
}
