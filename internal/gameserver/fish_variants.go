package gameserver

import (
	"fmt"
	"hash/fnv"
	"math"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 客户端 fishes 资源包（rf4_x64_Data/Data/fishes）里的 FishFamily 资源按「变体」分族：
// 每个族给出三套 body/mesh/skin（smallFishName / mediumFishName / largeFishName），
// 例如 a_salmon_f2 族 = a_salmon_smolt_s 皮肤 + a_salmon_f2_m 网格 + a_salmon_f2_b 身体。
//
// 同一种鱼存在多个族时，客户端按一个「文本/名 key」去取族：
//   - 溜鱼（14/14 的 27 对象）读 f22；
//   - 结算弹窗 / 渔护 / 渔护详情（25 对象 ecknbkpeeji）读 f11；
//
// 两个字段同名（oajeeklppia），取不到就退回该鱼种的**默认族**。
// 默认族是历史遗留的混拼资源（a_salmon 默认族 = a_salmon_smolt_s 皮肤 +
// a_salmon_m3_m 网格 + a_salmon_f3_b 身体），这正是玩家一直看到的那一个模型；
// 其它族的资源都在包里，只是服务端从来没告诉客户端用哪个。
//
// 族名必须由同一尾鱼稳定推导，否则会出现「溜的是一条、鱼护里是另一条」。
// 这里用（鱼种、体重、FishGUID）推导：同一尾鱼在溜鱼 / 结算 / 渔护 / 鱼卡四处
// 算出的族名必然一致，重新登录、重连、重读库也不会变。
//
// **族与体重挡位无关**：官服同一鱼种的多套外观是随机出的，同一挡位既可能出 1 期也可能
// 出 3 期，不存在「小的是 1 期、上星固定 2 期、蓝冠固定 3 期」的对应。旧实现按「入档线
// → 蓝冠线」的相对位置取下标，结果每个挡位永远是同一套模型（玩家实测：上星与蓝冠各是
// 固定的一两种），已改为按身份种子取下标，见 fishVariantStageIndex。
var fishModelVariants = map[string]fishModelVariant{
	// 大西洋鲑：幼鱼 parr / 降海幼鱼 smolt / 成鱼雌雄各 3 套外观
	// （体色深浅与下颌钩曲程度不同，官服随机出，不按体型分期）
	"a_salmon": {
		female: []string{"a_salmon_f1", "a_salmon_f2", "a_salmon_f3"},
		male:   []string{"a_salmon_m1", "a_salmon_m2", "a_salmon_m3"},
		parr:   "a_salmon_parr",
		smolt:  "a_salmon_smolt",
	},
	// 大鳞麻哈鱼：雌雄各 5 套外观
	"chavycha": {
		female: []string{"chavycha_f_1", "chavycha_f_2", "chavycha_f_3", "chavycha_f_4", "chavycha_f_5"},
		male:   []string{"chavycha_m_1", "chavycha_m_2", "chavycha_m_3", "chavycha_m_4", "chavycha_m_5"},
	},
	// 大马哈鱼：雌雄各 2 套外观
	"keta": {
		female: []string{"keta_f_1", "keta_f_2"},
		male:   []string{"keta_m_1", "keta_m_2"},
	},
	// 奇努克鲑：雌雄各 2 套外观
	"kijuch": {
		female: []string{"kijuch_f_1", "kijuch_f_2"},
		male:   []string{"kijuch_m_1", "kijuch_m_2"},
	},
	// 下面几种没有雌雄分期，资源包里就是「默认族 + 编号族」两/三套完整外观，
	// 默认族本身可用，所以列表里要把默认族排在第一位。
	//
	// 注意这里的 key 是**鱼种 key**，不是资源族名：美拟鲈的资源族叫 a_sleeper，
	// 但物种表（species_weight / catch_log）里是带点的 a.sleeper。写成 a_sleeper
	// 永远查不到，等于这一族的变体从来没生效过。
	"a.sleeper": {
		female: []string{"a_sleeper", "a_sleeper_2", "a_sleeper_3"},
		male:   []string{"a_sleeper", "a_sleeper_2", "a_sleeper_3"},
	},
	"burbot": {
		female: []string{"burbot", "burbot_2"},
		male:   []string{"burbot", "burbot_2"},
	},
	// bs_salmon：资源包里确有 bs_salmon / bs_salmon_2 两族，但当前 252 个鱼种 key
	// 里没有任何一个叫 bs_salmon（a_salmon / k_salmon / l_salmon 都另有默认族，
	// 且没有编号族），保留条目等官方补鱼种时直接命中，现在不会触发。
	"bs_salmon": {
		female: []string{"bs_salmon", "bs_salmon_2"},
		male:   []string{"bs_salmon", "bs_salmon_2"},
	},
}

type fishModelVariant struct {
	female []string
	male   []string
	parr   string
	smolt  string
}

// 幼鱼 / 降海幼鱼族只给「鱼苗体型」的个体用。
//
// 这两族在资源包里三套尺寸槽（small / medium / large）全部指向同一个 `*_s`
// 模型，也就是说美术只做了鱼苗期那一套。客户端渲染时按鱼的尺寸选槽：
// 短鱼走 small（拿到的正是鱼苗模型，正常），体长一旦落到 medium / large 槽，
// 拿到的还是 `*_s`，模型与槽位对不上 —— 实测表现就是「溜鱼、结算、渔护详情都
// 正常，只有渔护列表里的缩略图是空白」。7.7kg / 0.95m 的降海幼鱼踩的就是这个坑。
//
// 所以除了体重阈值，还必须卡体长：只有确实在鱼苗尺度（默认 ≤ 0.35m）时才用，
// 成鱼体型的个体一律走成鱼族。两个体重阈值保持原版 1.5kg / 4kg 即可，
// 本服派鱼最轻也有 7kg，这两族实际上只在真正的小鱼上出现。
const (
	defaultFishVariantParrMaxG     = 1500
	defaultFishVariantSmoltMaxG    = 4000
	defaultFishVariantJuvenileMaxM = 0.35
)

// juvenileLengthOK 判断这尾鱼的体长是否落在鱼苗尺度。
// 体长缺失（老数据）时按「不放行」处理：宁可用成鱼族，也不要出空白缩略图。
func juvenileLengthOK(lengthM, maxM float64) bool {
	return lengthM > 0 && maxM > 0 && lengthM <= maxM
}

// fishModelVariantEnabled 是否启用多模型族（tunables.fish_model_variant，默认开）。
// 置 0 即回到「每个鱼种只用客户端默认族」的旧行为，不用重新编译、不用重启。
func (s *Server) fishModelVariantEnabled() bool {
	return s.tunableF("fish_model_variant", 1) > 0
}

// fishVariantSeed 取「决定雌雄」的身份串。
//
// 首选 `catch_guid`：同一尾鱼在溜鱼、结算、渔护、鱼卡、3/17 面板、3/2 纪录、
// 24/22 社交、资格鱼表里都带同一个 GUID，哈希结果必然一致。
//
// 但历史存档里有大量 `catch_guid` 为空的 `catch_log` 行（`CatchRecord.GUID` 直接来自
// `COALESCE(catch_guid, 空字符串)`。旧实现对这些行退化成 `rand.Intn(2)`，于是同一尾旧鱼
// 每刷新一次 3/17 / 3/2 / 社交纪录就可能换一次性别——正好破坏本改动要保证的
// 「同一尾鱼模型一致」。这里改用**内容种子**兜底：鱼种 + 克重 + 体长（毫米）。
// 这些字段全部来自同一条记录，四条读路径都拿得到，所以不依赖任何一侧补列。
//
// 取舍：同鱼种、同克重、同毫米体长的两尾不同鱼会共用族名。这两尾外观本来就一样，
// 比起「每刷新一次换性别」的可见故障，共用族名无害。
func fishVariantSeed(species string, weightG int64, lengthM float64, guid string) string {
	if g := strings.ToLower(strings.TrimSpace(guid)); g != "" && g != zeroGUID {
		return g
	}
	if species == "" || weightG <= 0 {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", strings.ToLower(species), weightG, int64(math.Round(lengthM*1000)))
}

// fishVariantHash 把「身份种子 + 用途盐」混成一个 64 位值。
//
// 不能直接拿 fnv 的低位取模：只差几个字符的 GUID（同一批次连号生成的那种）算出来的
// 低位高度相关，实测会出现「某套外观永远抽不到」「雌鱼全是一个样」这类偏斜。这里过一遍
// splitmix64 终结器把低位打散，再取模。
func fishVariantHash(seed, salt string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(seed))))
	_, _ = h.Write([]byte(salt))
	x := h.Sum64()
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// fishVariantMale 由身份串决定雌雄。
//
// 不能用 rand：同一尾鱼溜鱼时抽一次、结算/渔护再抽一次会错位，
// 出现「溜的是雄鱼、鱼护里变雌鱼」。对身份串哈希后四条读路径永远一致；
// 身份串为空（字段全缺）时**固定按雌鱼处理**，宁可少一种外观，也不要每次刷新都变。
func fishVariantMale(seed string) bool {
	if strings.TrimSpace(seed) == "" {
		return false
	}
	return fishVariantHash(seed, "|sex")&1 == 1
}

