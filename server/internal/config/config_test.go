package config

import (
	"encoding/base64"
	"testing"
	"time"
)

func demoEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DSH_EE_DSN", "root:@tcp(127.0.0.1:4000)/dsh_ee")
	t.Setenv("DSH_EE_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("DSH_EE_DEMO_MODE", "true")
	t.Setenv("DSH_EE_BOOTSTRAP_ADMIN_PASSWORD", "demo-password-123")
	t.Setenv("DSH_EE_DEMO_RESET_AT", "")
}

// 登录页倒计时用：DSH_EE_DEMO_RESET_AT 按 RFC3339 解析并保留时区。
func TestLoadDemoResetAt(t *testing.T) {
	demoEnv(t)
	t.Setenv("DSH_EE_DEMO_RESET_AT", "2026-11-11T00:00:00+08:00")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.DemoResetAt.Format(time.RFC3339); got != "2026-11-11T00:00:00+08:00" {
		t.Fatalf("DemoResetAt = %q", got)
	}
}

func TestLoadDemoResetAtInvalid(t *testing.T) {
	demoEnv(t)
	t.Setenv("DSH_EE_DEMO_RESET_AT", "明天")
	if _, err := Load(); err == nil {
		t.Fatal("非法 DSH_EE_DEMO_RESET_AT 应该报错")
	}
}

// 不设 DSH_EE_DEMO_RESET_AT 时保持零值，登录页不显示倒计时。
func TestLoadDemoResetAtOptional(t *testing.T) {
	demoEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DemoResetAt.IsZero() {
		t.Fatalf("未设置时 DemoResetAt 应为零值，得到 %v", cfg.DemoResetAt)
	}
}
