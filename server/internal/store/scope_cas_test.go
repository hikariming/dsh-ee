package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

// 授权范围是整段覆盖的：管理端用「打开页面时的旧快照」保存会把期间新审批进来的
// 授权悄悄删掉。这里验证乐观并发（updated_at CAS）与范围申请通过时的合并语义。

func scopeTestAsset(t *testing.T, st *Store, slug string, sc Scope) *Asset {
	t.Helper()
	a := &Asset{Kind: KindApp, Slug: slug, Title: slug, Scope: sc, Delivery: DeliveryOptional}
	if err := st.CreateAsset(context.Background(), a, json.RawMessage(`{"title":"`+slug+`","persona":"p"}`), StatusPublished, 0); err != nil {
		t.Fatalf("建资产：%v", err)
	}
	return a
}

// sleepTurn 保证下一次 now() 写在不同的毫秒上（updated_at 列是 DATETIME(3)）。
func sleepTurn() { time.Sleep(2 * time.Millisecond) }

func TestAssetScopeCAS(t *testing.T) {
	st := openTokenTestStore(t)
	ctx := context.Background()
	a := scopeTestAsset(t, st, "cas-app", Scope{Users: []int64{1}})
	t0 := a.UpdatedAt

	sleepTurn()
	sc2 := Scope{Users: []int64{1, 2}}
	if err := st.UpdateAsset(ctx, a.ID, AssetPatch{Scope: &sc2, ExpectedUpdatedAt: &t0}); err != nil {
		t.Fatalf("用最新 updated_at 保存应成功：%v", err)
	}
	cur, err := st.GetAsset(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	t1 := cur.UpdatedAt
	if !slices.Equal(cur.Scope.Users, []int64{1, 2}) || t1.UnixMilli() == t0.UnixMilli() {
		t.Fatalf("保存后范围/版本不对：%+v %v", cur.Scope, cur.UpdatedAt)
	}

	// 拿旧快照（t0）再保存：必须被挡下，期间新加的授权与本次改动都不能落库
	sc3 := Scope{Users: []int64{1, 2, 3}}
	if err := st.UpdateAsset(ctx, a.ID, AssetPatch{Scope: &sc3, ExpectedUpdatedAt: &t0}); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧 updated_at 应返回 ErrConflict，得到 %v", err)
	}
	cur, _ = st.GetAsset(ctx, a.ID)
	if !slices.Equal(cur.Scope.Users, []int64{1, 2}) || cur.UpdatedAt.UnixMilli() != t1.UnixMilli() {
		t.Fatalf("冲突时不应写库：%+v %v", cur.Scope, cur.UpdatedAt)
	}

	sleepTurn()
	if err := st.UpdateAsset(ctx, a.ID, AssetPatch{Scope: &sc3, ExpectedUpdatedAt: &t1}); err != nil {
		t.Fatalf("刷新后重试应成功：%v", err)
	}
	cur, _ = st.GetAsset(ctx, a.ID)
	if !slices.Equal(cur.Scope.Users, []int64{1, 2, 3}) {
		t.Fatalf("重试后范围不对：%+v", cur.Scope)
	}

	// 记录不存在：ErrNotFound，而不是冲突
	missing := t1
	if err := st.UpdateAsset(ctx, 1<<40, AssetPatch{Scope: &sc2, ExpectedUpdatedAt: &missing}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的资产应返回 ErrNotFound，得到 %v", err)
	}
}

// 管理端拿旧快照保存不能丢掉这期间新审批进来的用户。
func TestAssetScopeCASKeepsNewGrant(t *testing.T) {
	st := openTokenTestStore(t)
	ctx := context.Background()
	a := scopeTestAsset(t, st, "grant-app", Scope{Users: []int64{1}})
	snapshot := a.UpdatedAt // 页面打开时读到的旧快照

	// 期间别人审批了一条「申请使用」，把用户 9 加进范围
	sleepTurn()
	granted := Scope{Users: []int64{1, 9}}
	if err := st.UpdateAsset(ctx, a.ID, AssetPatch{Scope: &granted, ExpectedUpdatedAt: &snapshot}); err != nil {
		t.Fatalf("追加授权：%v", err)
	}

	// 管理员用旧快照保存（只有 1、2）：应 409，用户 9 还在
	stale := Scope{Users: []int64{1, 2}}
	if err := st.UpdateAsset(ctx, a.ID, AssetPatch{Scope: &stale, ExpectedUpdatedAt: &snapshot}); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧快照保存应返回 ErrConflict，得到 %v", err)
	}
	cur, err := st.GetAsset(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cur.Scope.Users, 9) {
		t.Fatalf("新授权被旧快照覆盖了：%+v", cur.Scope.Users)
	}

	// 刷新后基于当前范围重新保存：9 与 2 都在
	sleepTurn()
	merged := Scope{Users: []int64{1, 9, 2}}
	token := cur.UpdatedAt
	if err := st.UpdateAsset(ctx, a.ID, AssetPatch{Scope: &merged, ExpectedUpdatedAt: &token}); err != nil {
		t.Fatalf("刷新后保存：%v", err)
	}
	cur, _ = st.GetAsset(ctx, a.ID)
	if !slices.Equal(cur.Scope.Users, []int64{1, 9, 2}) {
		t.Fatalf("新授权没有保留：%+v", cur.Scope.Users)
	}
}

