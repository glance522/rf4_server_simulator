# 严格吃饵档位：让鱼按现有鱼饵偏好咬钩

Status: done

## Problem Statement

玩家用苍蝇 / 红虫这类虫饵、20 号小钩钓小型鱼时，会被 21kg 的狗鱼咬钩并崩掉钩部件。玩家期望「鱼不乱吃饵」：掠食鱼不该吃虫饵，鱼口应当遵循项目里已有的鱼饵偏好数据。

排查后确认这不是某个参数没生效，而是判鱼语义与直觉不符：

- 后台「鱼类吃饵偏好」（`fish_bait_rules_enabled`）当前是开着的，但 `filterFishBaitSet` 的放行条件是**并集**：规则表写了「吃」、`companion_bait_fish` 有实证记录、`loc_bait_fish` 有本地渔获，三者任一命中都拿同样的满权重（`1+0.5×variation`），此外还有一条食性族内的低权重杂口。规则表没写「吃」并不剔除鱼。
- 规则表里 4 条「各类饵」被 `matches` 硬编码为无条件匹配，狗鱼因此把虫饵当正常食物。
- `companion_bait_fish` 覆盖 31231 行，`fly` 挂 100 种鱼、`bloodworm` 挂 120 种。Go 探针逐条匹配 252 条规则实测：苍蝇饵约 186 种鱼可咬，其中只有 46 种是规则表允许的，56 种纯靠实证放行、84 种靠杂口；红虫饵约 206 种、规则允许 47 种。
- 项目里还有一张 `fish_bait_pref`（wiki 每种鱼爱吃的饵，778 行 / 209 鱼种）完全没接进判鱼，只在完整性检查的表清单里出现过。

结果是「严格模式」名不副实，玩家无法通过后台参数让鱼按偏好吃饵；关掉严格模式又会一次废掉 252 条硬规则，代价过大。

## Solution

在保留 `fish_bait_rules_enabled` 两条通道的前提下，新增一个可调的「严格吃饵档位」。玩家在后台把档位从 0 调到 1 后，只有规则表（或后台覆盖、轮换组）允许的饵才有正常鱼口；规则外但有实证的搭配降为极低概率杂口，档位 2 则完全只认规则。规则表里「各类饵」在档位 >0 时按该鱼在 `fish_bait_pref` 里的偏好饵 + 拟饵类收窄，狗鱼吃虫饵这类穿越不再发生。档位默认 0，不打开时逐点等价于改动前。

## User Stories

1. 作为玩家，我希望用虫饵时不再被狗鱼咬钩，以便小钩细线钓小鱼时不被无关鱼种崩掉线组。
2. 作为玩家，我希望用对口拟饵时狗鱼仍照常咬钩，以便收窄不影响正常路亚玩法。
3. 作为运维，我希望在后台一键切换三档、把「规则外杂口」调到 0，以便按实机反馈微调而不必重编译。
4. 边界：档位 0 时所有旧行为与旧测试结果不变；`fish_bait_pref` 无记录或表缺失时保留「各类饵」原样，不擅自封鱼；档位 1/2 下 deny、「底钓只吃」、前置条件、轮换 strict 组的剔除能力不减弱。
5. 边界：档位收紧后苍蝇仍可钓约 42 种、红虫约 43 种规则允许的鱼，不会整片空竿。

## Implementation Decisions

- `internal/gameserver/fishing_bait_rules.go`（`filterFishBaitSet`）是唯一判鱼接缝：在循环外读一次档位与强度参数，档位 0 走原表达式一字不改；档位 >0 时把放行拆成「允许 → 满权重 × 偏好系数」与「规则外 → 低杂口」两段，档位 2 关掉后者。
- 新增 `fish_bait_strictness`（0/1/2，默认 0）与 `fish_bait_alt_bite`（0~0.5，默认 0.05，仅档位 1 生效）。两个键都走既有 tunables 30 秒缓存，不改表结构、不加迁移。
- `bait_pref_scale` 在档位 >0 时作为「实证偏好系数」的强度（0=中性、1=标准、2=加倍），档位系数温和（tier3 ×1.5 / tier2 ×1.25 / 在列 ×1.10）。非 strict 通道的 C8/C9 语义与 `fishing.go` 的 `if !strict` 分支不动，避免破坏旧档手感与既有断言。
- 「各类饵」收窄不在 `ruleBait.matches` 里做（它拿不到鱼种上下文），改在 `filterFishBaitSet` 里构造有效 allow 列表：把「各类饵」替换为「拟饵」+ 该鱼 `fish_bait_pref` 的偏好项；偏好表无记录时原样返回。
- `internal/store` 新增只读 `FishBaitPrefs(ctx, fishKey)`；`internal/gameserver/fishing_roll_cache.go` 增加按鱼种的一次判鱼会话内缓存。查询失败返回空列表，判鱼侧退回内置规则，旧库升级不受影响。
- `internal/admin/api.go` 对两个新键做入口范围校验；`internal/admin/web/index.html` 登记到游戏参数页并修正 `bait_pref_scale` 原先「按饵料偏好筛鱼」的误导文案。
- 不改协议、不改命令响应形状、不改表结构、不改迁移、不改经济/结算/权限链路。
- 回滚：后台把 `fish_bait_strictness` 设回 0 即刻回到旧手感（免重编译）；代码回滚时删除两个新键的读取分支即可，档位 0 分支与改动前逐点等价。
- 兼容：`fish_bait_rules_enabled=0` 时整个 `filterFishBaitSet` 不被调用，软加权通道完全不受本次改动影响。

