package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalWhitelistValidationAndForceFallback(t *testing.T) {
	app := &App{cfg: AppConfig{WorkRoot: t.TempDir()}}
	if err := app.ensureProjectScaffold("Demo"); err != nil {
		t.Fatal(err)
	}
	valid := `{"schema_version":1,"rules":[{"type":"direct","executable":"go","args":["test","./..."]}]}`
	validation, active, err := app.saveTerminalWhitelist("Demo", valid, false)
	if err != nil || !validation.Valid || !active {
		t.Fatalf("save valid: validation=%+v active=%v err=%v", validation, active, err)
	}
	invalid := `{"schema_version":1,"rules":[`
	validation, active, err = app.saveTerminalWhitelist("Demo", invalid, true)
	if err != nil || validation.Valid || active {
		t.Fatalf("force invalid: validation=%+v active=%v err=%v", validation, active, err)
	}
	content, gotValidation, usingFallback, err := app.loadTerminalWhitelistContent("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if content != invalid || gotValidation.Valid || !usingFallback {
		t.Fatalf("fallback state content=%q validation=%+v fallback=%v", content, gotValidation, usingFallback)
	}
	backup, err := app.terminalProjectConfigPath("Demo", terminalWhitelistBackupFilename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestTerminalEnvironmentStorageSanitizationAndRedaction(t *testing.T) {
	app := &App{cfg: AppConfig{WorkRoot: t.TempDir()}}
	if err := app.ensureProjectScaffold("Demo"); err != nil {
		t.Fatal(err)
	}
	file := defaultTerminalEnvironmentFile()
	file.Variables = []terminalEnvironment{{Name: "PORT", Value: "8080", Description: "Local port"}, {Name: "DEPLOY_TOKEN", Value: "secret-value", Description: "Credential"}}
	if err := app.saveTerminalEnvironment("Demo", file); err != nil {
		t.Fatal(err)
	}
	loaded, err := app.loadTerminalEnvironment("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Variables) != 2 {
		t.Fatalf("variables=%d", len(loaded.Variables))
	}
	env := sanitizedTerminalEnvironment([]string{"PATH=/bin", "GITHUB_TOKEN=inherited", "HOME=/tmp/home", "GOROOT=/toolchain/go", "LC_CTYPE=en_US.UTF-8", "UNRELATED_FLAG=drop-me"}, loaded.Variables)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "GOROOT=/toolchain/go") || !strings.Contains(joined, "LC_CTYPE=en_US.UTF-8") || !strings.Contains(joined, "DEPLOY_TOKEN=secret-value") || strings.Contains(joined, "GITHUB_TOKEN") || strings.Contains(joined, "UNRELATED_FLAG") {
		t.Fatalf("sanitized environment unexpected: %s", joined)
	}
	redacted := redactTerminalEnvironmentValues("token=secret-value port=8080", loaded.Variables)
	if strings.Contains(redacted, "secret-value") || strings.Contains(redacted, "8080") {
		t.Fatalf("secret remained: %s", redacted)
	}
	path, _ := app.terminalProjectConfigPath("Demo", terminalEnvironmentFilename)
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("terminalenv permissions too broad: %o", info.Mode().Perm())
	}
}

func makeGitMetadataZIP(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repo.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	for name, content := range map[string]string{"main.go": "package main\n", ".git/config": "[core]\n", "nested/.git/HEAD": "ref"} {
		writer, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestZIPImportGitMetadataChoice(t *testing.T) {
	zipPath := makeGitMetadataZIP(t)
	without := t.TempDir()
	if _, err := unzipArchiveInto(zipPath, without, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(without, ".git", "config")); !os.IsNotExist(err) {
		t.Fatalf(".git should be excluded, err=%v", err)
	}
	with := t.TempDir()
	if _, err := unzipArchiveInto(zipPath, with, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(with, ".git", "config"))
	if err != nil || !bytes.Contains(data, []byte("core")) {
		t.Fatalf(".git should be included data=%q err=%v", data, err)
	}
}

func TestCopyWorkingTreeGitMetadataChoice(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "app.rb"), []byte("puts :ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	without := t.TempDir()
	if _, err := copyWorkingTreeInto(src, without, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(without, ".git", "HEAD")); !os.IsNotExist(err) {
		t.Fatalf(".git should be excluded, err=%v", err)
	}
	with := t.TempDir()
	if _, err := copyWorkingTreeInto(src, with, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(with, ".git", "HEAD")); err != nil {
		t.Fatalf(".git should be included: %v", err)
	}
}

func TestTerminalWhitelistValidationWarnsForEmbeddedWildcards(t *testing.T) {
	content := `{"schema_version":1,"rules":[{"type":"direct","executable":"go*","args":["test"]},{"type":"shell","script":"echo *"}]}`
	_, validation := validateTerminalWhitelistJSON(content)
	if !validation.Valid {
		t.Fatalf("validation should remain valid: %+v", validation)
	}
	wildcardWarnings := 0
	for _, warning := range validation.Warnings {
		if strings.Contains(warning, "wildcard authorization") {
			wildcardWarnings++
		}
	}
	if wildcardWarnings != 2 {
		t.Fatalf("wildcard warnings=%d warnings=%v", wildcardWarnings, validation.Warnings)
	}
}
