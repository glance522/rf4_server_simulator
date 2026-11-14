# 硬性鱼口门、食性双票与区域固定鱼种名册

Status: done

## Problem Statement

鱼口混乱：路亚（纺车）在水面能钓到泥鳅，用非食谱饵能中鱼，底栖鱼能在水面被钓获。
部署库实测四个生态层开关（`fish_ecology_enabled` 无后台入口、`fish_bait_rules_enabled`、
`fish_weather_enabled`、`fish_distribution_enabled`）全部默认关闭且 `bite_probability=1`，
判鱼退化为「全图名册按捕获计数加权随机」；即便全部打开，水层/食性也只是带下限的软乘子，
荒谬组合被压低但永不剔除，权重全零时 `weightedChoice` 还会退化成纯随机保出鱼。
生态食性表 `diet_json` 的唯一消费者是窝料，咬口判定根本不读它。
此外没有任何「这片水域固定出什么鱼」的区域级名册——40m 网格只是哈希散布系数。

## Solution

判鱼管线在水域鱼种覆盖之后插入资格裁定层，并新增管理员可编辑的区域名册：

1. **硬门 G1（拟饵×食性，硬剔除）**：钩上实际饵是拟饵（item_def 类别 17）时，
   `diet_json.lure < bite_gate_lure_diet_min`（默认 0.30）的鱼直接出候选。
   泥鳅 0.20/鲤族 0 不再攻亮片；狗鱼 2.07、江鳕 0.63、鲟族 0.30+（jig 钓鲟是真实玩法）照常。
2. **水层门 G2（软地板）**：目标水层超出 `[LayerMin−0.15, LayerMax+0.15]` 时权重乘
   `bite_gate_layer_floor`（默认 0.01），不剔除——深水鱼偶尔在浅水被钓起是有意保留的杂口。
   容差经 252 种全量核对：没有任何鱼种 `LayerMax < 0.78`，底钓（0.93）零误伤。
3. **食性双票 G-diet**：天然饵（类别 27）按饵类型映射到 diet 键（baitType 2/6/7→worm、
   3/4/14/15→fish、8/9/10/17→boilie、11/13→plant、12/16→crustacean、1→杂类任一），
   与攻略证据（fishing_bait_rules.txt / companion_bait_fish / loc_bait_fish）双票合成：
   双认原权重、单认 ×`bite_gate_diet_floor`（0.05）、零票或明确禁饵剔除。
4. **区域名册**：新表 `fish_zone`/`fish_zone_fish`（迁移 00060）。区域几何两种：
   内陆 40m 网格矩形（与 `fishArea()` 同一推导，含端点）/ 挪威海声呐区号。
   模式：`exclusive`（只出名单内鱼种 ∩ 地图名册）/ `boost`（名单内 ×boost×weight、
   名单外 ×keep_others=0.35 沿用海图观测语义）。多区域命中时独占优先、同模式取 id 最小。
   查鱼助手区域评分同源乘名册系数。未配置区域的水域完全不受影响。
5. **空候选语义**：门/名册把候选剔空时走「本轮无符合条件的鱼」重排，并且经
   `fishRollContext.gateBlocked` 压制内置鱼池兜底——门拦下的杆不捞外图鱼。
6. **鱼汛模式预设**：后台「鱼汛模式」二选一（legacy 五关 / realistic 五开，单事务原子写），
   单个开关仍可在鱼群规则条微调（mixed 状态如实显示）。

代码默认全部关闭（遵守「开关默认关闭不得静默改变现有手感」契约）；
部署侧由管理员一键切到「真实鱼汛」。

## User Stories

1. 作为玩家，我希望用亮片收线时永远不中泥鳅，以便「专业钓法钓专业的鱼」成立。
2. 作为玩家，我希望钓层离谱时（水面钓江鳕）只有极小概率杂口，以便保留偶发惊喜但不再荒谬。
3. 作为玩家，我希望玉米钓不到泥鳅（零票）但正常钓到鲫鱼（双票），以便饵料选择有意义。
4. 作为管理员，我希望在地图上划一块区域指定固定鱼种，以便做「湾口鲤区」「深坑鳕鱼区」这类玩法。
5. 边界：门/名册剔空候选 = 本轮无口重排（不是必出鱼、不从鱼池捞外图鱼）；
   GM 指定鱼（arm_next_fish）绕过全部判鱼不受影响；切回 legacy 后字节级回到旧手感。

## Implementation Decisions

- 模块：`internal/gameserver/fish_bite_gates.go`（新，G1/G2/G-diet）、`fish_zone.go`（新，
  名册缓存 30s TTL + 管理队列 `reload_fish_zones` 主动失效）、`fishing.go` 接线
  （`rollFishFromDBWithRules` C1 之后、`rollFishPoolsWithRules` 权重构建后）、
  `fish_query_regions.go`（区域评分乘名册系数）；`internal/store/repos_fish_zone.go`（新，
  CRUD）、`repos2.go`（`BiteModeSet`/`BiteModeFor`）；`internal/admin/fish_zones.go`（新，
  `/api/fish/zones` 族）+ `/api/fish/bite_mode`；后台 web 鱼群规则条加两开关、
  钓场页加「区域固定鱼种」「鱼汛模式」区块。
- tunables：`bite_gate_enabled`（默认 0）、`bite_gate_lure_diet_min`（0.30）、
  `bite_gate_layer_floor`（0.01）、`bite_gate_diet_min`（0.25，高于合成基线 0.2 噪声地板）、
  `bite_gate_diet_floor`（0.05）。游戏参数页可调，规则条可开关。
- 数据与迁移：`00060_fish_zone.sql`（`fish_zone` + `fish_zone_fish`，`IF NOT EXISTS` 可重复执行，
  已登记 `RuntimeTables`）；不动静态库；不动存档格式（`fishRollContext` 新增 `gateBlocked *bool`
  纯运行时字段）。
- 兼容：客户端对鱼种选择零语义校验（14/14 发什么渲染什么），服务端任意收紧不触协议；
  全部开关默认 0，未部署预设的部署行为不变；回滚 = 切回 legacy / 关 bite_gate_enabled。
- 测试：`fish_bite_gates_test.go`、`fish_zone_test.go`、admin `fish_zones_test.go`/
  `bite_mode_test.go`；既有套件全量回归（预存负载抖动清单内用例单跑全过）。

## Non-Goals

- 不改延迟链、概率掷骰、档位采样、嘬饵/保持率（阶段 A/B/D/E 原样）。
- 不修正 genfishdata 合成生态数据的噪声（如贝类 lure 0.59），门阈值只取在噪声带之上的线；
  数据修缮另开任务。
- 不做区域名册的静态基线预置（随包发布的默认区域），首版只提供后台编辑能力。
- 窝料、鱼猎、查鱼助手主链路的行为除区域评分外不变。
