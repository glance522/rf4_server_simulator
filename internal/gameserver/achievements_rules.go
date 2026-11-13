package gameserver

import (
	"context"
	"strings"
	"time"
)

// 成就规则：每个成就只有一条正常判断路径。
//
//	玩法事件 → check(事件) → 满足则写 unlocked/progress/claimed → 推 2/8
//
// 没有补齐、回退、预测、重放判断、冻结开关、领奖状态、事件去重、投影重建。
// 已解锁的成就直接跳过，这是「已解锁则无事发生」的正常语义，不是异常分支。
// 3/3 只读 player_achievements，不参与判定。
//
// 事件只携带判断需要的本次数据，由玩法入口在成功路径上装配好；Check 全是
// 纯布尔表达式，不查库、不访问 ctx，因此每条成就永远只有一条判断路径。
//
// 条件来源：internal/gameserver/assets/achievement_texts.json，由客户端
// 4.0.25034 成就列表面板还原的 130 条标题/描述（截图第 N 条对应
// achievement_def 按 id 升序第 N 个 id）。目前实装 89 条，其余 41 条
// 依赖服务端尚未实现的功能，在文件末尾按分组留空注释。

type achievementEventKind uint8

const (
	evFishLanded   achievementEventKind = iota + 1 // 14/4 上鱼
	evCast                                         // 14/2 抛投
	evFishKept                                     // 14/5 入护（catch_log 已在 14/4 结算落库，入护只把 source 抬成 'keep'）
	evCraftOK                                      // 9/1 制作成功
	evBaitDug                                      // 9/3 挖饵成功
	evFishReleased                                 // 14/6 放流
)

// achievementEvent 只携带判断需要的本次事件数据。
type achievementEvent struct {
	Kind          achievementEventKind
	WeightG       int64   // 上鱼体重（克）
	DistanceM     float64 // 本次实际抛投距离（米）
	RigType       int64   // 本杆钓组类型（rigmeta.go:rigTypeNames）
	Bait          string  // 钩上饵/拟饵的 item_id
	DistinctBaits int64   // 累计用过的不同诱饵数（catch_log）
	RecipeID      string  // 本次制作配方
	BaitKey       string  // 本次挖到的饵 item_id
	CookPoints    float64 // 烹饪熟练度（skill.skill_id=6，0..100）
	Grade         int64   // 放流鱼档位（1..4，4=蓝冠=超级稀有）
}

type achievementRule struct {
	ID    int64
	Event achievementEventKind
	Check func(achievementEvent) bool
}

// 下面这些工厂只是把「一条布尔判断」参数化，规则表里每条成就仍只有一行、
// 一个 Check，没有任何状态或分支。
func fishHeavierThan(g int64) func(achievementEvent) bool {
	return func(ev achievementEvent) bool { return ev.WeightG > g }
}

// maxAchievementCastM 是抛投距离成就认可的物理上限（米）。阶梯最高 100 米，
// 留一档余量。14/2 的 f0 是 F32，客户端可上报 NaN/Inf 或伪造超大值，
// 这些值一律不参与判定。不拿 castDistanceForRig 的 actionMaxM 做上限：
// 服务端物理模型与客户端实测仍有偏差（实机 requested_m=38.81 / max_m=20.75），
// 用它 clamp 会把已验证的 1000 成就误杀。
const maxAchievementCastM = 150.0

// castOver 只认有限、非负的实际抛投距离：NaN 与任何数比较都为 false，
// ±Inf 与超过物理上限的伪造值被上界挡住，负值过不了 m 门槛。
// 仍是一条布尔判断，没有额外分支。
func castOver(m float64) func(achievementEvent) bool {
	return func(ev achievementEvent) bool {
		return ev.DistanceM > m && ev.DistanceM < maxAchievementCastM
	}
}

func rigIs(rts ...int64) func(achievementEvent) bool {
	return func(ev achievementEvent) bool {
		for _, rt := range rts {
			if ev.RigType == rt {
				return true
			}
		}
		return false
	}
}