// 通过范围申请时不能把 pending_scope 快照整段写回：申请之后新加的授权要保留。
func TestScopeResolveAssetMergesNewGrants(t *testing.T) {
	st := openTokenTestStore(t)
	ctx := context.Background()
	a := scopeTestAsset(t, st, "resolve-app", Scope{Users: []int64{1}})

	// 负责人申请扩大到「用户 1、2 + 研发部」
	pending := Scope{Users: []int64{1, 2}, Departments: []string{"研发效能部"}}
	if err := st.RequestAssetScope(ctx, a.ID, pending, nil); err != nil {
		t.Fatalf("申请范围：%v", err)
	}
	// 申请之后又有人被加进范围（例如审批了一条「申请使用」）
	granted := Scope{Users: []int64{1, 9}}
	if err := st.UpdateAsset(ctx, a.ID, AssetPatch{Scope: &granted}); err != nil {
		t.Fatalf("追加授权：%v", err)
	}

	ok, err := st.ResolveAssetScope(ctx, a.ID, true, "")
	if err != nil || !ok {
		t.Fatalf("通过范围申请：%v %v", ok, err)
	}
	cur, err := st.GetAsset(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cur.Scope.Users, 9) || !slices.Contains(cur.Scope.Users, 2) ||
		!slices.Contains(cur.Scope.Departments, "研发效能部") || cur.PendingScope != nil {
		t.Fatalf("通过后应合并快照与当前授权：%+v pending=%v", cur.Scope, cur.PendingScope)
	}
	// 没有待审申请时返回 false
	if ok, err := st.ResolveAssetScope(ctx, a.ID, true, ""); err != nil || ok {
		t.Fatalf("没有在审申请：%v %v", ok, err)
	}
}

