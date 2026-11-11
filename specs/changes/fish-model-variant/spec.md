# 鱼模型族下发（补齐多外观，同一尾鱼多处一致）

Status: ready

## Problem Statement

同一条鱼在客户端的不同界面显示成不同模型：溜鱼时是一个样子，收鱼结算弹窗和渔护里又是
另一套，稀有鱼面板（3/17）、统计面板（3/2）和任务/资格面板里的鱼干脆永远是同一个「老模型」。

原因不是资源缺失——客户端 `fishes` 资源包里每种鱼本来就有多个族（大西洋鲑有 `f1~f3` 雌、
`m1~m3` 雄、`parr`、`smolt`），每族都给了 small/medium/large 三套 body/mesh/skin。是服务端
从来没告诉客户端「用哪个族」：那几个字段一直是空的，客户端只能退回每个鱼种的**默认族**，
而默认族是历史遗留的混拼资源（`a_salmon` = `a_salmon_smolt_s` 皮肤 + `a_salmon_m3_m` 网格 +
`a_salmon_f3_b` 身体）。

## Solution

按鱼种选一个真实存在的族名下发：雌雄与「第几套外观」都由这尾鱼的身份串哈希决定，
**与体重、档位无关**——官服同一鱼种的多套模型是随机出的，同一挡位既可能出 1 期也可能出
3 期。同一尾鱼在溜鱼、结算、渔护、鱼卡、3/17 面板、3/2 统计、24/22 社交、资格鱼表里
得到同一个族名。

## User Stories

1. 作为玩家，我希望同一尾鱼在溜鱼、结算、渔护和各个面板里长得一样，不要换个界面就变个样。
2. 作为玩家，我希望大西洋鲑这类多模型的鱼在各个体重挡位都能钓到任意一套外观，而不是
   上星、蓝冠永远是固定的那一两种（2026-11-11 实机反馈）。
3. 边界：老存档里 `catch_guid` 为空的鱼，反复刷新面板也不会换性别。
4. 边界：运维把总开关置 `0` 之后，已经在线上的活跃鱼与已加载的渔护条目立刻回到默认族。
5. 边界：没有变体族的鱼种，字段保持 `null`（不能写空串，客户端对两者的兼容路径不同）。

## Implementation Decisions

- 映射与选择：`internal/gameserver/fish_variants.go`
  - `fishModelVariants`：鱼种 key → 可选族（雌/雄各一组，另加 `parr` / `smolt`）。
    注意美拟鲈的**鱼种 key 带点**（`a.sleeper`），资源族名才是 `a_sleeper`。
  - `fishVariantSeed`：取身份串。优先 `catch_guid`；为空时退回内容种子
    `鱼种|克重|体长毫米`；全缺时返回空串。
  - `fishVariantHash`：`fnv1a(身份串 + 用途盐)` 再过一遍 splitmix64 终结器。不能直接拿
    fnv 低位取模——同批次连号 GUID 只差几个字符，低位高度相关，实测会出现「某套外观永远
    抽不到」「雌鱼全是一个样」的偏斜。
  - `fishVariantMale`：`fishVariantHash(seed,"|sex")&1`。空身份串**固定按雌鱼**，不用 `rand`。
  - `fishVariantStageIndex`：`fishVariantHash(seed,"|model") % len(族列表)`，均匀取，
    **与体重无关**；盐不同，保证与雌雄不耦合。
  - `pickFishModelVariant`：`parr` / `smolt` 需同时满足体重阈值与体长 ≤ 0.35m
    （这两族三档尺寸槽全指向同一个 `*_s` 鱼苗模型，体长落到 medium/large 槽会让渔护列表
    缩略图渲染成空白）；成鱼从雌/雄列表里按 `fishVariantStageIndex` 取一套。
    不再读入档线/蓝冠线，`fishModelVariantLines` 缓存随之删除（渔护序列化不再查库）。
- 下发点（都是同一入口 `fishModelVariantForFish`）：27.f22（溜鱼）、25.f11/f12（结算/渔护，
  命名鱼把玩家名字让到 f12）、320.f13（3/17 与 3/2 纪录卡）、5005.f5/f6（任务/资格鱼表）、
  24/22.f11（社交纪录）。
- 开关：`fish_model_variant`（默认 1）。关闭时 `fishModelVariantFor` 与 `fillKeepnetVariant`
  会把已缓存的 `ModelVariant` 清空，保证改库 30 秒内生效；渔护条目只有内存缓存，
  库里不持久化这个字段。