func baitPrefix(ps ...string) func(achievementEvent) bool {
	return func(ev achievementEvent) bool {
		for _, p := range ps {
			if strings.HasPrefix(ev.Bait, p) {
				return true
			}
		}
		return false
	}
}

func recipeIs(id string) func(achievementEvent) bool {
	return func(ev achievementEvent) bool { return ev.RecipeID == id }
}

func recipePrefix(ps ...string) func(achievementEvent) bool {
	return func(ev achievementEvent) bool {
		for _, p := range ps {
			if strings.HasPrefix(ev.RecipeID, p) {
				return true
			}
		}
		return false
	}
}

func baitKeyIs(k string) func(achievementEvent) bool {
	return func(ev achievementEvent) bool { return ev.BaitKey == k }
}

func distinctBaitsAtLeast(n int64) func(achievementEvent) bool {
	return func(ev achievementEvent) bool { return ev.DistinctBaits >= n }
}

func cookAtLeast(p float64) func(achievementEvent) bool {
	return func(ev achievementEvent) bool { return ev.CookPoints >= p }
}

// 每增加一条成就，只加一行：绑定事件 + 一个布尔判断。
var achievementRules = []achievementRule{
	// ---- 101-110 浮钓：按 rigmeta.go 的 rigTypeNames ----
	{ID: 101, Event: evFishLanded, Check: rigIs(1)},      // 固定浮标 rig_FloatSimple
	{ID: 103, Event: evFishLanded, Check: rigIs(2)},      // 博洛尼亚钓组
	{ID: 105, Event: evFishLanded, Check: rigIs(21)},     // Waggler 自重漂
	{ID: 107, Event: evFishLanded, Check: rigIs(4)},      // 饵鱼浮钓
	{ID: 109, Event: evFishLanded, Check: rigIs(19, 20)}, // Sbirolino
	{ID: 110, Event: evFishLanded, Check: rigIs(30)},     // 浮漂发丝（鲤鱼）

	// ---- 151-162 路亚 ----
	{ID: 151, Event: evFishLanded, Check: baitPrefix("spoon", "spinner")},                          // 勺子/旋转饵
	{ID: 152, Event: evFishLanded, Check: rigIs(6)},                                                // 简易汲钩 jig
	{ID: 153, Event: evFishLanded, Check: baitPrefix("crank", "plug", "hm_minnow", "hm_j_minnow")}, // 鱼形硬饵
	{ID: 154, Event: evFishLanded, Check: baitPrefix("topwater", "popper")},                        // 表水路亚
	{ID: 155, Event: evFishLanded, Check: rigIs(10)},                                               // 分支子线
	{ID: 158, Event: evFishLanded, Check: rigIs(8)},                                                // 倒钓组
	{ID: 159, Event: evFishLanded, Check: rigIs(7)},                                                // 德州
	{ID: 160, Event: evFishLanded, Check: rigIs(9)},                                                // 卡罗莱纳
	{ID: 161, Event: evFishLanded, Check: rigIs(11)},                                               // 维基
	{ID: 162, Event: evFishLanded, Check: baitPrefix("jerkbait")},                                  // 抽停硬饵

	// ---- 200-240 底钓/鲤鱼 ----
	{ID: 200, Event: evFishLanded, Check: rigIs(12)}, // 简易水底
	{ID: 202, Event: evFishLanded, Check: rigIs(13)}, // 链斗式 Paternoster
	{ID: 203, Event: evFishLanded, Check: rigIs(14)}, // in-line
	{ID: 205, Event: evFishLanded, Check: rigIs(15)}, // 环型
	{ID: 206, Event: evFishLanded, Check: rigIs(18)}, // 诱饵笼 FeedRocket
	{ID: 207, Event: evFishLanded, Check: rigIs(16)}, // method
	{ID: 208, Event: evFishLanded, Check: rigIs(17)}, // 饵鱼底部
	{ID: 211, Event: evFishLanded, Check: rigIs(23)}, // 测竿 Marker
	{ID: 212, Event: evFishLanded, Check: rigIs(24)}, // 传统发丝
	{ID: 213, Event: evFishLanded, Check: rigIs(26)}, // Pop-up
	{ID: 214, Event: evFishLanded, Check: rigIs(25)}, // 雪人
	{ID: 215, Event: evFishLanded, Check: rigIs(28)}, // Chod
	{ID: 216, Event: evFishLanded, Check: rigIs(29)}, // Maggot
	{ID: 217, Event: evFishLanded, Check: rigIs(27)}, // Hinged-Stiff
	{ID: 218, Event: evFishLanded, Check: rigIs(32)}, // Pop-up method
	{ID: 240, Event: evFishLanded, Check: rigIs(34)}, // 欧鲤 Zig

	// ---- 250-275 饵料采集与制作 ----
	{ID: 250, Event: evBaitDug, Check: baitKeyIs("worm_01")},                                                               // 软体虫
	{ID: 251, Event: evBaitDug, Check: baitKeyIs("muckworm")},                                                              // 红蚯蚓
	{ID: 252, Event: evCraftOK, Check: recipeIs("craft_bait_bread")},                                                       // 湿面包
	{ID: 253, Event: evCraftOK, Check: recipeIs("craft_bait_perlovka")},                                                    // 珍珠大麦粥
	{ID: 254, Event: evCraftOK, Check: recipeIs("craft_bait_dough_sweet")},                                                 // 甜面团
	{ID: 255, Event: evCraftOK, Check: recipeIs("craft_bait_cream_wheat")},                                                 // 粗面粉粥
	{ID: 256, Event: evCraftOK, Check: recipeIs("craft_bait_dough_garlic")},                                                // 大蒜面团
	{ID: 257, Event: evCraftOK, Check: recipeIs("craft_bait_dough_cottage")},                                               // 奶渣面团
	{ID: 258, Event: evBaitDug, Check: baitKeyIs("bloodworm")},                                                             // 红虫
	{ID: 259, Event: evBaitDug, Check: baitKeyIs("caddisfly")},                                                             // 毛翅目幼虫
	{ID: 260, Event: evCraftOK, Check: recipePrefix("craft_bait_livefish_", "craft_bait_deadfish_", "craft_bait_fillet_")}, // 饵鱼
	{ID: 261, Event: evBaitDug, Check: baitKeyIs("naiad")},                                                                 // 蜉蝣幼虫
	{ID: 262, Event: evCraftOK, Check: recipeIs("craft_bait_from_frog")},                                                   // 青蛙饵鱼
	{ID: 264, Event: evCraftOK, Check: recipeIs("craft_bait_cut_fish")},                                                    // 鱼肉饵
	{ID: 265, Event: evBaitDug, Check: baitKeyIs("vypolzok_01")},                                                           // 地龙
	{ID: 266, Event: evBaitDug, Check: baitKeyIs("black_leech")},                                                           // 水蛭
	// 267 成就文本作「蛲螂」，静态库无该名；按同一注记（疑为甲虫类饵料）绑定蜣螂。
	{ID: 267, Event: evBaitDug, Check: baitKeyIs("dorbeetle")},                                                   // 蛲螂→蜣螂
	{ID: 269, Event: evBaitDug, Check: baitKeyIs("maybug_larva")},                                                // 土蚕
	{ID: 270, Event: evBaitDug, Check: baitKeyIs("gryllotalpa")},                                                 // 蝼蛄
	{ID: 271, Event: evBaitDug, Check: baitKeyIs("waterbug")},                                                    // 水生甲虫→龙虱甲虫
	{ID: 272, Event: evBaitDug, Check: baitKeyIs("unibeetle_larva")},                                             // 犀角金龟幼虫
	{ID: 273, Event: evCraftOK, Check: recipeIs("craft_bait_from_crawfish")},                                     // 河螯虾饵
	{ID: 274, Event: evCraftOK, Check: recipePrefix("craft_bait_from_freshmussel", "craft_bait_from_dreissena")}, // 河贻贝肉饵
	{ID: 275, Event: evBaitDug, Check: baitKeyIs("watergrass")},                                                  // 水藻→一束藻类

	// ---- 320 PVA 棒 ----
	{ID: 320, Event: evCraftOK, Check: recipePrefix("craft_feed_pva_")},

	// ---- 350-365 自制路亚（recipe 表 craft_lure_* 161 条按系列前缀）----
	{ID: 350, Event: evCraftOK, Check: recipePrefix("craft_lure_novg")},                                // 诺夫格勒
	{ID: 351, Event: evCraftOK, Check: recipePrefix("craft_lure_mosc")},                                // 莫斯科
	{ID: 352, Event: evCraftOK, Check: recipePrefix("craft_lure_volgabig")},                            // 下伏尔加大
	{ID: 353, Event: evCraftOK, Check: recipePrefix("craft_lure_volgasm")},                             // 下伏尔加小
	{ID: 354, Event: evCraftOK, Check: recipePrefix("craft_lure_serdobshort")},                         // 谢尔多布斯克短
	{ID: 355, Event: evCraftOK, Check: recipePrefix("craft_lure_serdoblong")},                          // 谢尔多布斯克长
	{ID: 356, Event: evCraftOK, Check: recipePrefix("craft_lure_porolon")},                             // 泡沫橡胶鱼
	{ID: 357, Event: evCraftOK, Check: recipePrefix("craft_lure_sudak")},                               // 梭鲈鱼
	{ID: 358, Event: evCraftOK, Check: recipePrefix("craft_lure_sib")},                                 // 西西伯利亚
	{ID: 359, Event: evCraftOK, Check: recipePrefix("craft_lure_spinner")},                             // 旋转路亚
	{ID: 360, Event: evCraftOK, Check: recipePrefix("craft_lure_popper")},                              // 波扒
	{ID: 361, Event: evCraftOK, Check: recipePrefix("craft_lure_walker")},                              // walker
	{ID: 362, Event: evCraftOK, Check: recipePrefix("craft_lure_jerk")},                                // 抽停拟饵
	{ID: 363, Event: evCraftOK, Check: recipePrefix("craft_lure_purecrank", "craft_lure_woodcrank")},   // 摇滚小胖子
	{ID: 364, Event: evCraftOK, Check: recipePrefix("craft_lure_pureminnow", "craft_lure_woodminnow")}, // 米诺
	{ID: 365, Event: evCraftOK, Check: recipePrefix("craft_lure_purejoin", "craft_lure_woodjoined")},   // 组装路亚

	// ---- 400-417 单次制作型烹饪/饮品（累计与全集型见末尾留空清单）----
	{ID: 400, Event: evCraftOK, Check: recipeIs("craft_cook_thermos_tea")},    // 茶
	{ID: 401, Event: evCraftOK, Check: recipeIs("craft_cook_thermos_coffee")}, // 咖啡
	{ID: 402, Event: evCraftOK, Check: recipeIs("craft_cook_thermos_cocoa")},  // 可可
	{ID: 403, Event: evCraftOK, Check: recipeIs("craft_cook_home_wine")},      // 自酿葡萄酒
	{ID: 404, Event: evCraftOK, Check: recipeIs("craft_cook_samogon_potato")}, // 马铃薯摩闪酒
	{ID: 405, Event: evCraftOK, Check: recipeIs("craft_cook_samogon_pennik")}, // 面包酒
	{ID: 406, Event: evCraftOK, Check: recipeIs("craft_cook_kotleti_pike")},   // 狗鱼饼
	{ID: 410, Event: evCraftOK, Check: recipeIs("craft_cook_blini")},          // 煎饼
	{ID: 417, Event: evCraftOK, Check: recipeIs("craft_cook_sidr")},           // 苹果酒

	// ---- 420-422 烹饪技能（skill.points，skill_id=6，上限 100）----
	{ID: 420, Event: evCraftOK, Check: cookAtLeast(40)},
	{ID: 421, Event: evCraftOK, Check: cookAtLeast(70)},
	{ID: 422, Event: evCraftOK, Check: cookAtLeast(100)},

	// ---- 900-912 里程碑 / 诱饵广度 / 放流 ----
	{ID: 900, Event: evFishLanded, Check: func(achievementEvent) bool { return true }},
	{ID: 905, Event: evFishLanded, Check: fishHeavierThan(50_000)}, // 体重超过 50 公斤
	{ID: 906, Event: evFishKept, Check: distinctBaitsAtLeast(20)},  // 20 种不同诱饵
	{ID: 907, Event: evFishKept, Check: distinctBaitsAtLeast(50)},  // 50 种
	{ID: 908, Event: evFishKept, Check: distinctBaitsAtLeast(10)},  // 10 种鱼饵
	{ID: 909, Event: evFishKept, Check: distinctBaitsAtLeast(30)},  // 30 种
	// 912 如果爱我就放我走：释放超级稀有鱼。蓝冠（grade 4，
	// econ_market.go:superTrophyGrade）就是超级稀有，与物种稀有度无关。
	{ID: 912, Event: evFishReleased, Check: func(ev achievementEvent) bool {
		return ev.Grade >= superTrophyGrade
	}},

	// ---- 1000-1004 抛投距离阶梯（DistanceM 来自 14/2 本次实际抛投）----
	{ID: 1000, Event: evCast, Check: castOver(30)},
	{ID: 1001, Event: evCast, Check: castOver(45)},
	{ID: 1002, Event: evCast, Check: castOver(60)},
	{ID: 1003, Event: evCast, Check: castOver(80)},
	{ID: 1004, Event: evCast, Check: castOver(100)},
}

