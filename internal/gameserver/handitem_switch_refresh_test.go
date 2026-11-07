package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/tunnel"
	"rf4server/internal/wiregen"
)

// handitemPushReader 按到达顺序把隧道里的推送信封送进通道。
func handitemPushReader(t *testing.T, peer *tunnel.Conn) <-chan *proto.Envelope {
	t.Helper()
	out := make(chan *proto.Envelope, 64)
	go func() {
		defer close(out)
		for {
			kind, _, payload, err := peer.ReadRecord()
			if err != nil {
				return
			}
			if kind != tunnel.TypeData {
				continue
			}
			env, ok := proto.ParseEnvelope(payload)
			if !ok {
				continue
			}
			out <- env
		}
	}()
	return out
}

// pushGuids 解出推送 body 里出现的全部物品 GUID。
func pushGuids(t *testing.T, srv *Server, env *proto.Envelope) []string {
	t.Helper()
	dec, _, err := proto.DecodeValue(env.Body, 0, srv.Table())
	if err != nil {
		t.Fatalf("%d/%d 推送解码失败: %v", env.Main, env.Sub, err)
	}
	return deepGUIDs(dec)
}

func pushHasGUID(list []string, g string) bool {
	for _, v := range list {
		if v == g {
			return true
		}
	}
	return false
}

// nextPush 等待下一条推送，超时即失败（推送丢失＝客户端永远看不到）。
func nextPush(t *testing.T, ch <-chan *proto.Envelope) *proto.Envelope {
	t.Helper()
	select {
	case env, ok := <-ch:
		if !ok {
			t.Fatal("推送通道已关闭，没等到预期推送")
		}
		return env
	case <-time.After(3 * time.Second):
		t.Fatal("3 秒内没等到推送")
	}
	return nil
}

func handitemCompCtx(t *testing.T, sess *Session, parentGUID, itemGUID string) *Ctx {
	t.Helper()
	return &Ctx{
		Sess: sess, Main: 13, Sub: 1,
		Tree:           rigRequestTree(t, proto.Guid(parentGUID), proto.Guid(itemGUID)),
		DecodeOK:       true,
		ExpectResponse: true,
		Log:            logging.Discard(),
	}
}

// TestHanditemSwitchReAddsOldAmmoToBackpack 锁定实机发现的换弹问题：
// 弹药格里已有 A 时直接装 B，旧弹药 A 只作为 f1 更新回传时客户端不会动，
// 弹药格里仍留着 A（看起来「切换无效」）。必须回包后 4/21 移除 + 4/22 重加。
func TestHanditemSwitchReAddsOldAmmoToBackpack(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := t.Context()
	sess, peer := newTunnelProbe(t, srv)

	hand, err := srv.CreateItem(ctx, 1000001, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建手持物失败: %v", err)
	}
	oldStack, err := srv.CreateItem(ctx, 1000001, "feedcompleted", "backpack", 2)
	if err != nil {
		t.Fatalf("创建旧窝料堆失败: %v", err)
	}
	srv.setStackCount(ctx, oldStack, 5)
	newStack, err := srv.CreateItem(ctx, 1000001, "feedcompleted", "backpack", 3)
	if err != nil {
		t.Fatalf("创建新窝料堆失败: %v", err)
	}
	srv.setStackCount(ctx, newStack, 7)

	pushes := handitemPushReader(t, peer)

	// 第一次装填：弹药格是空的，不该注册「移除+重加」。
	hctx := handitemCompCtx(t, sess, hand.GUID, oldStack.GUID)
	if body, ok := hHanditemSetComp(hctx); !ok || len(body) == 0 {
		t.Fatalf("13/1 第一次装填未处理: ok=%v body=%d", ok, len(body))
	}
	if len(hctx.afterResponse) != 0 {
		t.Fatalf("首次装填没有旧弹药，不应安排移除+重加，实得 %d", len(hctx.afterResponse))
	}
	loadedOld := srv.handitemLoadedGuid(ctx, hand.GUID)
	if loadedOld == "" {
		t.Fatal("第一次装填后弹药格仍是空的")
	}

	// 第二次装填：A 必须退回背包，并且回包后先发 4/21 再发 4/22。
	hctx2 := handitemCompCtx(t, sess, hand.GUID, newStack.GUID)
	if body, ok := hHanditemSetComp(hctx2); !ok || len(body) == 0 {
		t.Fatalf("13/1 换弹未处理: ok=%v body=%d", ok, len(body))
	}
	if len(hctx2.afterResponse) != 1 {
		t.Fatalf("换弹应安排一次回包后的移除+重加，实得 %d", len(hctx2.afterResponse))
	}
	if row, _ := db.GetItemByGUID(ctx, loadedOld); row == nil || row.Container != "backpack" {
		t.Fatalf("旧弹药应已退回背包，实得 %+v", row)
	}
	if row, _ := db.GetItemByGUID(ctx, srv.handitemLoadedGuid(ctx, hand.GUID)); row == nil || row.Container != "rig" {
		t.Fatalf("新弹药应挂在手持物上，实得 %+v", row)
	}

	hctx2.afterResponse[0]()

	// 处理器自己的推送在前；一旦出现携带旧弹药的 4/21，紧随其后的必须
	// 是把它重新加回背包的 4/22。顺序颠倒客户端会当成「加了又删」。
	for {
		env := nextPush(t, pushes)
		if env.Main != 4 || env.Sub != 21 {
			continue
		}
		if !pushHasGUID(pushGuids(t, srv, env), loadedOld) {
			continue
		}
		next := nextPush(t, pushes)
		if next.Main != 4 || next.Sub != 22 {
			t.Fatalf("4/21 移除旧弹药后必须紧跟 4/22 重加，实得 %d/%d", next.Main, next.Sub)
		}
		if !pushHasGUID(pushGuids(t, srv, next), loadedOld) {
			t.Fatalf("4/22 必须把旧弹药 %s 重新加回背包，实得 %v", shortGUID(loadedOld), pushGuids(t, srv, next))
		}
		break
	}
}

