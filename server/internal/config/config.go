// Package config 从环境变量读取服务配置。
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// 监听地址，默认只监听本机。
	Addr string
	// MySQL 协议 DSN，OceanBase（MySQL 模式）与 TiDB 通用。
	DSN string
	// 加密上游 API key 的 32 字节主密钥。
	SecretKey []byte
	// 部署在 HTTPS 后面时设为 true，会话 cookie 加 Secure。
	CookieSecure bool
	SessionTTL   time.Duration
	// 首次启动、用户表为空时创建的管理员。
	BootstrapAdmin         string
	BootstrapAdminPassword string
	// 本地数据目录：npm 包 tarball、上游元数据缓存。
	DataDir string
	// 对外访问地址（如 https://dsh.acme.internal），用于生成 tarball 下载地址；
	// 为空时按请求的 Host / X-Forwarded-* 推断。
	PublicURL string
	// 启动时等待数据库就绪的最长时间（docker compose 里数据库常比服务晚起来）。
	DBWait time.Duration
	// 管理模式「1 小时 / 8 小时」里「小时」的长度，默认 1h；只给自动化测试缩短用，生产不要设
	AdminModeUnit time.Duration
	// 数据中台的库（DSH_EE_DATA_DSN）。默认和主库同一个服务器、库名加 _data；设成 off 关闭。
	DataDSN string
	// 数据中台的只读账号（DSH_EE_DATA_READ_DSN）；不填时用 DataDSN 的账号自动建一个只有 SELECT 权限的账号。
	DataReadDSN string
	// 公开演示站（DSH_EE_DEMO_MODE）：登录页显示初始管理员账号，关掉改密码等会把别人锁在外面的操作，
	// 对外请求只许连本机。DemoReset 是给访客看的重置周期说明，如「每 6 小时」。
	DemoMode  bool
	DemoReset string
	// 下一次数据重置时间（RFC3339）；设置后登录页显示倒计时，不设则不显示。
	DemoResetAt time.Time
}

func Load() (*Config, error) {
	c := &Config{
		Addr:                   getenv("DSH_EE_ADDR", "127.0.0.1:8080"),
		DSN:                    os.Getenv("DSH_EE_DSN"),
		BootstrapAdmin:         getenv("DSH_EE_BOOTSTRAP_ADMIN", "admin"),
		BootstrapAdminPassword: os.Getenv("DSH_EE_BOOTSTRAP_ADMIN_PASSWORD"),
		SessionTTL:             7 * 24 * time.Hour,
		DataDir:                getenv("DSH_EE_DATA_DIR", "./data"),
		PublicURL:              strings.TrimRight(os.Getenv("DSH_EE_PUBLIC_URL"), "/"),
		DBWait:                 2 * time.Minute,
	}
	if c.DSN == "" {
		return nil, errors.New("缺少 DSH_EE_DSN，例如 root:@tcp(127.0.0.1:4000)/dsh_ee")
	}
	c.DataReadDSN = os.Getenv("DSH_EE_DATA_READ_DSN")
	switch v := os.Getenv("DSH_EE_DATA_DSN"); v {
	case "off":
	case "":
		d, err := mysql.ParseDSN(c.DSN)
		if err != nil {
			return nil, fmt.Errorf("DSH_EE_DSN: %w", err)
		}
		if d.DBName != "" {
			d.DBName += "_data"
			c.DataDSN = d.FormatDSN()
		}
	default:
		c.DataDSN = v
	}
	raw := os.Getenv("DSH_EE_SECRET_KEY")
	if raw == "" {
		return nil, errors.New("缺少 DSH_EE_SECRET_KEY（32 字节 base64），可用 openssl rand -base64 32 生成")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, errors.New("DSH_EE_SECRET_KEY 必须是 32 字节的 base64")
	}
	c.SecretKey = key
	if v := os.Getenv("DSH_EE_COOKIE_SECURE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("DSH_EE_COOKIE_SECURE: %w", err)
		}
		c.CookieSecure = b
	}
	if v := os.Getenv("DSH_EE_DEMO_MODE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("DSH_EE_DEMO_MODE: %w", err)
		}
		c.DemoMode = b
	}
	c.DemoReset = strings.TrimSpace(os.Getenv("DSH_EE_DEMO_RESET"))
	if v := strings.TrimSpace(os.Getenv("DSH_EE_DEMO_RESET_AT")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, errors.New("DSH_EE_DEMO_RESET_AT 必须是 RFC3339 时间，例如 2026-11-11T00:00:00+08:00")
		}
		c.DemoResetAt = t
	}
	if c.DemoMode && c.BootstrapAdminPassword == "" {
		return nil, errors.New("DSH_EE_DEMO_MODE 需要同时设置 DSH_EE_BOOTSTRAP_ADMIN_PASSWORD：登录页要把演示账号显示出来")
	}
	if v := os.Getenv("DSH_EE_SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("DSH_EE_SESSION_TTL: %w", err)
		}
		c.SessionTTL = d
	}
	if v := os.Getenv("DSH_EE_ADMIN_MODE_UNIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("DSH_EE_ADMIN_MODE_UNIT 必须是正的时间长度，例如 15s")
		}
		c.AdminModeUnit = d
	}
	if v := os.Getenv("DSH_EE_DB_WAIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("DSH_EE_DB_WAIT: %w", err)
		}
		c.DBWait = d
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("DSH_EE_PUBLIC_URL 必须是 http(s)://host[:port][/path] 形式")
		}
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