// distinctCatchBaits 是 14/5 入护事件的统计来源：catch_log 现在在 14/4 结算就写入
// （source='catch'，放生保持该值），所以这里统计的「用过的诱饵种类」也包含放生
// 用过的饵；成就仍挂在 evFishKept 上判定，不会因结算提前落库而提前解锁。
func (s *Server) distinctCatchBaits(ctx context.Context, uid int64) int64 {
	var n int64
	_ = s.DB.SQL.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT bait) FROM catch_log WHERE uid=? AND bait<>''`, uid).Scan(&n)
	return n
}

// evaluateAchievements 是唯一的判定入口，由玩法事件在发生后调用一次。
func (s *Server) evaluateAchievements(ctx *Ctx, ev achievementEvent) {
	if ctx == nil || ctx.Sess == nil || s.DB == nil {
		return
	}
	uid := ctx.Sess.Userid
	states, err := s.DB.PlayerAchievementStates(s.dbCtx(), uid)
	if err != nil {
		ctx.Log.Error("成就状态读取失败", "uid", uid, "err", err)
		return
	}
	now := float64(time.Now().Unix())
	newly := make([]int64, 0, len(achievementRules))
	for _, r := range achievementRules {
		if r.Event != ev.Kind || !r.Check(ev) {
			continue
		}
		// 只有 unlocked 与 claimed 同时为真才算真正完成。旧版本可能留下
		// unlocked=1, claimed=0 的中间态（图标永远不亮、也不会再推送），
		// 这里让它继续走到下面的 UPSERT 修复并补推一次。
		if st := states[r.ID]; st.Unlocked && st.Claimed {
			continue
		}
		// 客户端实测只有 claimed=1 会点亮图标；本模拟器无发奖链路，解锁即点亮。
		// 一次 SQL 写两个状态，只有真正首次点亮才推 2/8，避免重复推送。
		claimed, err := s.DB.UnlockAndClaimAchievement(s.dbCtx(), uid, r.ID, 1, now)
		if err != nil {
			ctx.Log.Error("成就解锁失败", "uid", uid, "id", r.ID, "err", err)
			continue
		}
		if claimed {
			newly = append(newly, r.ID)
		}
	}
	s.queueAchievementUnlockPushes(ctx, newly)
}

// ============================================================================
// 留空成就（33 条）= achievement_def 的 130 个 id − 规则表的 97 条。
// 完整清单与 achievements_registry_test.go 的 unimplementedAchievementIDs 一致：
//
//   102, 104, 106, 108, 156, 157, 204, 209, 210,
//   300, 301, 302, 303, 304, 305, 306, 307, 308, 309, 310, 311,
//   409, 411, 412, 413, 414, 415, 416,
//   901, 902, 903, 904, 911
//
// 待对应功能落地后，按「绑定事件 + 一个布尔判断」在规则表各补一行即可。
// 依赖的缺失能力按组说明：
//
// 102/104/156/157/204/209 竿型与轮型（博洛尼亚竿、竞赛竿、枪柄竿、
//   抛饵卷线器、PICKER 竿、鲤鱼竿）：item_def.attrs_json 有
//   fopbhlmkcpe 竿型/pdcalfjaigf 轮型数据，但成就事件尚未装配这两个字段。
//
// 106/108 三明治挂饵、夜光浮标：无「同钩多饵」枚举与发光浮漂标识。
//
// 210 抛掷 PVA 棒钓组：无抛投载荷事件（206 抛饵笼、211 测竿已用 rigType
//   近似实装，不在此清单内）。
//
// 251/265/266/267/269/270/271/275 红蚯蚓、地龙、水蛭、蛲螂、土蚕、蝼蛄、
//   水生甲虫、水藻：原先阻挡它们的是「挖饵池不发这些饵」，现已由 baitdig.go 的
//   baitDigPools 按解锁锚点接入对应饵种，规则按挖饵事件绑定。
//   267 成就文本作「蛲螂」、静态库无该名，绑定甲虫类饵料 dorbeetle（蜣螂）；
//   271 的「水生甲虫」绑定龙虱甲虫 waterbug。
//
// 300-311 供养人 1~12 级（共 12 条）：craft_feed_* 配方存在，但没有
//   「成分等级」概念——recipe.materials_json 无等级列、
//   item_def.attrs_json 无 tier/level。待服务端实现成分等级数据。
//
// 409/411/412/413/414/415/416 连续五次同一道菜、三种汤、各种鱼子酱/鱼干/
//   烟熏菜/油炸食品/肉鱼类菜肴：配方齐备，但无制作历史（txn 只在 note 里
//   带 recipe=，且可计件产物并堆不产生新行），无法做连续/全集去重。
//
// 901/902/903 每种浮子/纺车/沉底钓组各钓一条：需要「已用钓组集合」持久化，
//   player_achievements 无集合列。
//
// 904 连续五条鱼无中断：全项目无 streak/连续计数（player_counters 只有
//   escapes/line_breaks/rod_breaks）。
//
// 911 累计释放 10 条稀有鱼：此前放流无持久化（catch_log 只记 source='keep'）。
//   现在 catch_log 在结算落 source='catch'、入护才抬成 'keep'，放生的稀有鱼
//   可按「grade>=3 且 source='catch'」计数，911 具备实现条件（本批不实装）。
//   912 单次放流已实装。
//
// ---- 以下已实装，不属于上面 33 条，仅记录近似判定的已知差距 ----
// 151/153/154/162 用拟饵 item_id 前缀判断（crank/plug/spoon/spinner/
//   topwater/popper/jerkbait，以及自制米诺 hm_minnow_*/hm_j_minnow_*）。
//   静态库无 minnow* 前缀，原规则的 "minnow" 是死分支，已换成真实前缀。
//   若日后拿到 LureType 运行时常量表，可换成精确分类。
// 1000-1004 文案写「拟饵抛掷」，目前只判距离；待决定是否给 evCast 带
//   RigType 并判 rigTypeLureFamily（rigmeta.go）。
// ============================================================================