// TestHanditemSwitchRefreshCanBeDisabled 锁定 tunable 关得掉（客户端行为若变差可回退）。
func TestHanditemSwitchRefreshCanBeDisabled(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, srv, "handitem_switch_refresh", "0")
	sess, _ := newTunnelProbe(t, srv)
	hand, err := srv.CreateItem(ctx, 1000001, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	a, err := srv.CreateItem(ctx, 1000001, "feedcompleted", "backpack", 2)
	if err != nil {
		t.Fatal(err)
	}
	b, err := srv.CreateItem(ctx, 1000001, "feedcompleted", "backpack", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hHanditemSetComp(handitemCompCtx(t, sess, hand.GUID, a.GUID)); !ok {
		t.Fatal("第一次装填未处理")
	}
	hctx := handitemCompCtx(t, sess, hand.GUID, b.GUID)
	if _, ok := hHanditemSetComp(hctx); !ok {
		t.Fatal("换弹未处理")
	}
	if len(hctx.afterResponse) != 0 {
		t.Fatalf("开关关闭时不应安排移除+重加，实得 %d", len(hctx.afterResponse))
	}
}

// TestHanditemThrowSelfcleanRemovesProjectileInstance 锁定一次性抛出物的清理开关：
// handitem_throw_selfclean=1 时，f4 下发的新实例 GUID 必须同时进 f5 移除列表，
// 否则客户端背包里会攒出没人管的窝料（2026-11-07 实测「多出两份」的头号嫌疑）。
func TestHanditemThrowSelfcleanRemovesProjectileInstance(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, srv, "handitem_throw_selfclean", "1")
	// 这两个测试都要 f4 里有抛出物才能验证清理开关，所以显式打开发射开关；
	// 出厂缺省已经是 0（客户端自己会画抛投）。
	setWeatherTestTunable(t, srv, "handitem_throw_projectile", "1")

	hand, err := srv.CreateItem(ctx, 1000001, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建手持物失败: %v", err)
	}
	stack, err := srv.CreateItem(ctx, 1000001, "worm_01", "rig", 1)
	if err != nil {
		t.Fatalf("创建装填物失败: %v", err)
	}
	srv.setStackCount(ctx, stack, 3)
	srv.setHanditemLoadedRef(ctx, hand.GUID, stack.GUID)

	req := rigRequestTree(t, proto.Guid(hand.GUID),
		wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(12)}))
	body, ok := hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	proj := dec.Field(4).Val
	if proj == nil || proj.ArgCount() != 1 {
		t.Fatalf("f4 应下发 1 个抛出物，实得 %#v", proj)
	}
	pj := proj.ArgItem(0)
	gi := fieldIdxByCS(pj.Class, csGUID)
	if gi < 0 {
		t.Fatalf("抛出物类 %s 没有 GUID 槽", pj.Class)
	}
	pjField := pj.Field(gi)
	thrownGUID := pjField.GuidHex()
	if thrownGUID == "" || thrownGUID == stack.GUID {
		t.Fatalf("抛出物必须是新实例 GUID，实得 %q", thrownGUID)
	}
	removed := dec.Field(5).Val
	if removed == nil || removed.ArgCount() != 1 || removed.ArgItem(0).GuidHex() != thrownGUID {
		t.Fatalf("selfclean 开启时 f5 应移除抛出物 %s，实得 %#v", shortGUID(thrownGUID), removed)
	}
	if left, _ := db.GetItemByGUID(ctx, stack.GUID); left == nil || srv.stackCountOf(ctx, left) != 2 {
		t.Fatalf("抛出物自清不能影响弹药扣减：应剩 2 份，实得 %+v", left)
	}
	if reason := srv.dispatch.checkRegisteredResponse(13, 5, body, srv.Table()); reason != "" {
		t.Fatalf("13/5 合同守卫误拒: %s", reason)
	}
}

// TestHanditemThrowKeepsProjectileWhenSelfcleanOff 锁定缺省行为不变：
// 开关关着时抛出物不进 f5（实机实测「饵能飞出去」依赖这个形状）。
func TestHanditemThrowKeepsProjectileWhenSelfcleanOff(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := t.Context()
	setWeatherTestTunable(t, srv, "handitem_throw_selfclean", "0")
	// 这两个测试都要 f4 里有抛出物才能验证清理开关，所以显式打开发射开关；
	// 出厂缺省已经是 0（客户端自己会画抛投）。
	setWeatherTestTunable(t, srv, "handitem_throw_projectile", "1")

	hand, err := srv.CreateItem(ctx, 1000001, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := srv.CreateItem(ctx, 1000001, "worm_01", "rig", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.setStackCount(ctx, stack, 3)
	srv.setHanditemLoadedRef(ctx, hand.GUID, stack.GUID)

	req := rigRequestTree(t, proto.Guid(hand.GUID),
		wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(12)}))
	body, ok := hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if removed := dec.Field(5).Val; removed != nil && removed.ArgCount() > 0 {
		t.Fatalf("开关关闭时 f5 应为空，实得 %#v", removed)
	}
	if proj := dec.Field(4).Val; proj == nil || proj.ArgCount() != 1 {
		t.Fatalf("开关关闭时 f4 仍要下发抛出物，实得 %#v", proj)
	}
}
