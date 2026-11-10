package api_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hikariming/dsh-ee/server/internal/api"
	"github.com/hikariming/dsh-ee/server/internal/secret"
)

// 示例工作台：crm 连接的 lookup 是只读工具，update 是写工具（见 newFakeMCP）。
func sampleWorkbench() map[string]any {
	return map[string]any{"panes": []any{
		map[string]any{"type": "panel", "id": "main", "title": "客户工作台", "widgets": []any{
			map[string]any{"type": "metric", "title": "客户数", "source": map[string]any{"connection": "crm", "tool": "lookup", "args": map[string]any{"q": "count"}}, "field": "total", "format": "number"},
			map[string]any{"type": "table", "title": "最近客户", "source": map[string]any{"connection": "crm", "tool": "lookup"}, "rows": "items",
				"columns":     []any{map[string]any{"field": "name", "title": "名称"}},
				"row_actions": []any{map[string]any{"label": "让 AI 跟进", "prompt": "跟进客户 {{row.name}}"}}},
			map[string]any{"type": "form", "title": "新建客户",
				"fields": []any{map[string]any{"name": "name", "label": "名称", "kind": "text", "required": true}, map[string]any{"name": "level", "label": "级别", "kind": "select", "options": []string{"A", "B"}}},
				"submit": map[string]any{"label": "交给 AI", "prompt": "新建客户 {{form.name}}，级别 {{form.level}}"}},
			map[string]any{"type": "actions", "actions": []any{map[string]any{"label": "本周总结", "prompt": "总结本周新增客户"}}},
			map[string]any{"type": "markdown", "text": "先查重再新建。"},
			map[string]any{"type": "board", "title": "AI 看板"},
		}},
		map[string]any{"type": "web", "id": "crm-web", "title": "CRM 系统", "url": "https://crm.example.com/"},
	}}
}

