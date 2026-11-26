package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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

func TestDshInstallerPreservesPrerelease(t *testing.T) {
	for _, tc := range []struct {
		name, want string
	}{
		{"dsh-1.2.3-win-x64.exe", "1.2.3"},
		{"dsh-1.2.3-hotfix.1-win-x64.exe", "1.2.3-hotfix.1"},
		{"dsh-1.2.3-hotfix.1+build-1-arm64-mac.dmg", "1.2.3-hotfix.1+build-1"},
		{"dsh-1.2.3-hotfix.1.msi", "1.2.3-hotfix.1"},
		{"dsh-1.2.3-hotfix_.1-win-x64.exe", ""},
	} {
		_, got, err := dshInstallerDetails(tc.name, "", "")
		if tc.want == "" {
			if err == nil || !strings.Contains(err.Error(), "--version") {
				t.Errorf("%s: expected a --version diagnostic, got %q, %v", tc.name, got, err)
			}
		} else if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestDshInstallerStripsStandalonePlatformSuffix(t *testing.T) {
	for _, tc := range []struct {
		name, target, want string
	}{
		{"dsh-1.2.3-win.exe", "", "1.2.3"},
		{"dsh-1.2.3-hotfix.1-windows.msi", "", "1.2.3-hotfix.1"},
		{"dsh-1.2.3-mac.zip", "mac-arm64", "1.2.3"},
		{"dsh-1.2.3-hotfix.1-linux.exe", "win-x64", "1.2.3-hotfix.1"},
	} {
		_, got, err := dshInstallerDetails(tc.name, tc.target, "")
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestDshInstallerFilesSortByVersion(t *testing.T) {
	dir := t.TempDir()
	want := []string{
		"dsh-0.2.9-win-x64.exe",
		"dsh-0.2.10-rc.2-win-x64.exe",
		"dsh-0.2.10-rc.10-win-x64.exe",
		"a-dsh-0.2.10-mac-arm64.dmg",
		"b-dsh-0.2.10-mac-arm64.zip",
		"z-dsh-0.2.10-win-x64.exe",
		"dsh-latest-win-x64.exe",
	}
	for _, name := range want {
		writeDshInstaller(t, dir, name)
	}
	files, err := dshInstallerFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, file := range files {
		files[i] = filepath.Base(file)
	}
	if !slices.Equal(files, want) {
		t.Fatalf("got upload order %q, want %q", files, want)
	}
}

func TestDshPushBrowserRenamedFile(t *testing.T) {
	const name = "dsh-1.2.3-mac-arm64 (1).zip"
	file := writeDshInstaller(t, t.TempDir(), name)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("file") != name || q.Get("version") != "1.2.3" || q.Get("target") != "mac-arm64" {
			t.Errorf("incorrect import metadata: %v", q)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"ok":true,"id":2}`)
	}))
	defer server.Close()
	_, code, out, stderr := cli("dsh", "push", "--server", server.URL, "--token", "ee_at_fixture", file)
	if code != exitOK || !strings.Contains(out, "1 成功，0 失败") || stderr != "" {
		t.Fatalf("renamed file did not import: %d\n%s\n%s", code, out, stderr)
	}
}

func TestDshPushWarnsOnRemoteHTTP(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.exe")
	for _, tc := range []struct {
		server, language, warning string
	}{
		{"http://ee.example.com", "en", "Warning: the token and installer will be sent over unencrypted HTTP"},
		{"http://192.168.1.2:8080", "zh", "警告：令牌和安装包将通过未加密的 HTTP 传输"},
		{"https://ee.example.com", "en", ""},
		{"http://LOCALHOST.:8080", "en", ""},
		{"http://127.0.0.1:8080", "en", ""},
		{"http://[::1]:8080", "en", ""},
	} {
		_, code, _, stderr := cli("--lang="+tc.language, "dsh", "push", "--server", tc.server, "--token", "ee_at_fixture", missing)
		if code != exitProblems || !strings.Contains(stderr, "missing.exe") {
			t.Errorf("%s: push did not proceed to file validation: %d, %q", tc.server, code, stderr)
		}
		if tc.warning != "" && !strings.Contains(stderr, tc.warning) {
			t.Errorf("%s: missing warning: %q", tc.server, stderr)
		}
		if tc.warning == "" && strings.Contains(stderr, "Warning:") {
			t.Errorf("%s: unexpected warning: %q", tc.server, stderr)
		}
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
