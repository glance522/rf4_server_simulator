# LEVEL_UP_GIFT_CARDS —— 升级礼物卡片机制（完整还原）

> 分析日期：2026-11-08 · 客户端基线 **4.0.25034**（IL2CPP）
> 证据源：GameAssembly IDA 反编译 + IL2CPP 反编译源码（il2csharp_full）+ monitor 抓包文档库
> 文中引用均为相对资料名/符号名/VA 地址，便于核对，不含本机路径。

---

## 1. 一句话结论

**升级礼物不是独立系统，而是"钓获结算弹窗"（GameResult）里的一个"礼物栈项"**：
服务器在放流（14/6→807）或收鱼（14/4→801）结算响应的 **811 数组**里塞一条
`种类=1(新级别)` 的条目并附上物品数组；客户端看到 `310.f1=本次升级数>0` 就把该条目的
物品渲染成卡片网格，标题 = 本地化文案格式化"当前等级"。

---

## 2. 协议层（服务端发什么）

### 2.1 命令 → 响应映射（COMMAND_RESPONSE_MAP 实测）

| 命令 | api | 响应类型 | 与升级礼物的关系 |
|---|---|---|---|
| `14/4` | fishing/end | **801** `deocnichdpi` | f4 = 811[]；f6 = args(121) 嵌 310 结算 |
| `14/6` | fishing/release_fish | **807** `bnininmemgm` | **f1 = 811[]**；f0 = 310 结算；f2 = 307 档案 |
| `9/1` `9/3` | crafting / mine2 | 803 `emifcdomieb` | 同样带物品数组 + 310 |
| `27/4` | RemoteSettings | 809 `llgjfoldbcc`(:800) | 携带 811[] + onobdmohahp[]（活动礼物配置预载） |

### 2.2 807 `bnininmemgm` 字段表（dump.cs:12529 / object_chains/fishing_gnilpafigmn.md）

| wire idx | 字段 | 类型 | 语义 |
|---|---|---|---|
| f0 | `fjjpipgimlc` | **310** `pblgcopjhff` | 技能/经验推进块（**f1=本次升级数**） |
| f1 | `jofdadhnlgd` | **811** `plhpmhpadcg[]` | **礼物/纪录行条目数组（卡片来源）** |
| f2 | `bjipldipkph` | **307** `pjhgpkgpjih` | 权威档案 |
| f3 | `iingicaldbk` | `onobdmohahp[]` | 基类入库物品数组 |
| f4 | `pbhkmflbgfb` | `onobdmohahp[]` | 基类入库物品数组 |
| f5 | `iaimladeglm` | `Guid[]` | 基类 guid 数组 |

### 2.3 811 `plhpmhpadcg` —— 礼物行条目（object_chains/fishing_bkmnmjcfbic.md）

| idx | 字段 | 类型 | 语义 |
|---|---|---|---|
| f0 | `fjfejmomhed` | **ehbdkjmbdee**(0..6) | 礼物行种类（见 3.3） |
| f1 | `kbobbfmmibp` | u32/int | 与种类对应的数值 |
| f2 | `nbkhfhenbag` | `onobdmohahp[]` | **礼物物品组 A（卡片）** |
| f3 | `fmpbnollnmf` | `onobdmohahp[]` | **礼物物品组 B（卡片）** |

- `onobdmohahp` 是**完整物品 wire 对象**（GUID / 状态位掩码 / 价格 / 品质 / 经验加成列表），
  即卡片上显示的道具本体。
- 该数组同样内嵌于 801.f4、809、310 等结果块，由 `RUIGameResultWidget` 逐条渲染成结算行。

### 2.4 触发开关：310 `pblgcopjhff` 的升级数

- `310.iamiobmnmlo`（wire f1）= **本次升级数**（九样本实测：拟鲤三连升=3；
  客户端技能点=1/级，<20 级时 f1==f5）。
- 同一字段的旧读法"破纪录标记"已被 7.11 九样本推翻。
- `310` 里还有：f0=本条鱼经验、f2=钓法大类、f3=熟练度增量、f6=经验加成分解。

---

## 3. 客户端逻辑（代码实证）