func TestWorkbench(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := api.Bootstrap(ctx, st, "admin", "admin-password-123"); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, _ := secret.NewBox(key)
	srv := api.New(st, box, api.Options{DataDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	admin := newClient(t, ts.URL)
	code, body := admin.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "admin-password-123"}, nil)
	expect(t, "管理员登录", code, 200, body)
	code, body = admin.do("POST", "/api/admin/users", map[string]any{"username": "bob", "password": "bob-password-1", "department": "财务部"}, nil)
	expect(t, "建用户", code, 201, body)
	bobWeb := newClient(t, ts.URL)
	code, body = bobWeb.do("POST", "/api/auth/login", map[string]string{"username": "bob", "password": "bob-password-1"}, nil)
	expect(t, "登录", code, 200, body)
	code, body = bobWeb.do("POST", "/api/me/npm-tokens", map[string]any{"name": "dsh", "access": "read"}, nil)
	expect(t, "接入令牌", code, 201, body)
	bob := dshClient{t: t, base: ts.URL, token: decodeAs[struct{ Token string }](t, body).Token}

	up := newFakeMCP(t)
	connSpec := map[string]any{"title": "CRM", "description": "客户与合同", "type": "mcp", "url": up.URL, "auth": map[string]any{"type": "bearer"}}
	code, body = admin.do("POST", "/api/hub", map[string]any{"kind": "connection", "slug": "crm", "delivery": "auto", "spec": connSpec, "secret": "up-secret"}, nil)
	expect(t, "接入 CRM", code, 201, body)

	appSpec := func(wb any) map[string]any {
		return map[string]any{"title": "客户助手", "description": "管客户", "persona": "你是客户经理助手。", "starters": []string{"今天要跟进谁？"},
			"connections": []string{"crm"}, "workbench": wb}
	}
	create := func(slug string, wb any) (int, []byte) {
		return admin.do("POST", "/api/hub", map[string]any{"kind": "app", "slug": slug, "delivery": "auto", "spec": appSpec(wb)}, nil)
	}

	// ---- 校验：结构问题与数据源问题 ----
	bad := func(what string, mutate func(wb map[string]any), want string) {
		t.Helper()
		wb := sampleWorkbench()
		mutate(wb)
		code, body := create("bad-"+fmt.Sprint(len(what)), wb)
		if code != 400 || !strings.Contains(string(body), want) {
			t.Fatalf("%s：应返回 400 并提示「%s」，实际 %d %s", what, want, code, body)
		}
	}
	panel := func(wb map[string]any) map[string]any { return wb["panes"].([]any)[0].(map[string]any) }
	widget := func(wb map[string]any, i int) map[string]any { return panel(wb)["widgets"].([]any)[i].(map[string]any) }
	bad("写工具当数据源", func(wb map[string]any) { widget(wb, 0)["source"].(map[string]any)["tool"] = "update" }, "不是只读工具")
	bad("不存在的工具", func(wb map[string]any) { widget(wb, 0)["source"].(map[string]any)["tool"] = "nope" }, "里没有工具 nope")
	bad("连接不在依赖里", func(wb map[string]any) { widget(wb, 1)["source"].(map[string]any)["connection"] = "oa" }, "不在这个微应用的依赖连接里")
	bad("表单占位符引用不存在的字段", func(wb map[string]any) {
		widget(wb, 2)["submit"].(map[string]any)["prompt"] = "新建 {{form.missing}}"
	}, "{{form.missing}} 引用的字段不存在")
	bad("行按钮用表单占位符", func(wb map[string]any) {
		widget(wb, 1)["row_actions"].([]any)[0].(map[string]any)["prompt"] = "跟进 {{form.name}}"
	}, "这里不能用 {{form.name}}")
	bad("快捷按钮用占位符", func(wb map[string]any) {
		widget(wb, 3)["actions"].([]any)[0].(map[string]any)["prompt"] = "看 {{row.name}}"
	}, "快捷按钮不能用占位符")
	bad("写错的占位符", func(wb map[string]any) {
		widget(wb, 3)["actions"].([]any)[0].(map[string]any)["prompt"] = "看 {{ name }}"
	}, "占位符只能写成")
	bad("未知组件类型", func(wb map[string]any) { widget(wb, 4)["type"] = "gauge" }, "第 1 个窗格第 5 个组件：类型只能是")
	bad("网址不是 http", func(wb map[string]any) { wb["panes"].([]any)[1].(map[string]any)["url"] = "javascript:alert(1)" }, "网址必须以 http")
	bad("窗格 id 重复", func(wb map[string]any) { wb["panes"].([]any)[1].(map[string]any)["id"] = "main" }, "id main 重复了")
	bad("窗格太多", func(wb map[string]any) {
		panes := wb["panes"].([]any)
		for i := 0; i < 3; i++ {
			panes = append(panes, map[string]any{"type": "web", "id": fmt.Sprintf("w%d", i), "title": "网页", "url": "https://a.example.com"})
		}
		wb["panes"] = panes
	}, "最多 4 个窗格")
	bad("参数不是对象", func(wb map[string]any) { widget(wb, 0)["source"].(map[string]any)["args"] = []int{1} }, "必须是 JSON 对象")
	bad("下拉没有选项", func(wb map[string]any) {
		widget(wb, 2)["fields"].([]any)[1].(map[string]any)["options"] = []string{}
	}, "下拉选项要有 1～20 项")
	bad("未知字段", func(wb map[string]any) { widget(wb, 5)["html"] = "<b>x</b>" }, "spec 格式不对")

	// ---- 图表、清单、详情卡、空状态：数据源同样只能是只读工具 ----
	addWidget := func(wb map[string]any, w map[string]any) {
		panel(wb)["widgets"] = append(panel(wb)["widgets"].([]any), w)
	}
	chart := func(kind string, tool string, ys ...string) map[string]any {
		var y []any
		for _, f := range ys {
			y = append(y, map[string]any{"field": f, "title": f})
		}
		return map[string]any{"type": "chart", "title": "趋势", "chart": kind, "source": map[string]any{"connection": "crm", "tool": tool}, "rows": "items", "x": "month", "y": y}
	}
	bad("图表用写工具", func(wb map[string]any) { addWidget(wb, chart("line", "update", "amount")) }, "不是只读工具")
	bad("图表类型不对", func(wb map[string]any) { addWidget(wb, chart("radar", "lookup", "amount")) }, "图表类型（chart）只能是")
	bad("饼图两个序列", func(wb map[string]any) { addWidget(wb, chart("pie", "lookup", "a", "b")) }, "饼图只能有 1 个数值序列")
	bad("图表没有序列", func(wb map[string]any) { addWidget(wb, chart("bar", "lookup")) }, "数值序列（y）要有 1～4 个")
	bad("清单用写工具", func(wb map[string]any) {
		addWidget(wb, map[string]any{"type": "checklist", "title": "待办", "source": map[string]any{"connection": "crm", "tool": "export"}, "item_field": "name"})
	}, "不是只读工具")
	bad("清单两种条目都填", func(wb map[string]any) {
		addWidget(wb, map[string]any{"type": "checklist", "title": "待办", "source": map[string]any{"connection": "crm", "tool": "lookup"}, "item_field": "name", "items": []any{map[string]any{"id": "a", "text": "A"}}})
	}, "不能都填")
	bad("清单条目 id 重复", func(wb map[string]any) {
		addWidget(wb, map[string]any{"type": "checklist", "title": "待办", "items": []any{map[string]any{"id": "a", "text": "A"}, map[string]any{"id": "a", "text": "B"}}})
	}, "id a 重复了")
	bad("详情卡字段不对", func(wb map[string]any) {
		widget(wb, 1)["detail"] = []any{map[string]any{"field": "a b", "title": "x"}}
	}, "详情卡第 1 项的字段格式不对")
	bad("空状态按钮用占位符", func(wb map[string]any) {
		widget(wb, 1)["empty"] = map[string]any{"text": "还没有客户", "action": map[string]any{"label": "去建一个", "prompt": "新建 {{row.name}}"}}
	}, "空状态的按钮：快捷按钮不能用占位符")
	bad("分组标题没写", func(wb map[string]any) { addWidget(wb, map[string]any{"type": "heading"}) }, "分组标题必填")

	// ---- 合法配置：保存、下发、写进指令 ----
	code, body = create("crm-helper", sampleWorkbench())
	expect(t, "建带工作台的微应用", code, 201, body)
	appID := decodeAs[struct{ Asset struct{ ID int64 } }](t, body).Asset.ID

	code, raw, header := bob.do("GET", "/api/dsh/catalog", nil, nil)
	expect(t, "目录", code, 200, raw)
	var cat struct {
		Apps []struct {
			Name      string          `json:"name"`
			Workbench json.RawMessage `json:"workbench"`
		} `json:"apps"`
	}
	_ = json.Unmarshal(raw, &cat)
	var crmWorkbench json.RawMessage
	for _, app := range cat.Apps {
		if app.Name == "crm-helper" {
			crmWorkbench = app.Workbench
		}
	}
	if crmWorkbench == nil || !strings.Contains(string(crmWorkbench), `"tool":"lookup"`) || !strings.Contains(string(crmWorkbench), `"crm-web"`) {
		t.Fatalf("目录应原样带上工作台：%s", raw)
	}
	etag := header.Get("ETag")

	code, raw, _ = bob.do("GET", "/api/dsh/skills/crm-helper", nil, nil)
	expect(t, "加载微应用", code, 200, raw)
	content := decodeAs[struct{ Content string }](t, raw).Content
	for _, want := range []string{"# 客户助手\n\n你是客户经理助手。\n", "## 用户可以这样问\n\n- 今天要跟进谁？\n", "## 工作台", "「客户工作台」：客户数、最近客户、新建客户、快捷按钮、说明、AI 看板", "「CRM 系统」：内部系统网页 https://crm.example.com/", "`ee_workbench`"} {
		if !strings.Contains(content, want) {
			t.Fatalf("指令里应有「%s」：\n%s", want, content)
		}
	}
	// 旧段落不变：工作台一节只追加在最后
	if i := strings.Index(content, "\n## 工作台"); i < 0 || !strings.HasSuffix(content[:i], "- 今天要跟进谁？\n") {
		t.Fatalf("工作台一节应追加在示例问题之后：\n%s", content)
	}

	// 改工作台 → 新版本上线后 ETag 变化
	wb := sampleWorkbench()
	panel(wb)["title"] = "客户工作台 v2"
	code, body = admin.do("POST", fmt.Sprintf("/api/hub/%d/versions", appID), map[string]any{"spec": appSpec(wb)}, nil)
	expect(t, "新版本", code, 201, body)
	_, _, header = bob.do("GET", "/api/dsh/catalog", nil, nil)
	if header.Get("ETag") == etag {
		t.Fatal("工作台改了，目录 ETag 应变化")
	}

	// 新组件：分组标题、图表、清单（配置的条目与数据源）、详情卡、空状态都能保存，并写进指令
	wb2 := sampleWorkbench()
	widget(wb2, 1)["detail"] = []any{map[string]any{"field": "owner", "title": "负责人"}, map[string]any{"field": "renew_at", "title": "续约日期", "format": "date"}}
	widget(wb2, 1)["empty"] = map[string]any{"text": "还没有客户", "action": map[string]any{"label": "去建一个", "prompt": "帮我新建一个客户"}}
	widget(wb2, 0)["delta_field"] = "change"
	widget(wb2, 0)["format"] = "compact"
	addWidget(wb2, map[string]any{"type": "heading", "title": "趋势与待办"})
	addWidget(wb2, chart("line", "lookup", "amount", "count"))
	addWidget(wb2, chart("pie", "lookup", "amount"))
	addWidget(wb2, map[string]any{"type": "checklist", "title": "入职清单", "items": []any{map[string]any{"id": "laptop", "text": "领电脑"}, map[string]any{"id": "badge", "text": "办工牌"}}})
	addWidget(wb2, map[string]any{"type": "checklist", "title": "跟进清单", "source": map[string]any{"connection": "crm", "tool": "lookup"}, "rows": "items", "item_field": "name", "id_field": "id"})
	code, body = create("crm-helper-v2", wb2)
	expect(t, "新组件", code, 201, body)
	_, raw, _ = bob.do("GET", "/api/dsh/catalog", nil, nil)
	for _, want := range []string{`"type":"chart"`, `"chart":"pie"`, `"type":"checklist"`, `"id":"laptop"`, `"detail":[`, `"empty":{`, `"delta_field":"change"`, `"type":"heading"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("目录里应有 %s：%s", want, raw)
		}
	}
	_, raw, _ = bob.do("GET", "/api/dsh/skills/crm-helper-v2", nil, nil)
	content = decodeAs[struct{ Content string }](t, raw).Content
	for _, want := range []string{"AI 看板、趋势、趋势、入职清单、跟进清单", "action 为 `checklist`，title 写「入职清单」", "`laptop` 领电脑；`badge` 办工牌", "条目来自 `crm__lookup`，编号是每一项的 `id` 字段"} {
		if !strings.Contains(content, want) {
			t.Fatalf("指令里应有「%s」：\n%s", want, content)
		}
	}
	if strings.Contains(content, "趋势与待办") {
		t.Fatalf("分组标题不用写进指令：\n%s", content)
	}

	// 没有工作台的微应用：指令里不出现工作台一节
	code, body = admin.do("POST", "/api/hub", map[string]any{"kind": "app", "slug": "plain-helper", "delivery": "auto", "spec": map[string]any{"title": "普通助手", "description": "没有面板", "persona": "你是助手。"}}, nil)
	expect(t, "普通微应用", code, 201, body)
	_, raw, _ = bob.do("GET", "/api/dsh/skills/plain-helper", nil, nil)
	if strings.Contains(string(raw), "工作台") {
		t.Fatalf("没有工作台的微应用不应写工作台一节：%s", raw)
	}

	// 非微应用带 workbench 会被清掉
	code, body = admin.do("POST", "/api/hub", map[string]any{"kind": "prompt", "slug": "p1", "spec": map[string]any{"title": "提示词", "description": "d", "body": "b", "workbench": sampleWorkbench()}}, nil)
	expect(t, "提示词", code, 201, body)
	if strings.Contains(string(body), "workbench") {
		t.Fatalf("提示词不应保留工作台：%s", body)
	}
}
