package api_test

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hikariming/dsh-ee/server/internal/rbac"
)

func TestDshReleasePushCatalogDownloadAndClients(t *testing.T) {
	f := newPrincipalFixture(t)
	admin := newClient(t, f.ts.URL)
	code, body := admin.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": adminPassword}, nil)
	expect(t, "管理员登录", code, 200, body)

	// 1. 导入一个 mac-arm64 离线包并设为推荐
	payload := []byte("fake-dsh-desktop-zip-payload-v0.3.0")
	sum := sha512.Sum512(payload)
	shaB64 := base64.StdEncoding.EncodeToString(sum[:])

	req, err := http.NewRequest("POST", f.ts.URL+"/api/admin/dsh-releases/import?target=mac-arm64&version=0.3.0&file=dsh-0.3.0-arm64.zip&sha512="+shaB64, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := admin.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var importRes struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&importRes); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || importRes.ID == 0 {
		t.Fatalf("离线导入失败：status=%d id=%d", resp.StatusCode, importRes.ID)
	}

	code, body = admin.do("POST", "/api/admin/dsh-releases/"+itoa(importRes.ID)+"/recommend", nil, nil)
	expect(t, "设为推荐", code, 200, body)

	// 2. 配置推送策略（auto_download + 升级说明）
	code, body = admin.do("PUT", "/api/admin/dsh-releases/push", map[string]any{
		"push_mode": "invalid_mode",
	}, nil)
	expect(t, "非法推送模式应 400", code, 400, body)

	code, body = admin.do("PUT", "/api/admin/dsh-releases/push", map[string]any{
		"push_mode": "auto_download",
		"push_note": "修复内网代理问题，请尽快升级",
	}, nil)
	expect(t, "更新推送策略", code, 200, body)

	// 3. 员工令牌拉取 /api/dsh/catalog，应拿到 desktop_update 且支持 ETag
	uOld := f.user(t, "push-old-user", nil)
	uNew := f.user(t, "push-new-user", nil)
	scopes := []string{rbac.ScopeModels, rbac.ScopeHub, rbac.ScopeConnections}
	f.token(t, uOld.ID, "ee_at_push_old", scopes, nil, nil)
	f.token(t, uNew.ID, "ee_at_push_new", scopes, nil, nil)

	c := newClient(t, f.ts.URL)
	oldHeaders := map[string]string{
		"Authorization":  "Bearer ee_at_push_old",
		"X-DshEE-Client": "dsh-connect/1.0.8",
		"X-DshEE-Host":   "desktop/0.2.1-rc.5/mac-arm64",
	}
	doWithHeaders := func(method, path string, headers map[string]string) (int, []byte, http.Header) {
		t.Helper()
		req, err := http.NewRequest(method, f.ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.Bytes(), resp.Header
	}
	code, body, hdr := doWithHeaders("GET", "/api/dsh/catalog", oldHeaders)
	expect(t, "旧版本客户端拉取目录", code, 200, body)
	etag1 := hdr.Get("ETag")
	if etag1 == "" {
		t.Fatalf("应返回 ETag")
	}

	cat := decode[struct {
		DesktopUpdate struct {
			Mode     string `json:"mode"`
			Note     string `json:"note"`
			Releases map[string]struct {
				Version     string `json:"version"`
				File        string `json:"file"`
				SHA512      string `json:"sha512"`
				DownloadURL string `json:"download_url"`
			} `json:"releases"`
		} `json:"desktop_update"`
	}](t, body)
	if cat.DesktopUpdate.Mode != "auto_download" || cat.DesktopUpdate.Note != "修复内网代理问题，请尽快升级" {
		t.Fatalf("desktop_update 模式或说明不对：%+v", cat.DesktopUpdate)
	}
	rel, ok := cat.DesktopUpdate.Releases["mac-arm64"]
	if !ok || rel.Version != "0.3.0" || rel.SHA512 != shaB64 || rel.File != "dsh-0.3.0-arm64.zip" {
		t.Fatalf("mac-arm64 推送版本不对：%+v", cat.DesktopUpdate.Releases)
	}

	// ETag 命中 304
	oldHeaders["If-None-Match"] = etag1
	code, _, _ = doWithHeaders("GET", "/api/dsh/catalog", oldHeaders)
	expect(t, "目录未变更应 304", code, 304, nil)

	// 修改推送策略后 ETag 变化
	code, body = admin.do("PUT", "/api/admin/dsh-releases/push", map[string]any{
		"push_mode": "notify",
		"push_note": "改为提醒升级",
	}, nil)
	expect(t, "切换为 notify", code, 200, body)
	code, body, hdr2 := doWithHeaders("GET", "/api/dsh/catalog", oldHeaders)
	expect(t, "策略变更后应返回 200", code, 200, body)
	if hdr2.Get("ETag") == etag1 {
		t.Fatalf("策略变更后 ETag 应该更新：%s", etag1)
	}

	// 4. 员工用 Bearer 令牌直接下载 /dl/{target}/{file}
	dlReq, _ := http.NewRequest("GET", rel.DownloadURL, nil)
	dlReq.Header.Set("Authorization", "Bearer ee_at_push_old")
	dlResp, err := c.hc.Do(dlReq)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	_, _ = got.ReadFrom(dlResp.Body)
	dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK || !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("Bearer 下载安装包失败：status=%d len=%d", dlResp.StatusCode, got.Len())
	}

	// 5. 另一台已升级的客户端上报，管理员查看终端版本分布
	newHeaders := map[string]string{
		"Authorization":  "Bearer ee_at_push_new",
		"X-DshEE-Client": "dsh-connect/1.0.8",
		"X-DshEE-Host":   "desktop/0.3.0/mac-arm64",
	}
	code, body = c.do("GET", "/api/dsh/catalog", nil, newHeaders)
	expect(t, "新版本客户端上报", code, 200, body)

	code, body = admin.do("GET", "/api/admin/dsh-releases/clients", nil, nil)
	expect(t, "管理员查终端版本分布", code, 200, body)
	fleet := decode[struct {
		Summary struct {
			Total    int `json:"total"`
			UpToDate int `json:"up_to_date"`
			Outdated int `json:"outdated"`
			Unknown  int `json:"unknown"`
		} `json:"summary"`
		Clients []struct {
			Username           string `json:"username"`
			Version            string `json:"version"`
			Target             string `json:"target"`
			RecommendedVersion string `json:"recommended_version"`
			Status             string `json:"status"`
		} `json:"clients"`
	}](t, body)
	if fleet.Summary.Total != 2 || fleet.Summary.UpToDate != 1 || fleet.Summary.Outdated != 1 {
		t.Fatalf("终端汇总不对：%+v clients=%+v", fleet.Summary, fleet.Clients)
	}

	// 6. 关闭推送后 desktop_update.mode 为 off 且 releases 为空
	code, body = admin.do("PUT", "/api/admin/dsh-releases/push", map[string]any{
		"push_mode": "off",
		"push_note": "",
	}, nil)
	expect(t, "关闭推送", code, 200, body)
	delete(oldHeaders, "If-None-Match")
	code, body = c.do("GET", "/api/dsh/catalog", nil, oldHeaders)
	expect(t, "关闭推送后拉取目录", code, 200, body)
	catOff := decode[struct {
		DesktopUpdate struct {
			Mode     string         `json:"mode"`
			Releases map[string]any `json:"releases"`
		} `json:"desktop_update"`
	}](t, body)
	if catOff.DesktopUpdate.Mode != "off" || len(catOff.DesktopUpdate.Releases) != 0 {
		t.Fatalf("关闭推送后不应下发版本：%+v", catOff.DesktopUpdate)
	}
	_ = context.Background()
}
