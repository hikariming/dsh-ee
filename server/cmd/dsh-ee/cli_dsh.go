package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

var dshPushUsage = cliText{
	zh: `用法：dsh-ee dsh push [--server 地址] [--token 令牌] <文件或目录>...
从安装包文件名识别平台与版本。目录只扫描当前层的 .zip、.dmg、.exe、.msi 文件。
单个文件可指定 --target mac-arm64|mac-x64|win-x64、--version 版本、--sha512 校验值。
地址与令牌也可通过 DSH_EE_URL、DSH_EE_TOKEN 提供；令牌须在「我的令牌」开启管理模式。
`,
	en: `Usage: dsh-ee dsh push [--server URL] [--token TOKEN] <file-or-dir>...
Detects target and version from filenames. Scans directories for .zip, .dmg, .exe, and .msi files without recursing.
For one file, you can set --target mac-arm64|mac-x64|win-x64, --version VERSION, and --sha512 CHECKSUM.
You can also set DSH_EE_URL and DSH_EE_TOKEN; enable admin mode for the token in "My tokens".
`,
}

var (
	dshInstallerVersionCore  = regexp.MustCompile(`\d+\.\d+\.\d+`)
	dshInstallerTargetSuffix = regexp.MustCompile(`(?i)[-_ ](?:(?:macos|mac|windows|win)[-_ ])?(?:arm64|aarch64|x64|x86_64|intel)(?:[-_ ](?:macos|mac|windows|win))?$`)
	dshImportVersion         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

func cliDsh(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "push" {
		args = args[1:]
	} else if len(args) == 0 || !dshHelp(args[0]) {
		fmt.Fprint(stderr, dshPushUsage)
		return exitUsage
	}
	if len(args) > 0 && dshHelp(args[0]) {
		fmt.Fprint(stdout, dshPushUsage)
		return exitOK
	}
	o, err := parseOpts(args, "server", "token", "target", "version", "sha512")
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	if err != nil || len(o.pos) == 0 || o.asJSON {
		fmt.Fprint(stderr, dshPushUsage)
		return exitUsage
	}
	target, releaseVersion, checksum := o.vals["target"], strings.TrimSpace(o.vals["version"]), o.vals["sha512"]
	if target != "" || releaseVersion != "" || checksum != "" {
		if len(o.pos) != 1 {
			fmt.Fprintln(stderr, tr("--target、--version 和 --sha512 仅用于单个文件", "--target, --version, and --sha512 are only for one file"))
			return exitUsage
		}
		if st, err := os.Stat(o.pos[0]); err == nil && st.IsDir() {
			fmt.Fprintln(stderr, tr("--target、--version 和 --sha512 不适用于目录", "--target, --version, and --sha512 do not apply to directories"))
			return exitUsage
		}
	}
	ep := cliEndpoint{
		base:  strings.TrimRight(strings.TrimSpace(firstNonEmpty(o.vals["server"], os.Getenv("DSH_EE_URL"))), "/"),
		token: strings.TrimSpace(firstNonEmpty(o.vals["token"], os.Getenv("DSH_EE_TOKEN"))),
	}
	u, err := url.Parse(ep.base)
	if err != nil || u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(ep.base, "#") {
		fmt.Fprintln(stderr, tr("服务地址无效；不要包含用户名、密码、查询参数或片段", "invalid server URL; omit credentials, query parameters, and fragments"))
		return exitUsage
	}
	c, err := ep.client(tr("已开启管理模式", "admin mode enabled"), 45*time.Minute)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	c.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	host := strings.TrimSuffix(u.Hostname(), ".")
	if u.Scheme == "http" && !strings.EqualFold(host, "localhost") && !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(stderr, tr("警告：令牌和安装包将通过未加密的 HTTP 传输；建议使用 HTTPS。", "Warning: the token and installer will be sent over unencrypted HTTP; use HTTPS if available."))
	}

	pushed, failed := 0, 0
	reportFailure := func(file string, err error) {
		failed++
		fmt.Fprintf(stderr, tr("推送失败 %s：%v\n", "Failed to push %s: %v\n"), file, err)
	}
	for _, input := range o.pos {
		files, err := dshInstallerFiles(input)
		if err != nil {
			reportFailure(input, err)
			continue
		}
		for _, file := range files {
			if err := pushDshInstaller(c, stdout, file, target, releaseVersion, checksum); err != nil {
				reportFailure(file, err)
			} else {
				pushed++
			}
		}
	}
	fmt.Fprintf(stdout, tr("推送完成：%d 成功，%d 失败\n", "Push complete: %d succeeded, %d failed\n"), pushed, failed)
	if failed > 0 {
		return exitProblems
	}
	return exitOK
}

