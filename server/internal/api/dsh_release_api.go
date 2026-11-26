package api

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hikariming/dsh-ee/server/internal/store"
)

func dshReleaseVerified(r *store.DshRelease) bool {
	if r.UpstreamURL == "" {
		return r.ChecksumVerified
	}
	return r.Kind != "dmg"
}

func publicDshRelease(r *store.DshRelease, base string) map[string]any {
	source := "official"
	if r.UpstreamURL == "" {
		source = "import"
	}
	return map[string]any{
		"source": source,
		"id":     r.ID, "target": r.Target, "kind": r.Kind, "version": r.Version, "released_at": r.ReleasedAt,
		"size": r.Size, "sha512": r.SHA512, "recommended": r.Recommended, "verified": dshReleaseVerified(r),
		"download_url": base + "/dl/" + r.Target + "/" + url.PathEscape(r.File),
	}
}

func (s *Server) handleDshReleases(w http.ResponseWriter, r *http.Request) {
	set, err := s.appSettings(r.Context())
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	var out []map[string]any
	if set.Distribution.Enabled {
		targets := make([]string, 0, len(s.releaseFeeds))
		for target := range s.releaseFeeds {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		for _, target := range targets {
			items, err := s.store.ListDshReleases(r.Context(), target)
			if err != nil {
				s.internalErr(w, r, err)
				return
			}
			for _, item := range items {
				if item.State == store.ReleaseMirrored {
					out = append(out, publicDshRelease(item, s.publicBase(r)))
				}
			}
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	pushMode := set.Distribution.PushMode
	if pushMode == "" {
		pushMode = "notify"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   set.Distribution.Enabled,
		"push_mode": pushMode,
		"push_note": set.Distribution.PushNote,
		"releases":  out,
	})
}

func (s *Server) handleAdminDshReleases(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDshReleases(r.Context(), "")
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		source := "official"
		if item.UpstreamURL == "" {
			source = "import"
		}
		v := map[string]any{
			"source": source,
			"id":     item.ID, "target": item.Target, "kind": item.Kind, "version": item.Version, "released_at": item.ReleasedAt,
			"file": item.File, "size": item.Size, "sha512": item.SHA512, "upstream_url": item.UpstreamURL,
			"state": item.State, "recommended": item.Recommended, "error": item.Error, "mirrored_at": item.MirroredAt,
			"created_at": item.CreatedAt, "verified": dshReleaseVerified(item),
		}
		if item.State == store.ReleaseDownloading {
			var downloaded int64
			if part, err := s.releasePartPath(item); err == nil {
				if info, err := os.Stat(part); err == nil {
					downloaded = info.Size()
				}
			}
			v["downloaded"] = downloaded
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": out})
}

func (s *Server) handleCheckDshReleases(w http.ResponseWriter, r *http.Request) {
	s.kickDshReleases()
	s.audit(r, auditDshReleaseCheck, "", nil)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) handleMirrorDshRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.store.DshReleaseByID(r.Context(), id)
	if notFound(err) {
		writeErr(w, http.StatusNotFound, "版本不存在")
		return
	}
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	if item.UpstreamURL == "" {
		writeErrCode(w, http.StatusBadRequest, codeReleaseImported, nil, "离线导入的安装包不能从官方下载，请重新导入")
		return
	}
	if item.State == store.ReleaseMirrored {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	go func() {
		// 不跟随请求取消（关掉管理页面不该中止镜像），但必须有自己的总时长上限，
		// 否则上游挂起会永久占着 goroutine，DB 状态一直卡在 downloading。
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), dshReleaseTimeout)
		defer cancel()
		if err := s.downloadDshRelease(ctx, id); err != nil {
			s.log.Warn("下载 Desktop 安装包失败", "id", id, "err", err)
		}
	}()
	s.audit(r, auditDshReleaseMirror, item.Target+"/"+item.File, map[string]any{"version": item.Version})
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) handleRecommendDshRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.SetDshReleaseRecommended(r.Context(), id); err != nil {
		switch {
		case notFound(err):
			writeErr(w, http.StatusNotFound, "版本不存在")
		case errors.Is(err, store.ErrReleaseNotMirrored):
			writeErr(w, http.StatusBadRequest, err.Error())
		default:
			s.internalErr(w, r, err)
		}
		return
	}
	s.hub.notify()
	s.audit(r, auditDshReleaseRecommend, "dsh-release:"+r.PathValue("id"), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type dshDesktopRelease struct {
	ID          int64     `json:"id"`
	Target      string    `json:"target"`
	Kind        string    `json:"kind"`
	Version     string    `json:"version"`
	File        string    `json:"file"`
	Size        int64     `json:"size"`
	SHA512      string    `json:"sha512"`
	Verified    bool      `json:"verified"`
	Source      string    `json:"source"`
	ReleasedAt  time.Time `json:"released_at"`
	DownloadURL string    `json:"download_url"`
}

type dshDesktopUpdate struct {
	Mode     string                       `json:"mode"`
	Note     string                       `json:"note,omitempty"`
	Releases map[string]dshDesktopRelease `json:"releases"`
}

func (s *Server) desktopUpdateCatalog(ctx context.Context, set *store.AppSettings, base string) (*dshDesktopUpdate, error) {
	mode := set.Distribution.PushMode
	if mode == "" {
		mode = "notify"
	}
	out := &dshDesktopUpdate{
		Mode:     mode,
		Note:     set.Distribution.PushNote,
		Releases: map[string]dshDesktopRelease{},
	}
	if !set.Distribution.Enabled || mode == "off" {
		out.Mode = "off"
		out.Note = ""
		return out, nil
	}
	items, err := s.store.ListDshReleases(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if !item.Recommended || item.State != store.ReleaseMirrored {
			continue
		}
		if _, exists := out.Releases[item.Target]; exists {
			continue
		}
		source := "official"
		if item.UpstreamURL == "" {
			source = "import"
		}
		dl := "/dl/" + item.Target + "/" + url.PathEscape(item.File)
		if base != "" {
			dl = base + dl
		}
		out.Releases[item.Target] = dshDesktopRelease{
			ID:          item.ID,
			Target:      item.Target,
			Kind:        item.Kind,
			Version:     item.Version,
			File:        item.File,
			Size:        item.Size,
			SHA512:      item.SHA512,
			Verified:    dshReleaseVerified(item),
			Source:      source,
			ReleasedAt:  item.ReleasedAt,
			DownloadURL: dl,
		}
	}
	return out, nil
}

func (s *Server) handleUpdateDshReleasePush(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PushMode string `json:"push_mode"`
		PushNote string `json:"push_note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求内容格式不对")
		return
	}
	mode := strings.TrimSpace(body.PushMode)
	if !slices.Contains([]string{"off", "notify", "auto_download"}, mode) {
		writeErr(w, http.StatusBadRequest, "Desktop 版本推送模式取值不对："+mode)
		return
	}
	note := strings.TrimSpace(body.PushNote)
	if len([]rune(note)) > 200 {
		writeErr(w, http.StatusBadRequest, "Desktop 升级说明不能超过 200 个字符")
		return
	}
	cur, err := s.appSettings(r.Context())
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	next := *cur
	next.Distribution.PushMode = mode
	next.Distribution.PushNote = note
	if err := s.store.SaveSettings(r.Context(), &next, currentUser(r).ID); err != nil {
		s.internalErr(w, r, err)
		return
	}
	s.settings.mu.Lock()
	s.settings.v = nil
	s.settings.mu.Unlock()
	s.hub.notify()
	s.audit(r, auditDshReleasePush, mode, map[string]any{"push_mode": mode, "push_note": note})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "push_mode": mode, "push_note": note})
}

func parseDshHost(raw string) (kind, version, target string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown", "", ""
	}
	if raw == "cli" {
		return "cli", "", ""
	}
	if rest, ok := strings.CutPrefix(raw, "desktop/"); ok {
		parts := strings.Split(rest, "/")
		version = strings.TrimSpace(parts[0])
		if len(parts) >= 2 {
			t := strings.TrimSpace(parts[1])
			if t == "mac-arm64" || t == "mac-x64" || t == "win-x64" {
				target = t
			}
		}
		return "desktop", version, target
	}
	return "unknown", "", ""
}

func compareReleaseVersion(a, b string) int {
	parse := func(v string) ([]int, string) {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		core, pre, _ := strings.Cut(v, "-")
		segs := strings.Split(core, ".")
		nums := make([]int, len(segs))
		for i, seg := range segs {
			n, _ := strconv.Atoi(seg)
			nums[i] = n
		}
		return nums, pre
	}
	an, ap := parse(a)
	bn, bp := parse(b)
	n := len(an)
	if len(bn) > n {
		n = len(bn)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(an) {
			av = an[i]
		}
		if i < len(bn) {
			bv = bn[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	if ap == bp {
		return 0
	}
	if ap == "" {
		return 1
	}
	if bp == "" {
		return -1
	}
	if ap < bp {
		return -1
	}
	return 1
}

func (s *Server) handleAdminDshReleaseClients(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAllDshClients(r.Context(), time.Now().Add(-30*24*time.Hour))
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	releases, err := s.store.ListDshReleases(r.Context(), "")
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	recommended := map[string]string{}
	uniqueVersions := map[string]bool{}
	for _, rel := range releases {
		if rel.Recommended && rel.State == store.ReleaseMirrored {
			if _, ok := recommended[rel.Target]; !ok {
				recommended[rel.Target] = rel.Version
				uniqueVersions[rel.Version] = true
			}
		}
	}
	fallbackVersion := ""
	if len(uniqueVersions) == 1 {
		for v := range uniqueVersions {
			fallbackVersion = v
		}
	}

	type fleetClient struct {
		Username           string     `json:"username"`
		Department         string     `json:"department"`
		ClientName         string     `json:"client_name"`
		LastClient         string     `json:"last_client"`
		LastHost           string     `json:"last_host"`
		HostKind           string     `json:"host_kind"`
		Version            string     `json:"version"`
		Target             string     `json:"target"`
		RecommendedVersion string     `json:"recommended_version"`
		Status             string     `json:"status"`
		LastUsedAt         *time.Time `json:"last_used_at"`
	}
	out := make([]fleetClient, 0, len(items))
	summary := map[string]int{"total": 0, "up_to_date": 0, "outdated": 0, "unknown": 0}
	for _, item := range items {
		kind, ver, target := parseDshHost(item.LastHost)
		rec := ""
		if target != "" {
			rec = recommended[target]
		} else if kind == "desktop" {
			rec = fallbackVersion
		}
		status := "unknown"
		if kind == "desktop" && ver != "" && rec != "" {
			if compareReleaseVersion(ver, rec) >= 0 {
				status = "up_to_date"
			} else {
				status = "outdated"
			}
		}
		summary["total"]++
		summary[status]++
		out = append(out, fleetClient{
			Username:           item.Username,
			Department:         item.Department,
			ClientName:         item.ClientName,
			LastClient:         item.LastClient,
			LastHost:           item.LastHost,
			HostKind:           kind,
			Version:            ver,
			Target:             target,
			RecommendedVersion: rec,
			Status:             status,
			LastUsedAt:         item.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summary":     summary,
		"recommended": recommended,
		"clients":     out,
	})
}

func (s *Server) handleDeleteDshRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.store.DshReleaseByID(r.Context(), id)
	if notFound(err) {
		writeErr(w, http.StatusNotFound, "版本不存在")
		return
	}
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	if item.Recommended {
		writeErr(w, http.StatusBadRequest, store.ErrReleaseRecommended.Error())
		return
	}
	if item.State == store.ReleaseDownloading {
		writeErr(w, http.StatusConflict, "下载中的版本不能删除")
		return
	}
	if item.LocalPath != "" {
		if err := os.Remove(item.LocalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.internalErr(w, r, err)
			return
		}
	}
	if err := s.store.ResetDshRelease(r.Context(), id); err != nil {
		s.internalErr(w, r, err)
		return
	}
	s.audit(r, auditDshReleaseDelete, item.Target+"/"+item.File, map[string]any{"version": item.Version})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDownloadDshRelease(w http.ResponseWriter, r *http.Request) {
	target, file := r.PathValue("target"), r.PathValue("file")
	if target == "" || file == "" || file == "." || file == ".." || strings.Contains(file, `/`) || strings.Contains(file, `\`) {
		writeErr(w, http.StatusBadRequest, "安装包路径无效")
		return
	}
	items, err := s.store.ListDshReleases(r.Context(), target)
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	var item *store.DshRelease
	for _, candidate := range items {
		if candidate.File == file && candidate.State == store.ReleaseMirrored {
			item = candidate
			break
		}
	}
	if item == nil {
		writeErr(w, http.StatusNotFound, "安装包不存在或尚未下载到 dsh-ee")
		return
	}
	part, err := s.releasePartPath(item)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "安装包路径无效")
		return
	}
	name := strings.TrimSuffix(part, ".part")
	f, err := os.Open(filepath.Clean(name))
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, "本地安装包不存在")
		return
	}
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.internalErr(w, r, err)
		return
	}
	modTime := item.ReleasedAt
	if item.MirroredAt != nil {
		modTime = *item.MirroredAt
	}
	if modTime.Equal(time.Time{}) {
		modTime = info.ModTime()
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.File}))
	w.Header().Set("X-Checksum-Sha512", item.SHA512)
	http.ServeContent(w, r, item.File, modTime, f)
}