// fishVariantStageIndex 由身份种子决定用第几套外观（均匀取），**与体重无关**。
//
// 官服这类鱼的多套模型是随机出的：同一体重挡位既可能出 1 期也可能出 3 期。旧实现把
// 「入档线 → 蓝冠线」等分给各期，于是每个挡位永远是同一套（上星、蓝冠各固定一两种），
// 玩家一眼就能看出规律。改成哈希取模后，同一种鱼在同一挡位会分散到全部族上。
//
// 仍然用身份种子而不是 rand：同一尾鱼在溜鱼 / 结算 / 渔护 / 鱼卡四处必须拿到同一个族名。
// 盐用 "|model"，与决定雌雄的那一位互不相关，避免「雄鱼永远是 1 期」这类耦合。
func fishVariantStageIndex(seed string, n int) int {
	if n <= 0 {
		return 0
	}
	if strings.TrimSpace(seed) == "" {
		// 字段全缺的老数据：固定取第一套，宁可少一种外观，也不要每次刷新都变。
		return 0
	}
	return int(fishVariantHash(seed, "|model") % uint64(n))
}

// pickFishModelVariant 为这尾鱼挑一个客户端真实存在的模型族名。
//
// juvenileMaxM 是鱼苗族的体长上限：只有小鱼才允许用 parr / smolt，
// 否则渔护列表的缩略图会因为「鱼苗模型 + 成鱼尺寸槽」渲染成空白。
func pickFishModelVariant(species string, weightG int64, male bool, seed string,
	lengthM float64, parrMaxG, smoltMaxG int64, juvenileMaxM float64) string {
	v, ok := fishModelVariants[species]
	if !ok {
		return ""
	}
	if v.parr != "" && weightG > 0 && weightG <= parrMaxG && juvenileLengthOK(lengthM, juvenileMaxM) {
		return v.parr
	}
	if v.smolt != "" && weightG > 0 && weightG <= smoltMaxG && juvenileLengthOK(lengthM, juvenileMaxM) {
		return v.smolt
	}
	list := v.female
	if male {
		list = v.male
	}
	if len(list) == 0 {
		return ""
	}
	return list[fishVariantStageIndex(seed, len(list))]
}

