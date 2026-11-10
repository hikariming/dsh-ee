package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// 公开演示站（DSH_EE_DEMO_MODE）：所有访客共用初始管理员账号，数据由 deploy/demo 的脚本定期清空重来。
// 这里只防两类事：一是有人把别人锁在外面（改密码、停用账号、改角色、接企业目录），
// 二是有人把演示站当跳板或网盘（对外请求、上传 npm 包、同步上游）。其余随便改，重置后就回来了。

// DemoSite 是演示站的配置；Server 的 opts.DemoSite 为 nil 表示正常部署。
type DemoSite struct {
	// 登录页上显示的演示账号
	Username string
	Password string
	// 给访客看的重置周期说明，如「每 6 小时」；空则不显示
	Reset string
	// 下一次重置时间；零值表示不显示倒计时
	NextResetAt time.Time
	// dsh-ee 自己监听的端口：对外请求只许连本机的这个端口（示例连接就是连自己）
	selfPort string
}

// demoSiteBlocked 是演示站里关掉的接口（按路由表里的 pattern）。没列出的照常可用：
// 建应用、知识库上传、数据中台上传 CSV、管理模式这些正是要演示的，数据都在自己库里，重置即清。
var demoSiteBlocked = map[string]bool{
	// 会把别人锁在外面
	"POST /api/auth/password":         true,
	"PATCH /api/admin/users/{id}":     true,
	"PUT /api/admin/users/{id}/roles": true,
	"PATCH /api/admin/roles/{id}":     true,
	"DELETE /api/admin/roles/{id}":    true,
	"PUT /api/admin/auth/ldap":        true,
	"POST /api/admin/auth/ldap/test":  true,
	"POST /api/admin/auth/ldap/sync":  true,
	"PUT /api/admin/auth/oidc":        true,
	"POST /api/admin/auth/oidc/test":  true,
	"PUT /api/admin/brand/logo":       true,
	// 模型接入：访客填进来的 key 会被别人拿来用
	"POST /api/admin/providers":            true,
	"PATCH /api/admin/providers/{id}":      true,
	"POST /api/admin/providers/{id}/check": true,
	// 上传文件、从外网拉东西
	"POST /api/me/packages":                    true,
	"POST /api/agent/packages":                 true,
	"POST /api/me/import-requests":             true,
	"POST /api/admin/upstream/sync":            true,
	"POST /api/admin/upstream/check-updates":   true,
	"POST /api/admin/upstream/import":          true,
	"POST /api/admin/dsh-releases/check":       true,
	"POST /api/admin/dsh-releases/{id}/mirror": true,
}

const demoSiteMsg = "演示站不开放这个操作。想完整体验，可以自己部署一套：见 deploy/README.md"

func demoSiteRefuse(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusForbidden, demoSiteMsg)
}

// demoSiteNpmWrite：npm 源在演示站只读（不许 npm publish / login）。
func (s *Server) demoSiteNpmWrite(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.DemoSite == nil || r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	demoSiteRefuse(w, r)
	return true
}

var errDemoSiteDial = errors.New("演示站不允许访问外部地址")

// guard 让对外请求只能连 dsh-ee 自己：按真正要连的 IP 判断（DNS 解析之后），代理也一样被拦。
// 正常部署（d 为 nil）原样返回。
func (d *DemoSite) guard(t *http.Transport) *http.Transport {
	if d == nil {
		return t
	}
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return errDemoSiteDial
			}
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() || (d.selfPort != "" && port != d.selfPort) {
				return fmt.Errorf("%w：%s", errDemoSiteDial, address)
			}
			return nil
		},
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}
	t.Proxy = nil
	return t
}

type demoSiteInfo struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Reset       string `json:"reset,omitempty"`
	NextResetAt string `json:"next_reset_at,omitempty"`
}

// demoSiteInfo 给登录页和页面顶部的提示条用；正常部署返回 nil。
func (s *Server) demoSiteInfo() *demoSiteInfo {
	d := s.opts.DemoSite
	if d == nil {
		return nil
	}
	info := &demoSiteInfo{Username: d.Username, Password: d.Password, Reset: d.Reset}
	if !d.NextResetAt.IsZero() {
		info.NextResetAt = d.NextResetAt.Format(time.RFC3339)
	}
	return info
}