### 3.1 结果模型 `blehdkolhfl` 的构造器族（游戏结果数据）

| ctor | 数据来源 | source 值 | 说明 |
|---|---|---|---|
| `..ctor(bnininmemgm(807), lmeocpochih 鱼)` @`0x180D498A0` | **放流 807** | 1 | 鱼 → 单元素物品数组；811/310 原样带入（monitor 文档标注"放生方向"） |
| `..ctor(Guid, deocnichdpi(801), eiajfagcpfm)` @`0x180D4AF70` | **收鱼 801** | 0 / 2 | 有鱼条目详情→0；空竿→2；310 从鱼获条目内嵌取出 |
| `..ctor(ojdbfepnbea, emifcdomieb(803))` @`0x180D494F0` | 制作/挖矿 803 | 3 | 811 数组为 null |
| `..ctor(llgjfoldbcc(809))` @`0x180D49210` | RemoteSettings | 4 | 809 的 811[] 与物品数组直接带入 |
| `..ctor(igceagkmkbp)` @`0x180D49CF0` | 其他 | 5 | — |

> **陷阱**：`blehdkolhfl.fjfejmomhed`（ofikdpaaokd 枚举，结果来源）与 811 条目的
> `fjfejmomhed`（ehbdkjmbdee 枚举 0..6，礼物行种类）**同名不同义**，混淆器复用名。
> `kegidigbokk(source)` 的参数与匹配目标都是后者。

所有构造器尾部统一调用 `ljmgdbbnjke()`（配置解析，见 §4）。

### 3.2 弹窗与栈项组装

1. `RUIGameResultDialog.kjocncnjkli(blehdkolhfl, bool)` → 加载 prefab `dialogs/dialog_gresult`。
   弹窗条件 `gbcgnldfehm()`：`结果来源≠eifppolebdb ∥ 310.升级数>0 ∥ 811 数组非空`。
2. `RUIPlayerProgressModernWidget.offanbbdlcn`（il2csharp `RUIPlayerProgressModernWidget.cs:2226`）：
   **`310.升级数 > 0` 才尝试生成礼物栈**：
   ```csharp
   if (结算.升级数 > 0)
       if (data.kegidigbokk(ehbdkjmbdee.ffldihfeajm /*=1*/, out stack))
           加入栈项（prefab dialogs/player/stack/stack_item_gifts）
   ```
3. 礼物栈 → `RUIPlayerProgressStackItemGifts` → `RUIPlayerProgressGiftsTableExtension` 表格
   → `RUIGiftsWidget` / `RUIGiftTableCell` 逐件渲染卡片；播放音效 `cardGiftReceived`。
4. 同弹窗还有 Level / Exp / FreePoints(+N 技能点) / 纪录 栈项
   ——即抓包文档记载的"新级别 / 礼物 / +N技能点"三件套。

### 3.3 `kegidigbokk(ehbdkjmbdee)` —— 礼物行种类 → 标题 + 物品（blehdkolhfl.cs:1651+）

| source | 标题 | 物品来源 |
|---|---|---|
| 1 | `string.Format(Get("kDlg_Catched_GiftsNewLevel"), 玩家当前等级)` | 811 数组 f0==1 条目的 f2+f3 |
| 2 | `kDlg_Catched_GiftsGameEvent` | f0==2 |
| 3 | （空；资历节点走独立重载） | f0==3，按节点 systemIds 白名单过滤 |
| 4 | `limnkfhkgdm.mfndacckgec()`（动态） | f0==4 |
| 5 | `kDlg_Catched_GiftsTrophyCountReached` | f0==5 |
| 6 | `kDlg_Catched_GiftsTrophyTableCollected` | f0==6 |

- 收集函数 `gccplcodaba(source)`：遍历 `blehdkolhfl.jofdadhnlgd`（811[]），
  取 `f0 == source` 的条目，合并其 f2+f3 两组 `onobdmohahp[]`
  （闭包 `mjkjlgekabh`：`pSystemIds.Contains(item.systemId)` 白名单过滤）。
- 组装函数 `mlbdmnghfco(title, icon=70(cchlaplgnhd), items, out stack)`：
  **物品列表为空 → 不生成礼物行（卡片不出现）**。