// fishModelVariantForFish 是族名的唯一入口：同一尾鱼（GUID）在任何界面都得到同一个族名。
// 空串表示该鱼种没有变体族（或开关关闭），调用方按原样留空走默认模型。
func (s *Server) fishModelVariantForFish(species string, weightG int64, lengthM float64, grade int64, guid string) string {
	if s == nil || !s.fishModelVariantEnabled() {
		return ""
	}
	if _, ok := fishModelVariants[species]; !ok {
		return ""
	}
	parrMax := int64(s.tunableF("fish_variant_parr_max_g", defaultFishVariantParrMaxG))
	smoltMax := int64(s.tunableF("fish_variant_smolt_max_g", defaultFishVariantSmoltMaxG))
	juvenileMaxM := s.tunableF("fish_variant_juvenile_max_m", defaultFishVariantJuvenileMaxM)
	seed := fishVariantSeed(species, weightG, lengthM, guid)
	return pickFishModelVariant(species, weightG, fishVariantMale(seed), seed,
		lengthM, parrMax, smoltMax, juvenileMaxM)
}

// fishModelVariantFor 取这尾待结算鱼的族名，并回写到 RolledFish 上供日志核对。
func (s *Server) fishModelVariantFor(f *RolledFish) string {
	if f == nil {
		return ""
	}
	// 开关关闭要立刻生效：RolledFish 上的 ModelVariant 是缓存，直接复用会出现
	// 「fish_model_variant 已置 0，活跃的那尾鱼还挂着旧族名」（review P2）。
	if !s.fishModelVariantEnabled() {
		f.ModelVariant = ""
		return ""
	}
	if f.ModelVariant != "" {
		return f.ModelVariant
	}
	f.ModelVariant = s.fishModelVariantForFish(f.Species, f.WeightG, f.LengthM, f.Grade, f.FishGUID)
	return f.ModelVariant
}

// fillKeepnetVariant 给渔护条目补上族名（幂等：已算过就不再算）。
// 开关关闭时把缓存清掉，保证已加载的渔护条目也立刻回到鱼种默认族。
func (s *Server) fillKeepnetVariant(e *store.KeepnetEntry) {
	if e == nil {
		return
	}
	if !s.fishModelVariantEnabled() {
		e.ModelVariant = ""
		return
	}
	if e.ModelVariant != "" {
		return
	}
	e.ModelVariant = s.fishModelVariantForFish(e.Species, e.WeightG, e.LengthM, e.Cat, e.EchoGUID)
}

// recordModelField 是 320 纪录卡 f13（面板列表里的模型族）该写什么。
// 空族名保持 null，让客户端按旧行为走鱼种默认族；非空才写族名。
func recordModelField(modelKey string) *proto.Value {
	if modelKey == "" {
		return proto.Str(nil)
	}
	return proto.StrVal(modelKey)
}

// keepnetModelKey 渔护/结算里 25.f11 该写什么：有变体族就写族名（四个界面模型一致），
// 否则退回命名鱼的玩家名字（旧行为）。
func keepnetModelKey(e *store.KeepnetEntry) string {
	if e == nil {
		return ""
	}
	if e.ModelVariant != "" {
		return e.ModelVariant
	}
	return e.NameZh
}
