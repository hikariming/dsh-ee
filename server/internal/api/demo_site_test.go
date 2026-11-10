package api_test

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hikariming/dsh-ee/server/internal/api"
	"github.com/hikariming/dsh-ee/server/internal/secret"
)

// 演示站：登录页给出账号；会把别人锁在外面的操作被拒；对外请求只能连自己；一键导入示例照常可用。
func TestDemoSite(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := api.Bootstrap(ctx, st, "admin", "admin-password-123"); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, _ := secret.NewBox(key)
	ts := httptest.NewUnstartedServer(nil)
	srv := api.New(st, box, api.Options{
		DataDir:  t.TempDir(),
		Addr:     ts.Listener.Addr().String(),
		DemoSite: &api.DemoSite{Username: "admin", Password: "admin-password-123", Reset: "每 6 小时", NextResetAt: time.Date(2026, 11, 11, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts.Config.Handler = srv.Handler()
	ts.Start()
	defer ts.Close()

	anon := newClient(t, ts.URL)
	code, body := anon.do("GET", "/api/auth/methods", nil, nil)
	expect(t, "登录方式", code, 200, body)
	demo := decodeAs[struct {
		Demo *struct {
			Username    string `json:"username"`
			Password    string `json:"password"`
			Reset       string `json:"reset"`
			NextResetAt string `json:"next_reset_at"`
		}
	}](t, body).Demo
	if demo == nil || demo.Username != "admin" || demo.Password != "admin-password-123" || demo.Reset != "每 6 小时" {
		t.Fatalf("登录页应给出演示账号：%s", body)
	}
	if demo.NextResetAt != "2026-11-11T00:00:00+08:00" {
		t.Fatalf("登录页应给出下次重置时间：%s", body)
	}

	admin := newClient(t, ts.URL)
	code, body = admin.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "admin-password-123"}, nil)
	expect(t, "管理员登录", code, 200, body)

	code, body = admin.do("POST", "/api/auth/password", map[string]string{"old_password": "admin-password-123", "new_password": "something-else-456"}, nil)
	expect(t, "演示站改密码", code, 403, body)
	if !strings.Contains(string(body), "演示站") {
		t.Fatalf("应说明是演示站的限制：%s", body)
	}
	code, body = admin.do("POST", "/api/admin/providers", map[string]any{"name": "x", "base_url": "https://api.example.com/v1", "api_key": "sk-x"}, nil)
	expect(t, "演示站加模型接入", code, 403, body)
	code, body = anon.do("PUT", "/npm/some-pkg", map[string]any{}, nil)
	expect(t, "演示站 npm 发布", code, 403, body)

	// 示例连接从服务端访问自己，演示站里也要能建成
	code, body = admin.do("POST", "/api/admin/demo", nil, nil)
	expect(t, "导入示例", code, 200, body)
	for _, it := range decodeAs[struct {
		Items []struct{ Kind, Slug, Result, Error string }
	}](t, body).Items {
		if it.Result != "created" {
			t.Fatalf("%s %s 没建成：%s %s", it.Kind, it.Slug, it.Result, it.Error)
		}
	}

	// 试连别的地址：哪怕同在本机、只是端口不同，也被拦在拨号这一步
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("演示站不应访问到外部地址")
	}))
	defer other.Close()
	spec := map[string]any{"title": "X", "description": "x", "type": "mcp", "url": other.URL, "auth": map[string]any{"type": "bearer"}}
	code, body = admin.do("POST", "/api/admin/connections/test", map[string]any{"spec": spec, "secret": "s"}, nil)
	expect(t, "试连外部地址", code, 200, body)
	if !strings.Contains(string(body), "演示站不允许访问外部地址") {
		t.Fatalf("应说明演示站不许访问外部地址：%s", body)
	}
}
