# 公开演示站

给大家随便玩的 dsh-ee：所有访客共用一个管理员账号（登录页上直接显示），数据定期全部清空、重新导入示例内容。
不用 docker、不用 root，普通用户就能跑：本机一个单机 TiDB（unistore）加 dsh-ee 二进制。

## 演示模式做了什么

`DSH_EE_DEMO_MODE=1`（`demo.sh` 会自动设上）时：

- 登录页显示演示账号和「一键填入」，每个页面顶上提示「公开演示站、会重置、别填真实密钥」。
- 关掉会把别人锁在外面的操作：改密码、改用户 / 角色、配 LDAP / OIDC、换 logo。
- 关掉会被滥用的操作：加模型接入（别人会拿访客的 key 用）、上传包、npm publish、同步上游、同步 DSH 官方包。
- 所有对外请求只许连 dsh-ee 自己（示例连接就是连自己），按解析后的 IP 判断；
  试连别的地址、云主机元数据接口都会被拒。
- 建应用、知识库、数据中台、管理模式这些照常可用，重置以后就清掉了。

被关掉的接口在 `server/internal/api/demo_site.go` 的 `demoSiteBlocked` 里。

## 部署

```sh
make build                      # 生成 dist/dsh-ee
deploy/demo/demo.sh init        # 生成 ~/dsh-ee-demo/demo.env，检查里面的路径
deploy/demo/demo.sh reset       # 第一次启动：建库、导入示例内容
```

数据都放在 `~/dsh-ee-demo`（可用 `DEMO_HOME` 换地方）：`tidb/` 是数据库，`data/` 是 npm 包，`logs/` 是日志，
`last-import.json` 是最近一次导入示例的结果。

定期重置和开机自启（`crontab -e`）：

```
0 */6 * * * /path/to/dsh-ee/deploy/demo/demo.sh reset >> /home/you/dsh-ee-demo/logs/reset.log 2>&1
@reboot     /path/to/dsh-ee/deploy/demo/demo.sh start >> /home/you/dsh-ee-demo/logs/reset.log 2>&1
```

`demo.sh` 启动时会按 `demo.env` 里的 `DEMO_RESET_HOURS`（默认 6）算出下一次重置时间，登录页据此显示「距离下次重置还有多久（几点）」。
改 crontab 的重置周期时，把 `DEMO_RESET_HOURS` 和显示文案 `DSH_EE_DEMO_RESET` 一起改。

## 对外访问

### 用 Cloudflare Tunnel（推荐，不用开端口、不用配证书）

```sh
cloudflared tunnel login                                   # 浏览器里授权，选域名
cloudflared tunnel create dsh-ee-demo                      # 记下打印出来的 tunnel id
cloudflared tunnel route dns dsh-ee-demo demo.example.com  # 自动加 CNAME
```

写 `~/dsh-ee-demo/cloudflared.yml`：

```yaml
tunnel: <tunnel id>
credentials-file: /home/you/.cloudflared/<tunnel id>.json
ingress:
  - hostname: demo.example.com
    service: http://127.0.0.1:8095
  - service: http_status:404
```

在 `demo.env` 里设 `CF_TUNNEL_CONFIG=/home/you/dsh-ee-demo/cloudflared.yml`、
`DSH_EE_PUBLIC_URL=https://demo.example.com`、`DSH_EE_COOKIE_SECURE=true`，再 `demo.sh reset`。
之后 `demo.sh start / stop` 会一起起停 cloudflared，`reset` 不会断开 tunnel（重置那十几秒访客看到 502）。

### 用 nginx

dsh-ee 只监听 `127.0.0.1:8095`，前面用 nginx 反代并加 HTTPS（宝塔面板：网站 → 添加站点 → 反向代理到
`http://127.0.0.1:8095`，申请证书）。流式回答要关掉缓冲：

```nginx
location / {
    proxy_pass http://127.0.0.1:8095;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_buffering off;
    proxy_read_timeout 600s;
    client_max_body_size 50m;
}
```

配好以后在 `demo.env` 里设 `DSH_EE_PUBLIC_URL=https://你的域名`、`DSH_EE_COOKIE_SECURE=true`，再 `demo.sh reset`。

## 资源

8G 内存的机器足够：刚重置完 TiDB 约 500M、dsh-ee 约 20M。`TIDB_MEMORY_LIMIT`（默认 1536MB）给 TiDB 设上限，
需要本机有 `mysql` 客户端（宝塔自带的 `/www/server/mysql/bin/mysql` 也行）；没有就只靠定期重置兜底。