- 资历节点重载 `kegidigbokk(pdmmfipdfkb)`：标题
  `kDlg_Gift_QualificationNodeDone` / `kDlg_Gift_QualificationEventNodeDone`，
  图标 0x36/0x37，物品按节点 `hjepmedfnni` 名称表过滤 source=3 条目。

### 3.4 等级取值

标题里的"当前等级"与配置过滤的"旧等级"都读全局游戏状态单例
（`nnfmkefpbdi` → +0x178 对象 → +0xb0 int = 玩家等级）；
`旧等级 = 当前等级 - 310.升级数`。

---

## 4. 配置侧路径（客户端本地，1/4 配置 blob）

`ljmgdbbnjke()`（所有 ctor 尾部调用，blehdkolhfl.cs:1155）：

```
if (310.升级数 >= 1) {
    levelFrom = 玩家等级 - 310.升级数;  levelTo = 玩家等级;   // 闭包 pofadojnfng
    entries = 配置JSON表
        .Where(cikghkmghae: cgfegllkfea.等级 ∈ (levelFrom, levelTo])
        .Where(hbdjhamgmnc: 平台字段通过 && 时间窗通过)
        .OrderBy(等级)
        .Select(JsonValue → cgfegllkfea)
        .ToArray();
    this.ieihhhbliod = entries;
}
```

- `cgfegllkfea`（配置态礼包，dump.cs:37834）：`fkjpeoodkfh`=lkbioeollij(11100) 礼物理由、
  `jbandpaaagm`=mmphincpfjn[]（args 15000：{物品定义[], 理由}）、5×u64 时间戳窗口、
  `dlmaaeecbci`(**等级 int** @0xA0)、`nhjofmmmngl`(平台标记 string @0x98)、`obifdihmcfn`(int[])。
- "1-10 级才有"的直接解释：**礼物等级表在服务器配置里只配了低等级**，
  客户端再按 `(旧级, 新级]` 区间过滤。
- 注意：`ieihhhbliod` 在现代弹窗组装路径上未见消费者（getter 无外部调用者），
  **主链是服务器 811 下发**；此配置解析疑似预留/旧路径，但可作为等级表存在的旁证。
- **另一套礼物 UI**：13020 `edihjhnglec` 字典（1/4 blob，f0=systemID、f1/f2=时间戳、
  f3={systemID,数量,bool}[]）→ 3/28 请求 → 13022 → `RUIGiftsExplorerPanel`
  （礼物面板/可领取列表，player_p2.md §11）。与升级弹窗互不相干。

---

## 5. 本地化键

| 键 | 用途 |
|---|---|
| `kDlg_Catched_GiftsNewLevel` | 礼物栈标题（source=1），带 {0}=等级 占位 |
| `kDlg_Catched_GiftsGameEvent` | 活动礼物（source=2） |
| `kDlg_Catched_GiftsTrophyCountReached` | 奖杯数达标（source=5） |
| `kDlg_Catched_GiftsTrophyTableCollected` | 图鉴收集（source=6） |
| `kDlg_Gift_QualificationNodeDone` / `...EventNodeDone` | 资历节点礼物 |
| `kDlg_Catched_NewLevelGifts` | limnkfhkgdm 属性（另一处升级礼物文案） |
| `kStorageSlotNameGifts` / `kStorageItemNameGift` / `kDlg_BtnName_GiftKeep` | 礼物存储槽/保留按钮（配套系统） |

---

## 6. go 服现状对照与复刻要点

### 6.1 现状

| 项 | 状态 | 位置（rf4_go_public） |
|---|---|---|
| 310.f1/f5 = 升级数 | ✅ 已发 | `internal/gameserver/econ.go`（build813）、`internal/gameserver/progress_ops.go:254` |
| "新级别/+N技能点"弹窗 | ✅ 由 310 驱动，已生效 | — |
| 807 响应 | ⚠️ 恒 `{null,null,307,null,null,null}`（照抄官服普通放流样本） | `internal/gameserver/econ.go`（MakeObj "gnilpafigmn"） |
| 升级礼物卡片 | ❌ 无 811 条目 → 客户端不显示卡片 | — |
| 旧奖励 | 已移除（测试断言升级不发金币/礼物邮件；tunable `level_rewards_enabled` 已失效） | `catch_lifecycle_regression_test.go:43` |

