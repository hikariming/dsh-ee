package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDshPushDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"deepseek-harness-1.2.3-rc.5-mac-arm64.ZIP",
		"deepseek-harness-1.2.3-win-x64.exe",
		"deepseek-harness-2.0.0-macos-intel.dmg",
		"nightly.yml",
	} {
		writeDshInstaller(t, dir, name)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested.zip"), 0700); err != nil {
		t.Fatal(err)
	}
	writeDshInstaller(t, filepath.Join(dir, "nested.zip"), "dsh-3.0.0.exe")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		file := q.Get("file")
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != file || r.ContentLength != int64(len(file)) ||
			r.Method != "POST" || r.URL.Path != "/api/admin/dsh-releases/import" ||
			r.Header.Get("Authorization") != "Bearer ee_at_cli_fixture" || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("invalid upload for %s: %s %s, bytes=%d, err=%v", file, r.Method, r.URL.Path, r.ContentLength, err)
		}
		switch file {
		case "deepseek-harness-1.2.3-rc.5-mac-arm64.ZIP":
			if q.Get("target") != "mac-arm64" || q.Get("version") != "1.2.3-rc.5" {
				t.Errorf("invalid mac arm64 metadata: %v", q)
			}
		case "deepseek-harness-1.2.3-win-x64.exe":
			if q.Get("target") != "win-x64" || q.Get("version") != "1.2.3" {
				t.Errorf("invalid Windows metadata: %v", q)
			}
			http.Error(w, `{"error":"import conflict"}`, http.StatusConflict)
			return
		case "deepseek-harness-2.0.0-macos-intel.dmg":
			if q.Get("target") != "mac-x64" || q.Get("version") != "2.0.0" {
				t.Errorf("invalid Intel metadata: %v", q)
			}
		default:
			t.Errorf("unexpected upload: %s", file)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"ok":true,"id":12}`)
	}))
	defer server.Close()
	t.Setenv("DSH_EE_URL", server.URL)
	t.Setenv("DSH_EE_TOKEN", "ee_at_cli_fixture")
	handled, code, out, stderr := cli("--lang=en", "dsh", "push", dir)
	if !handled || code != exitProblems || !strings.Contains(out, "2 succeeded, 1 failed") || !strings.Contains(stderr, "HTTP 409: import conflict") {
		t.Fatalf("unexpected directory push result: %v %d\n%s\n%s", handled, code, out, stderr)
	}
}

func TestDshPushSingleFile(t *testing.T) {
	dir := t.TempDir()
	file := writeDshInstaller(t, dir, "dsh custom.zip")
	hash := strings.Repeat("a", 128)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("Authorization") != "Bearer ee_at_explicit" ||
			q.Get("target") != "mac-arm64" || q.Get("version") != "1.2.3+custom" ||
			q.Get("file") != "dsh custom.zip" || q.Get("sha512") != hash {
			t.Errorf("invalid explicit upload: %v", q)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"ok":true,"id":20}`)
	}))
	defer server.Close()
	t.Setenv("DSH_EE_URL", "https://unused.invalid")
	t.Setenv("DSH_EE_TOKEN", "ee_at_unused")
	_, code, out, stderr := cli("dsh", "push", "--server="+server.URL, "--token", "ee_at_explicit", file,
		"--target", "mac-arm64", "--version", "1.2.3+custom", "--sha512", hash)
	if code != exitOK || stderr != "" || !strings.Contains(out, "ID 20") || !strings.Contains(out, "1 成功，0 失败") {
		t.Fatalf("unexpected single file push result: %d\n%s\n%s", code, out, stderr)
	}
}

func TestDshPushRejectsUnknownMetadataAndRedirects(t *testing.T) {
	dir := t.TempDir()
	ambiguous := writeDshInstaller(t, dir, "dsh-1.2.3.zip")
	fixedDMG := writeDshInstaller(t, dir, "dsh-latest-macos-arm64.dmg")
	windows := writeDshInstaller(t, dir, "dsh-1.2.3.exe")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect should not be followed")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("file") != "dsh-1.2.3.exe" {
			t.Error("invalid metadata should not be uploaded")
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	t.Setenv("DSH_EE_URL", server.URL)
	t.Setenv("DSH_EE_TOKEN", "ee_at_fixture")
	_, code, out, stderr := cli("--lang=en", "dsh", "push", ambiguous, fixedDMG, windows)
	if code != exitProblems || !strings.Contains(out, "0 succeeded, 3 failed") || !strings.Contains(stderr, "--target") ||
		!strings.Contains(stderr, "--version") || !strings.Contains(stderr, "HTTP 302") {
		t.Fatalf("unexpected push result: %d\n%s\n%s", code, out, stderr)
	}
	_, code, _, stderr = cli("dsh", "push", dir, "--version", "1.2.3")
	if code != exitUsage || !strings.Contains(stderr, "不适用于目录") {
		t.Fatalf("unexpected result for directory with override: %d\n%s", code, stderr)
	}
}

func writeDshInstaller(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(name), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