func dshHelp(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

func dshInstallerFiles(input string) ([]string, error) {
	st, err := os.Stat(input)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		if !st.Mode().IsRegular() {
			return nil, errors.New(tr("不是普通文件", "not a regular file"))
		}
		return []string{input}, nil
	}
	entries, err := os.ReadDir(input)
	if err != nil {
		return nil, err
	}
	type installer struct {
		name, target string
		version      *semver.Version
	}
	var installers []installer
	for _, e := range entries {
		if e.Type().IsRegular() && dshInstallerFamily(e.Name()) != "" {
			target, version, _ := dshInstallerDetails(e.Name(), "", "")
			parsedVersion, _ := semver.StrictNewVersion(version)
			installers = append(installers, installer{e.Name(), target, parsedVersion})
		}
	}
	if len(installers) == 0 {
		return nil, errors.New(tr("目录中没有支持的安装包", "directory contains no supported installers"))
	}
	slices.SortFunc(installers, func(a, b installer) int {
		switch {
		case a.version == nil && b.version != nil:
			return 1
		case a.version != nil && b.version == nil:
			return -1
		case a.version != nil:
			if order := a.version.Compare(b.version); order != 0 {
				return order
			}
		}
		if order := cmp.Compare(a.target, b.target); order != 0 {
			return order
		}
		return cmp.Compare(a.name, b.name)
	})
	files := make([]string, len(installers))
	for i, item := range installers {
		files[i] = filepath.Join(input, item.name)
	}
	return files, nil
}

func dshInstallerFamily(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".exe", ".msi":
		return "win"
	case ".zip", ".dmg":
		return "mac"
	}
	return ""
}

func dshInstallerDetails(filename, target, releaseVersion string) (string, string, error) {
	family := dshInstallerFamily(filename)
	if family == "" {
		return "", "", errors.New(tr("仅支持 .zip、.dmg、.exe、.msi", "only .zip, .dmg, .exe, and .msi are supported"))
	}
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	if target == "" {
		lower := strings.ToLower(stem)
		switch {
		case family == "win":
			target = "win-x64"
		case strings.Contains(lower, "arm64") || strings.Contains(lower, "aarch64"):
			target = "mac-arm64"
		case strings.Contains(lower, "x64") || strings.Contains(lower, "x86_64") || strings.Contains(lower, "intel"):
			target = "mac-x64"
		default:
			return "", "", errors.New(tr("无法识别平台；单独推送此文件并指定 --target", "cannot detect target; push this file separately with --target"))
		}
	}
	if (family == "win" && target != "win-x64") || (family == "mac" && target != "mac-arm64" && target != "mac-x64") {
		return "", "", fmt.Errorf(tr("平台 %q 不支持此文件类型", "target %q does not support this file type"), target)
	}
	if releaseVersion == "" {
		versionStem := dshInstallerTargetSuffix.ReplaceAllString(stem, "")
		if loc := dshInstallerVersionCore.FindStringIndex(versionStem); loc != nil {
			candidate := versionStem[loc[0]:]
			if _, err := semver.StrictNewVersion(candidate); err == nil {
				releaseVersion = candidate
			}
		}
	}
	if !dshImportVersion.MatchString(releaseVersion) {
		return "", "", errors.New(tr("无法识别有效版本；单独推送此文件并指定 --version", "cannot determine a valid version; push this file separately with --version"))
	}
	return target, releaseVersion, nil
}

func pushDshInstaller(c *eeClient, stdout io.Writer, file, target, releaseVersion, checksum string) error {
	name := filepath.Base(file)
	target, releaseVersion, err := dshInstallerDetails(name, target, releaseVersion)
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return errors.New(tr("安装包为空", "installer is empty"))
	}
	q := url.Values{"target": {target}, "version": {releaseVersion}, "file": {name}}
	if checksum != "" {
		q.Set("sha512", checksum)
	}
	req, err := http.NewRequest(http.MethodPost, c.base+"/api/admin/dsh-releases/import?"+q.Encode(), f)
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-DshEE-Client", "dsh-ee-cli/"+version)
	fmt.Fprintf(stdout, tr("正在推送 %s（%s %s，%d 字节）…\n", "Pushing %s (%s %s, %d bytes)…\n"), file, target, releaseVersion, st.Size())
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, 64<<10)
	if resp.StatusCode != http.StatusCreated {
		raw, err := io.ReadAll(body)
		if err != nil {
			return fmt.Errorf("HTTP %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, serverErrText(raw))
	}
	var result struct {
		OK bool
		ID int64
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil || !result.OK || result.ID <= 0 {
		return errors.New(tr("服务端未返回有效的导入结果", "server did not return a valid import result"))
	}
	fmt.Fprintf(stdout, tr("已导入 %s（ID %d）\n", "Imported %s (ID %d)\n"), file, result.ID)
	return nil
}