### 6.2 复刻官方效果（客户端零改动）

1. **等级表**：按 1..10 级配置礼物物品表（物品 systemID 必须可解析，否则卡片丢失）。
2. **升级时**（14/6 放流响应 807）：
   - f0 = 310（f1=升级数，保持现状）
   - **f1 = 811[]，填一条 `{f0:1(新级别), f1:新等级, f2:[礼物物品…], f3:[]}`**
   - f2 = 307 档案；f3（基类物品数组）按官方行为填实际入包物品
3. 14/4 收鱼方向同理填 **801.f4**（其余字段按 fishing_caagocpofin.md 服务端建议）。
4. 物品用完整 `onobdmohahp` wire 对象构造（GUID/数量/状态位），
   需与实际入库（库存推送）一致，避免"卡片显示但背包没有"。

---

## 7. 证据等级与开放问题

**[代码实证]**（IDA + 反编译源码，确定）：
§3 全部客户端逻辑、§4 配置解析、§5 本地化键、811 结构、807 字段表。

**[实测]**（官服抓包）：
`310.f1=升级数`（九样本）；官服 807 普通放流样本 6 字段全 null（均未升级）；
"新级别/礼物/+N技能点"弹窗由 310 两字段驱动。

**[开放问题]**
1. **升级时刻官服 807/801 的 811 非空样本缺失**——monitor 现有 4 条 807 样本均为
   未升级放流。复刻前应抓一次低级号升级放流，核对 f1 条目的 f0/f1/f2 真实填法
   与 f3（基类物品数组）是否同步携带礼物本体。
2. 811.f2 与 f3 两组物品的语义未定（普通/高级？普通/稀有？）——同一条目两组并存的
   官服样本缺失。
3. `ieihhhbliod`（配置解析产物）的真实消费者未定位——不影响主链，但决定"纯配置驱动"
   是否也可行（若官服升级时不带 811，卡片可能改由配置表渲染）。

---

## 8. 索引（符号 / VA / 行号）

| 项 | 值 |
|---|---|
| `blehdkolhfl`（结果模型） | dump.cs:141341；il2csharp `Assembly-CSharp/blehdkolhfl.cs` |
| ctor(807 放流, source=1) | VA `0x180D498A0` |
| ctor(801 收鱼, source=0/2) | VA `0x180D4AF70` |
| `kegidigbokk(ehbdkjmbdee)` | VA `0x180D4BD40`（blehdkolhfl.cs:1651+） |
| `gccplcodaba(ehbdkjmbdee, string[])` | VA `0x180D48C90` |
| `mlbdmnghfco` | VA `0x180D48FA0` |
| `ljmgdbbnjke`（配置解析） | VA `0x180D4A860`（blehdkolhfl.cs:1155） |
| 弹窗 `RUIGameResultDialog` | dump.cs:133572；prefab `dialogs/dialog_gresult` |
| 栈项组装 `offanbbdlcn` | il2csharp `RUIPlayerProgressModernWidget.cs:2226`（source=1 调用点 :2299） |
| 礼物栈 prefab | `dialogs/player/stack/stack_item_gifts` |
| 807 `bnininmemgm` | dump.cs:12529；读取器 VA `0x180E24120`；C→S 14/6 入口 VA `0x1805FC230` |
| 801 `deocnichdpi` | dump.cs:60884；读取器 VA `0x180CACE10` |
| 811 `plhpmhpadcg` | dump.cs:18676（旧名 bkmnmjcfbic）；读取器 VA `0x180F51760` |
| 310 `pblgcopjhff` | dump.cs:208887 |
| `ehbdkjmbdee`（礼物行种类 0..6） | dump.cs:208717 |
| `cgfegllkfea`（配置礼包） | dump.cs:37834 |
| `lkbioeollij`（配置礼物理由 11100） | dump.cs:68929 |
| `mmphincpfjn`（配置条目 15000） | dump.cs:174553 |
| `onobdmohahp`（物品 wire 对象） | dump.cs:297175 |