## Testing Decisions

- 单元 + 真实 SQLite（`seedBaitRuleTest` 同一接缝，`internal/gameserver/fishing_bait_strictness_test.go`）：档位 0 复现「实证补放行」满权重；档位 1 断言狗鱼+虫饵不咬、狗鱼+拟饵仍咬、规则外实证只拿杂口权重；档位 2 断言规则外实证完全不咬；`fish_bait_alt_bite=0` 与 `bait_pref_scale=0` 的中性/关闭行为各一条。
- 后台入口（`internal/admin`）：两个新键越界值返回 400、边界值返回 200。
- 期望值来自手算：`maxBaitPreference=1.5×1.15`、`altBite=0.05` 时杂口权重 = `1.725×0.05=0.08625`；偏好系数按 tier 手算，不用同一函数再算一遍。
- 回滚后哪些测试会变红：新增的档位 1/2 断言全部变红；`fishing_bait_rules_test.go` 的「实钓证据可补充未列出的搭配」与 `fishing_bait_alternatives_test.go` 的 5% 杂口断言在档位 0 下必须保持全绿，作为兼容回归。
- 门禁：`go test ./...`、`go vet ./...`、`go build ./...`、`go run ./tools/doccheck`；实机以 `14/14 发鱼` 的鱼种与后台档位参数做一次 A/B。

## Out of Scope

- 不重写 252 条规则表，不改「各类饵鱼」等其它类别词。
- 不改钩号/线强度参与派鱼的门槛（`hook_entry_cap` 仍是官方标注的假想模型）。
- 不把 `fish_bait_pref` 当作全表白名单替换规则表；它只用于收窄「各类饵」这 4 条。
- 不做后台批量生成 `fish_bait_override` 的功能，不新增协议字段。
- 不改非 strict 软加权通道，不调整天气、欢乐时光、窝料等其它权重层。

## Further Notes

- `[事实]` Go 探针逐条匹配 252 条规则 × 真实饵定义：苍蝇 allow 46 / 实证独有 56 / 杂口 84 / 通配 4；红虫 allow 47 / 实证独有 76 / 杂口 83 / 通配 4。
- `[事实]` `fish_bait_pref` 主库与静态库各 778 行；4 条「各类饵」鱼均有偏好记录：`n.pike` 5 条全是拟饵系列，`crawfish` / `dreissena` / `freshwater_mussel` 含虫饵与植物饵。
- `[事实]` `specs/capabilities/bite-bait-and-spawn.md` §3 要求「限制策略要可配，开关默认关闭时不得静默改变现有手感」，且禁止把偏好表写死在判定分支里；本次以 tunables + 数据查询满足。
- `[事实]` `fishing_bait_rules_test.go` 明确断言「实钓证据可补充未列出的搭配，但不能绕过明确不吃拟饵的规则」，因此档位 0 的原分支必须保留。
- `[推断]` 「各类饵」在攻略语境里指拟饵几乎通吃，而非虫饵通吃；收窄时保留「拟饵」类别以维持既有路亚手感。
- `[假设]` 档位 1 的杂口默认值 0.05 与现有 `legalAlternative` 权重一致，玩家感知为「偶尔还有杂口」；强度由 `fish_bait_alt_bite` 控制，可下调到 0 完全关闭。
- `[未验证]` 三档在实机长时段鱼口分布上的差异，需以 `14/14 发鱼` 的鱼种对照；档位 0 与改动前逐点等价已由自动化测试覆盖。