- 数据与迁移：不改表、不改 `catch_log` 结构，纯读 + 计算。
- 兼容与回滚：客户端基线 4.0.25034。回滚把 `fish_model_variant` 置 `0` 即可（不动代码、
  不重启），所有下发点当次请求即回到默认族。

## Testing Decisions

- 纯逻辑（`fish_variants_test.go` / `fish_record_model_test.go`）：
  - `TestPickFishModelVariantJuvenileFamilies`：parr/smolt 的体重+体长门槛；7.7kg 成鱼体型
    必须走成鱼族（缩略图空白的回归点）。
  - `TestFishModelVariantIgnoresWeightBands`：同一体重（入档以上/上星/蓝冠三档）扫不同身份，
    六套外观都要出现——直接钉住「模型不再与档位绑定」。
  - `TestFishModelVariantSameSeedAnyWeight`：同一身份换任何体重都是同一套外观。
  - `TestFishModelVariantCoversMultiFamilySpecies`：大鳞麻哈鱼 5 套、大马哈鱼/奇努克鲑各 2 套全铺满。
  - `TestFishModelVariantStablePerFish`：同一 GUID 反复调用族名不变。
  - `TestFishVariantSeedFallbacks`：GUID 缺失走内容种子、全零 GUID 视为缺失、空种子固定雌鱼。
  - `TestFishModelVariantDottedSleeperKey`：`a.sleeper` 命中、`a_sleeper` 不命中。
- 协议/行为（真实 SQLite + 真实构造函数）：
  - `TestRecordCard320CarriesModelFamily` / `TestRecordCard320StableWithoutCatchGUID`：
    `catch_guid` 为空时 320.f13 反复读一致，且与 3/2 统计、渔护 25.f11、资格鱼表 5005.f5 逐字相同。
  - `TestFishModelKeySharedByEntityAndKeepnet`：27.f22 与 25.f11 同一个族名。
  - `TestFishModelVariantTunableOffClearsCache`：运行中置 0，活跃鱼与渔护条目的族名立刻清空。
  - `TestFishModelVariantOffKeepsOldWire`：关掉后 27.f22 / 25.f11 回到旧行为（名字）。
- 期望值来源：族名清单来自客户端资源包里的族名枚举 + 线上真实鱼（42.5kg 拉多加鲑等）；
  选族只断言「落在该鱼种的可选族集合里」「同一身份稳定」「同一挡位能铺满全部族」，不写死
  某个体重对应某个族——那正是这次要废掉的映射。
- 回滚后变红：把 `fishVariantSeed` 改回「空 GUID → rand」，
  `TestRecordCard320StableWithoutCatchGUID` 会随机失败；把开关判断挪回缓存之后，
  `TestFishModelVariantTunableOffClearsCache` 失败。

## Out of Scope

- 不给客户端新增族名：只使用资源包里已存在的族（`bs_salmon` 两族保留条目，但物种表里没有
  用这个 key 的鱼种，现在不会命中）。
- 不做「按天气/季节/产卵期切换外观」这类玩法逻辑：外观只看身份哈希。
- 不做「淘汰族」黑名单：`female` / `male` 列表本身就是可用族白名单，官方停用的族不进列表
  即永不出现。目前三套鲑鱼族都仍在用。
- 不往库里持久化 `ModelVariant`：渔护条目每次加载重算即可。

## Further Notes

- `[事实]` 客户端 `fishes` 资源包按族提供三套尺寸资源，取族字段在三个界面上同名
  （`oajeeklppia`），取不到就退回鱼种默认族。
- `[事实]` 默认族是历史混拼资源（`a_salmon` 那个组合），也就是玩家说的「淘汰的老模型」。
- `[事实]` 本服 `catch_log` 有大量 `catch_guid` 为空的旧行（`CatchRecord.GUID` 来自
  `COALESCE(catch_guid,'')`，没有兜底值）。
- `[事实]` 玩家 2026-11-11 实测：上星与蓝冠各固定一两种模型，与官服「同一挡位随机出各套
  外观」不符——旧的按体重分期即由此废弃。
- `[未验证]` 25.f11 / 5005.f5 的语义迁移只有服务端断言与既有实机观察，没有逐帧比对客户端
  实际读取结果；若客户端优先读 25.f12，命名鱼会退回名称、非命名鱼仍取族名。
