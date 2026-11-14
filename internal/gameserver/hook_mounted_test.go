package gameserver

import (
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// hookFlagsOut 读钩对象 hgaijieklek 当前值。
func hookFlagsOut(t *testing.T, hook *proto.Value) uint64 {
	t.Helper()
	idx := fieldIdxByCS("agedjipcnpe", csStateEnum)
	if idx < 0 || idx >= len(hook.Fields) || hook.Fields[idx].Num == nil {
		t.Fatalf("钩对象缺少 %s 字段", csStateEnum)
	}
	return *hook.Fields[idx].Num
}

// markHookMounted 补挂竿位：官方四钓法抓包口径（钩 20992 | 1024 = 22016），
// 缺位时客户端钩实体不进搏鱼态。裸钩与容器嵌套钩两种推送形状都必须补齐。
func TestMarkHookMountedBareHook(t *testing.T) {
	hook := wiregen.MustMakeObj("agedjipcnpe", map[int]*proto.Value{
		fieldIdxByCS("agedjipcnpe", csStateEnum): proto.U32(20992),
	})
	markHookMounted(hook)
	if got := hookFlagsOut(t, hook); got != 22016 {
		t.Fatalf("裸钩 flags=%d，期望 22016（20992|1024）", got)
	}
}

func TestMarkHookMountedNestedRig(t *testing.T) {
	// 路亚/海钓串钩形状：ioombppcnlh 容器 f0 挂 ARGS(60)[钩...]，钩在树深处。
	inner := wiregen.MustMakeObj("agedjipcnpe", map[int]*proto.Value{
		fieldIdxByCS("agedjipcnpe", csStateEnum): proto.U32(20992),
	})
	container := wiregen.MustMakeObj("ioombppcnlh", map[int]*proto.Value{
		0: proto.Args(proto.S("60"), inner),
	})
	markHookMounted(container)
	if got := hookFlagsOut(t, inner); got != 22016 {
		t.Fatalf("嵌套钩 flags=%d，期望 22016", got)
	}
}

func TestMarkHookMountedNilAndNonHook(t *testing.T) {
	markHookMounted(nil)
	rod := wiregen.MustMakeObj("kkmhfbeldpg", nil)
	markHookMounted(rod) // 非钩对象递归无副作用，仅要求不 panic
}

// 多钩钓组逐钩挂竿位：每枚挂了鱼的钩单独推 4/22，互不影响。
// 实机 2026-11-13：倒钓臂 4 枚钩全挂了鱼，但只有主鱼那枚在 14/11 拿到挂竿位，
// 其余钩停在静止件状态，鱼模型不会挂上去——拉上来只看得见一条。
func TestBuildHookTreeByGUIDMarksOnlyItsOwnHook(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	hook, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateItem(t.Context(), 1000001, "c_hook_20712", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}

	tree := s.buildHookTreeByGUID(t.Context(), hook.GUID)
	if tree == nil {
		t.Fatal("按 GUID 取不到钩树")
	}
	markHookMounted(tree)
	if got := hookFlagsOut(t, tree); got&hookMountedFlag == 0 {
		t.Fatalf("目标钩未补挂竿位：flags=%d", got)
	}

	otherTree := s.buildHookTreeByGUID(t.Context(), other.GUID)
	if otherTree == nil {
		t.Fatal("第二枚钩取不到树")
	}
	if got := hookFlagsOut(t, otherTree); got&hookMountedFlag != 0 {
		t.Fatalf("只该影响自己那枚钩，另一枚却被改了：flags=%d", got)
	}
}

// 空会话/空 GUID 不得 panic。
func TestPush4_22HookGUIDNoop(t *testing.T) {
	t.Parallel()
	s, _ := newRigTestServer(t)
	var nilSess *Session
	if nilSess.Push4_22HookGUID("abc", true) {
		t.Fatal("空会话不应推送成功")
	}
	sess := &Session{Srv: s, Userid: 1000001, log: logging.Discard()}
	if sess.Push4_22HookGUID("", true) {
		t.Fatal("空钩 GUID 不应推送成功")
	}
}
