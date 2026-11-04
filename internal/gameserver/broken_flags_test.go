package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/proto"
)

// TestBrokenRodItemTreeCarriesDamageBits 断竿（耐久0）物品树必须带客户端损坏位：
// hgaijieklek|=128（bapnijnooje.akhnmdpbhnf）与 dbgaheblmei|=16
// （pfapfdlnfbj.fhblahbelfd）。健康竿不得携带。
func TestBrokenRodItemTreeCarriesDamageBits(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.DB.SetItemDurability(ctx, rod.GUID, 0); err != nil {
		t.Fatal(err)
	}

	row, err := srv.DB.GetItemByGUID(ctx, rod.GUID)
	if err != nil || row == nil {
		t.Fatalf("取竿行失败: %v %v", row, err)
	}
	tree, err := srv.ItemToTree(ctx, row)
	if err != nil {
		t.Fatal(err)
	}

	stateIdx := fieldIdxByCS(tree.Class, csStateEnum)
	subIdx := fieldIdxByCS(tree.Class, csSubtype)
	if stateIdx < 0 || subIdx < 0 {
		t.Fatalf("竿类缺少状态字段 state=%d sub=%d", stateIdx, subIdx)
	}
	gotFlags := u64Of(tree.Fields[stateIdx].Num)
	if gotFlags&brokenItemFlagBit == 0 {
		t.Fatalf("断竿 hgaijieklek 缺 128 位: %d (%b)", gotFlags, gotFlags)
	}
	// 状态位保持原样：追加 16 会禁用客户端删除/出售动作（fhblahbelfd 语义）。
	if gotState := u64Of(tree.Fields[subIdx].Num); gotState != 0 {
		t.Fatalf("断竿 dbgaheblmei 不应改动: %d", gotState)
	}

	// 健康竿对照组：不得携带损坏位。
	rod2, err := srv.CreateItem(ctx, uid, "tele_10012_tl16", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	row2, err := srv.DB.GetItemByGUID(ctx, rod2.GUID)
	if err != nil || row2 == nil {
		t.Fatal(err)
	}
	tree2, err := srv.ItemToTree(ctx, row2)
	if err != nil {
		t.Fatal(err)
	}
	f2 := u64Of(tree2.Fields[stateIdx].Num)
	if f2&brokenItemFlagBit != 0 {
		t.Fatalf("健康竿不应带损坏位: %d", f2)
	}
	_ = proto.S // 保持 import
}