// 申请之后管理员已放开全员：通过旧申请不能把范围收回成快照里的窄范围。
func TestScopeResolveAssetKeepsAll(t *testing.T) {
	st := openTokenTestStore(t)
	ctx := context.Background()
	a := scopeTestAsset(t, st, "resolve-all", Scope{Users: []int64{1}})
	if err := st.RequestAssetScope(ctx, a.ID, Scope{Users: []int64{1, 2}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAsset(ctx, a.ID, AssetPatch{Scope: &Scope{All: true}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ResolveAssetScope(ctx, a.ID, true, ""); err != nil || !ok {
		t.Fatalf("通过范围申请：%v %v", ok, err)
	}
	cur, _ := st.GetAsset(ctx, a.ID)
	if !cur.Scope.All {
		t.Fatalf("期间已放开全员，通过旧申请不应收窄：%+v", cur.Scope)
	}
}

func TestScopeResolveAssetReject(t *testing.T) {
	st := openTokenTestStore(t)
	ctx := context.Background()
	a := scopeTestAsset(t, st, "resolve-reject", Scope{Users: []int64{1}})
	if err := st.RequestAssetScope(ctx, a.ID, Scope{All: true}, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ResolveAssetScope(ctx, a.ID, false, "先在部门里试一个月"); err != nil || !ok {
		t.Fatalf("驳回范围申请：%v %v", ok, err)
	}
	cur, _ := st.GetAsset(ctx, a.ID)
	if cur.Scope.All || cur.PendingScope != nil || cur.ScopeNote != "先在部门里试一个月" {
		t.Fatalf("驳回后范围不变、带原因：%+v", cur)
	}
}

func TestKBaseScopeCAS(t *testing.T) {
	st := openTokenTestStore(t)
	ctx := context.Background()
	b := &KBase{Slug: "cas-kb", Title: "CAS", Scope: Scope{Users: []int64{1}}}
	if err := st.CreateKBase(ctx, b, 0); err != nil {
		t.Fatalf("建库：%v", err)
	}
	t0 := b.UpdatedAt

	sleepTurn()
	sc2 := Scope{Users: []int64{1, 2}}
	if err := st.UpdateKBase(ctx, b.ID, KBasePatch{Scope: &sc2, ExpectedUpdatedAt: &t0}); err != nil {
		t.Fatalf("用最新 updated_at 保存：%v", err)
	}
	cur, err := st.GetKBase(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	t1 := cur.UpdatedAt

	sc3 := Scope{Users: []int64{1, 2, 3}}
	if err := st.UpdateKBase(ctx, b.ID, KBasePatch{Scope: &sc3, ExpectedUpdatedAt: &t0}); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧 updated_at 应 ErrConflict，得到 %v", err)
	}
	if !slices.Equal(cur.Scope.Users, []int64{1, 2}) {
		t.Fatalf("冲突时不应写库：%+v", cur.Scope)
	}
	if err := st.UpdateKBase(ctx, b.ID, KBasePatch{Scope: &sc3, ExpectedUpdatedAt: &t1}); err != nil {
		t.Fatalf("刷新后重试：%v", err)
	}
}

func TestScopePluginPolicyCAS(t *testing.T) {
	st := openTokenTestStore(t)
	ctx := context.Background()
	p := PluginPolicy{Name: "dsh-cas", Scope: Scope{Users: []int64{1}}, Delivery: DeliveryAuto}
	if err := st.SetPluginPolicy(ctx, &p, 0, nil); err != nil {
		t.Fatalf("首次设置策略：%v", err)
	}
	t0 := p.UpdatedAt
	// 以为还没有记录、其实已经有了：不能覆盖别人的策略
	p2 := PluginPolicy{Name: "dsh-cas", Scope: Scope{All: true}, Delivery: DeliveryOptional}
	if err := st.SetPluginPolicy(ctx, &p2, 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("已有记录时新建应 ErrConflict，得到 %v", err)
	}

	sleepTurn()
	p3 := PluginPolicy{Name: "dsh-cas", Scope: Scope{Users: []int64{1, 2}}, Delivery: DeliveryAuto}
	if err := st.SetPluginPolicy(ctx, &p3, 0, &t0); err != nil {
		t.Fatalf("用最新 updated_at 更新：%v", err)
	}
	t1 := p3.UpdatedAt
	// 旧快照再保存 → 冲突
	p4 := PluginPolicy{Name: "dsh-cas", Scope: Scope{All: true}, Delivery: DeliveryOptional}
	if err := st.SetPluginPolicy(ctx, &p4, 0, &t0); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧 updated_at 应 ErrConflict，得到 %v", err)
	}
	policies, err := st.PluginPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := policies["dsh-cas"]; !slices.Equal(got.Scope.Users, []int64{1, 2}) || got.UpdatedAt.UnixMilli() != t1.UnixMilli() {
		t.Fatalf("冲突后策略应保持原样：%+v", got)
	}
	if err := st.SetPluginPolicy(ctx, &p4, 0, &t1); err != nil {
		t.Fatalf("刷新后重试：%v", err)
	}
}

func TestScopeDataSetCAS(t *testing.T) {
	st := openTokenTestStore(t)
	ctx := context.Background()
	d := &DataSet{Slug: "cas-ds", Title: "CAS", SQL: "SELECT 1", Scope: Scope{Users: []int64{1}}}
	if err := st.CreateDataSet(ctx, d, 0); err != nil {
		t.Fatalf("建数据集：%v", err)
	}
	t0 := d.UpdatedAt

	sleepTurn()
	upd := *d
	upd.Scope = Scope{Users: []int64{1, 2}}
	if err := st.UpdateDataSet(ctx, &upd, 0, &t0); err != nil {
		t.Fatalf("用最新 updated_at 保存：%v", err)
	}
	cur, err := st.GetDataSet(ctx, d.Slug)
	if err != nil {
		t.Fatal(err)
	}
	t1 := cur.UpdatedAt

	stale := *cur
	stale.Scope = Scope{All: true}
	if err := st.UpdateDataSet(ctx, &stale, 0, &t0); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧 updated_at 应 ErrConflict，得到 %v", err)
	}
	cur, _ = st.GetDataSet(ctx, d.Slug)
	if cur.Scope.All || !slices.Equal(cur.Scope.Users, []int64{1, 2}) {
		t.Fatalf("冲突时不应写库：%+v", cur.Scope)
	}
	if err := st.UpdateDataSet(ctx, &stale, 0, &t1); err != nil {
		t.Fatalf("刷新后重试：%v", err)
	}
}
