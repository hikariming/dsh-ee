package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 演示站的对外请求只能连 dsh-ee 自己监听的端口；正常部署不受影响。
func TestDemoSiteGuard(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	_, port, _ := net.SplitHostPort(u.Host)

	get := func(d *DemoSite) error {
		c := &http.Client{Transport: d.guard(&http.Transport{Proxy: http.ProxyFromEnvironment})}
		resp, err := c.Get(ts.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(nil); err != nil {
		t.Fatalf("正常部署不应拦：%v", err)
	}
	if err := get(&DemoSite{selfPort: port}); err != nil {
		t.Fatalf("连自己应放行：%v", err)
	}
	if err := get(&DemoSite{selfPort: "1"}); !errors.Is(err, errDemoSiteDial) {
		t.Fatalf("本机别的端口应被拦：%v", err)
	}
	c := &http.Client{Transport: (&DemoSite{selfPort: port}).guard(&http.Transport{})}
	if _, err := c.Get("http://169.254.169.254/latest/meta-data/"); !errors.Is(err, errDemoSiteDial) {
		t.Fatalf("外部地址应被拦：%v", err)
	}
}

// 拦截表里的每一项都要真的是路由表里的接口，免得改了路由以后悄悄失效。
func TestDemoSiteBlockedRoutesExist(t *testing.T) {
	s := &Server{}
	have := map[string]bool{}
	for _, r := range s.routes() {
		have[r.pattern] = true
	}
	for p := range demoSiteBlocked {
		if !have[p] {
			t.Errorf("demoSiteBlocked 里的 %q 不在路由表里", p)
		}
	}
}

// 登录页倒计时：只有配置了下次重置时间才下发 next_reset_at；正常部署不出现。
func TestDemoSiteInfoNextReset(t *testing.T) {
	if info := (&Server{}).demoSiteInfo(); info != nil {
		t.Fatalf("正常部署不应有演示站信息：%+v", info)
	}
	s := &Server{opts: Options{DemoSite: &DemoSite{Username: "u", Password: "p", Reset: "每 6 小时"}}}
	body, err := json.Marshal(s.demoSiteInfo())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "next_reset_at") {
		t.Fatalf("未配置下次重置时间时不应下发 next_reset_at：%s", body)
	}
	s.opts.DemoSite.NextResetAt = time.Date(2026, 11, 11, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	body, err = json.Marshal(s.demoSiteInfo())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"next_reset_at":"2026-11-11T00:00:00+08:00"`) {
		t.Fatalf("应下发下次重置时间：%s", body)
	}
}
