package gameserver

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"rf4server/internal/proto"
)

// category 80（技能书）的顶层物品类必须是 wrapper ipdakkbfoja/13301：
// 客户端在背包里把物品对象转成书目配置，发 13300 会导致
// "Unable to cast object of type 'onobdmohahp' to type 'blmhnnbiofn'" 并卡在载入资料。
func TestSkillBookUsesWrapperClass(t *testing.T) {
	got, ok := categoryWireClass[80]
	if !ok {
		t.Fatal("category 80 未映射")
	}
	if got.Class != "ipdakkbfoja" || got.TypeID != 13301 {
		t.Fatalf("category 80 映射=%+v，期望 ipdakkbfoja/13301", got)
	}
}

func TestSkillBookUseRaisesProficiencyAndConsumesBook(t *testing.T) {
	cases := []struct {
		itemID string
		cat    int64
		target float64
	}{
		{"float_fishing_book", 1, 30},
		{"spinning_book", 2, 30},
		{"feeder_book", 3, 40},
		{"seafish_book", 11, 30},
	}
	for _, tc := range cases {
		t.Run(tc.itemID, func(t *testing.T) {
			srv, db := newEconomyTxTestServer(t)
			cctx := context.Background()
			if _, err := db.EnsureProfile(cctx, 1000001); err != nil {
				t.Fatal(err)
			}
			row, err := srv.CreateItem(cctx, 1000001, tc.itemID, "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			useCtx := drinkUseTestCtx(t, srv, 1, row.GUID, 0)
			body, handled := hItemUse(useCtx)
			if !handled || len(body) == 0 || len(useCtx.afterResponse) != 1 {
				t.Fatalf("4/8 阅读技能书未完成: handled=%v body=%v pushes=%d", handled, body, len(useCtx.afterResponse))
			}
			value, end, err := proto.DecodeValue(body, 0, srv.Table())
			if err != nil || end != len(body) || value.TypeID != 800 {
				t.Fatalf("800 快照不符: %+v end=%d err=%v", value, end, err)
			}
			f2 := value.Field(2).Val
			if f2 == nil || f2.Kind != proto.KindGuidArr || len(f2.Items) != 1 ||
				!strings.EqualFold(f2.Items[0].Hex, row.GUID) {
				t.Fatalf("800.f2 未回传被移除的书: %+v", f2)
			}
			if _, err := db.GetItemByGUID(cctx, row.GUID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("技能书未从背包消耗: err=%v", err)
			}
			pts, err := db.GetSkillPoints(cctx, 1000001, tc.cat)
			if err != nil || pts != tc.target {
				t.Fatalf("熟练度不符: cat=%d pts=%v want=%v err=%v", tc.cat, pts, tc.target, err)
			}
		})
	}
}

func TestSkillBookUseNoEffectKeepsBook(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	cctx := context.Background()
	if _, err := db.EnsureProfile(cctx, 1000001); err != nil {
		t.Fatal(err)
	}
	if err := db.AddSkillPoints(cctx, 1000001, 11, 40); err != nil {
		t.Fatal(err)
	}
	row, err := srv.CreateItem(cctx, 1000001, "seafish_book", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	useCtx := drinkUseTestCtx(t, srv, 2, row.GUID, 0)
	body, handled := hItemUse(useCtx)
	failBody, present, _, failure := useCtx.replyCapture.snapshot()
	if !handled || body != nil || !present || !failure || len(useCtx.afterResponse) != 0 {
		t.Fatalf("应回无效果失败: handled=%v body=%v present=%v failure=%v pushes=%d",
			handled, body, present, failure, len(useCtx.afterResponse))
	}
	// 必须回 174 操作结果码，普通 .NET 异常会被客户端显示成通用内部错误。
	want := proto.BuildFailureEnvelope(2, "RF4.API.APIError", "174")
	if !bytes.Equal(failBody, want) {
		t.Fatalf("无效果失败信封不符: got=%x want=%x", failBody, want)
	}
	if _, err := db.GetItemByGUID(cctx, row.GUID); err != nil {
		t.Fatalf("无效果时不应消耗技能书: %v", err)
	}
	pts, err := db.GetSkillPoints(cctx, 1000001, 11)
	if err != nil || pts != 40 {
		t.Fatalf("无效果时熟练度不应变化: pts=%v err=%v", pts, err)
	}
}

// 已经读过一本同类技能书后再读第二本：不得报通用内部错误，第二本保留。
func TestSkillBookUseAgainRepliesNoEffect(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	cctx := context.Background()
	if _, err := db.EnsureProfile(cctx, 1000001); err != nil {
		t.Fatal(err)
	}
	first, err := srv.CreateItem(cctx, 1000001, "spinning_book", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	firstCtx := drinkUseTestCtx(t, srv, 1, first.GUID, 0)
	if body, handled := hItemUse(firstCtx); !handled || len(body) == 0 {
		t.Fatalf("首次阅读应成功: handled=%v body=%x", handled, body)
	}
	if _, err := db.GetItemByGUID(cctx, first.GUID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("首次阅读应消耗技能书: err=%v", err)
	}

	second, err := srv.CreateItem(cctx, 1000001, "spinning_book", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	secondCtx := drinkUseTestCtx(t, srv, 2, second.GUID, 0)
	body, handled := hItemUse(secondCtx)
	failBody, present, _, failure := secondCtx.replyCapture.snapshot()
	if !handled || body != nil || !present || !failure || len(secondCtx.afterResponse) != 0 {
		t.Fatalf("再次阅读应回无效果失败: handled=%v body=%v present=%v failure=%v pushes=%d",
			handled, body, present, failure, len(secondCtx.afterResponse))
	}
	want := proto.BuildFailureEnvelope(2, "RF4.API.APIError", "174")
	if !bytes.Equal(failBody, want) {
		t.Fatalf("再次阅读失败信封不符: got=%x want=%x", failBody, want)
	}
	if _, err := db.GetItemByGUID(cctx, second.GUID); err != nil {
		t.Fatalf("再次阅读不应消耗技能书: %v", err)
	}
	pts, err := db.GetSkillPoints(cctx, 1000001, 2)
	if err != nil || pts != 30 {
		t.Fatalf("再次阅读不应改变熟练度: pts=%v err=%v", pts, err)
	}
}
