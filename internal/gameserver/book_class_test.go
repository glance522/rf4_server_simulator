package gameserver

import "testing"

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
