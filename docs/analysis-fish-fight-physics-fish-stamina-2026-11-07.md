# RF4 搏鱼物理分析（3/4）：鱼的耐力（体力/stamina）机制

> 分析日期：2026-11-07
> 性质：**一次性分析快照**，非长期维护文档；行号以当日反编译树与仓库代码为准，后续提交可能漂移。
> 来源：AI 子代理逆向报告（Explore agent 逐类回读源码）；总览与问题清单见 [analysis-fish-fight-physics-2026-11-07.md](analysis-fish-fight-physics-2026-11-07.md)。
> 对象：`E:\RF4\cs\il2csharp_full\il2csharp_full\Assembly-CSharp\`（下称 `AC\`），辅助 `E:\RF4\cs\ida-docs\`。ida-docs 的行号基于旧反编译树（mono_src_old，本环境不存在），本文所有 `file:line` 均以本 dump 实测为准；docs 结论标注来源。

## 0. 关键类对照

| 混淆名 | 角色 | 位置 |
|---|---|---|
| `nmhcohgdjon` | **客户端鱼模型**（14/14 → fish model），体力状态都在这里 | `AC\nmhcohgdjon.cs` |
| `ackcocopopk` | tid 27 DTO(`gkfghccolil`) 的 XOR 混淆值包装器（f0~f21） | `AC\ackcocopopk.cs` |
| `Fish` | 鱼 MonoBehaviour，FixedUpdate 物理层 | `AC\RF4\Client\FishingScene\Fish.cs` |
| `gfgapohfogm` | 搏鱼中 AI 状态（state=2） | `AC\gfgapohfogm.cs` |
| `eabkpandbch`/`pipkjodjmli` 等 | 搏斗机动（曲线行驱动），基类 `paldjjcggdd` | `AC\eabkpandbch.cs` 等 |
| `llkfcijmgfd` | 曲线行→机动类工厂 + 加权选阶段 | `AC\llkfcijmgfd.cs` |
| `kejihbadeej` / `kldglnchdec` | 挣脱概率 / 脱钩计时（用 f20） | 同目录 |
| `BodyMotor` / `BodyEnvironment` | 鱼运动受阻比 / 入水深度比 | `AC\RF4\Client\BodyPhysics\` |

## 1. 鱼体力存在哪、f5/f6 如何被消费

**字段（`nmhcohgdjon`）**：

- `kdjlhmlpgan` (0x44) —— **累计疲劳**（obfuscated float），私有，唯一的积分器写入点在 `dpjclmgalfb`（`AC\nmhcohgdjon.cs:16` 声明，`:687-720` 实现）。公共只读属性 `pjpmochnmim`（`:341-356`）暴露它。
- `jfhacgcmocf` (0x6c) —— **疲劳容量**，由 f5 派生（`:25` 声明）。
- `llhelgdmpnf` (0x7c) —— **剩余力量比**（0.1~1），由疲劳/容量派生；公共属性 `akooinkapig`（`:27`、`:155-170`）。

**f5（odbiihjlfop，搏鱼体力池）的消费**（`AC\nmhcohgdjon.cs:660-674`，主构造器内）：

```csharp
eikjjhjpfca eikjjhjpfca1 = this.ejpfdimbeoj.odbiihjlfop;   // f5
...
float real2 = UnityEngine.Random.Range(0.7f, unknown) * real1;  // 容量 = rand(0.7..?) × f5
this.jfhacgcmocf = <encode real2>;                          // :671-673
this.dpjclmgalfb(0f);                                       // :674 初始化
```

- 即**容量 ≈ 0.7~1.0 × f5，不是恰好 f5**（上界 `unknown` 是反编译丢失的常量，疑为 1.0 —— 未确认）。
- 测试鱼分支直接 `odbiihjlfop = 1e+07f; oeflihacoij = 1.0f`（`:783-784`），docs 也记"测试鱼 1e7=无限体力"（`ida-docs\fields\fgogccpglhb.md:19`）。

**f6（oeflihacoij，初始体力比 0~1）的消费**：作为**疲劳积累速率的补数系数**进入积分器（见第 2 节公式），读取并 clamp01 在 `nmhcohgdjon.cs:696-701`。**客户端代码里不存在 `当前体力 = f5×f6` 这种表达式**；"f6=0.6 即 60% 起步"是服务端侧语义（docs：`fields/fgogccpglhb.md:20`）。客户端里 f6=1.0 ⇒ `(1-f6)=0` ⇒ 永不积累疲劳（这正是测试鱼无限体力的实现机制，`nmhcohgdjon.cs:783-784` + `:706`）。

> ⚠️ 总览补充（2026-11-07 交叉印证结论）：f6 的客户端真实语义是**抗疲劳系数**，不是"开局体力比"。服务端 `fishInitialStaminaRatio`（tunable `fish_initial_stamina_ratio`，默认官方样本 0.5628）若被按"开局体力比"理解去调参，方向会调反——调到 1.0 = 永不疲劳的鱼（等价测试鱼），不是"满体力开局"。

初始疲劳 `kdjlhmlpgan` 在构造器中被赋的常量被共享桩（`__SharedBodyStubs`）吞掉，**是否等于 `(1-f6)×容量`（即"按 f6 预支体力"）无法从本 dump 确认** —— 未确认；可确认的是构造器末尾调用 `dpjclmgalfb(0f)` 且力量比被钳在 [0.1,1]。

## 2. 消耗公式（唯一扣"体力"的地方）

**积分器**（`AC\nmhcohgdjon.cs:687-720`，`dpjclmgalfb(float ophgpncndog)`，全文摘录核心行）：

```csharp
float real1 = <decode kdjlhmlpgan>;              // 当前累计疲劳
float real3 = clamp01(<decode oeflihacoij>);     // f6, :696-701
float real4 = (1.0f - real3) * (ophgpncndog * 5.0f) + real1;   // :706 疲劳 += (1-f6)*5*param
this.kdjlhmlpgan = <encode real4>;               // :709
float real6 = 1.0f - (real4/real5) * (real4/real5);            // :711 real5=decode(jfhacgcmocf)=容量
float real7 = 0.1f > real6 ? 0.1f : (real6 <= 1.0f ? real6 : 1.0f);  // :713 钳 [0.1,1]
this.llhelgdmpnf = <encode real7>;               // :715 力量比 = 1-(疲劳/容量)^2
```

**每帧输入 param 的来源**（`AC\RF4\Client\FishingScene\Fish.cs:1217-1251`，方法 `nlchmjebogf(dt)`，由 `FixedUpdate` 驱动 `Fish.cs:2020-2038`，**仅在搏鱼状态 `eabpihljeli.ibhmnnlodmk(=2)` 时执行**，状态门在 `Fish.cs:1196`）：

```csharp
float real21 = 1.0f - this.hgckogiofnc.hlllcemfpbh;            // :1224 1-受阻比
float real22 = clamp(real21, 0.25f, 1f);                        // :1225 最低 0.25
float real23 = real22 * (real20 * this.hgckogiofnc.aciegmolcfm);// :1226 受阻比 × (期望速度×极速)
float real24 = this.lmkifikkfkn.kmnbkbgbpij();                  // :1234 钓组线力参数
float real26 = min(real23, real24 * 3.0f);                      // :1237 封顶 3×线力
float real27 = ... nklaengaiaf() ? real26 : real26 / 10.0f;     // :1238 线未绷紧时 ÷10
if (this.coffdfkgfaa.nbhppkkldpm.ajknpjhphkj) {                 // :1240 f7 标志(官服全为0)
    float real29 = clamp(dfndbpleelb.eiaglnkpokj, 0, 0.25f);    // :1243 入水深度比
    real30 = real29 * 300.0f + 1.0f;                            // :1244 1..76 倍
} else {
    real30 = clamp01(real1) * -0.14999998f + <常量丢失>;         // :1248-1249 ≈0.85~1（未确认常量，疑为 1）
}
this.coffdfkgfaa.dpjclmgalfb(real30 * real27 * bgholdfbpjj);    // :1251 param = 倍率×挣扎功率×dt
```

**物理含义整理**（每帧）：

```
疲劳 += (1 - f6) × 5 × 倍率real30 × 挣扎功率real27 × dt
挣扎功率 = clamp(1 - 实际速度/期望速度, 0.25, 1) × 期望速度×极速，上限 3×钓组线力（线不绷紧时再 ÷10）
```

- 什么行为扣体力：**不是按"冲刺/拉扯"事件扣，而是按运动受阻程度连续扣**。鱼被线拉住、顶线、想跑跑不动（实际速度 << 期望速度）时 `hlllcemfpbh→0`、受阻项→1，扣得最多；鱼自由游动时最低 0.25 系数。`hlllcemfpbh = clamp(实际速度/期望速度,0,1)` 定义在 `AC\RF4\Client\BodyPhysics\BodyMotor.cs:342-343`；`aciegmolcfm`（极速项）在 `:349`。
- **与线载/体重的关系**：扣量被钓组线力 `kmnbkbgbpij()` 的 3 倍封顶（`Fish.cs:1234-1237`）——重鱼拉力大、更常顶满封顶值；f8 本身只进线物理，不直接进疲劳公式。
- **与入水深度**：`eiaglnkpokj` 是**鱼体入水深度比**（由碰撞盒在水体边界内的位置算出，`AC\RF4\Client\BodyPhysics\BodyEnvironment.cs:446-533`，`1-eiaglnkpokj` 属性 `:53-60`），不是线张力。f7 分支下深水挣扎最多 76 倍消耗；f7 官服恒 0（`fields/fgogccpglhb.md:21`），实际走 else 分支（≈0.85~1 倍）。
- **是否由曲线行驱动**：**没有专门的 stamina drain 列**。19 列曲线行（`AC\jiefmbnjlcp.cs:5-23`，wire 顺序见 `ida-docs\fields\oiikpeaajln.md:14-32`）驱动的是机动的期望速度/时长/角度，从而间接决定"期望速度"项。最直接的"速度缩放"列是 **f14 `hkjohejlkgo`(0x40, 0..1)**：`lbakoabeoaf(鱼.jfhcjbbkalp() × row.hkjohejlkgo)`（`AC\eabkpandbch.cs:150, 436, 539, 552`）。

## 3. 恢复公式

- **本 dump 中不存在恢复路径**：`dpjclmgalfb` 全工程仅一处调用（`Fish.cs:1251`），且 `param ≥ 0`（else 分支即使常量取 0，也只是把速率降到 ~0，不会变负）；`kdjlhmlpgan` 无其他写入点。也就是说**客户端搏鱼模型里疲劳只增不减、力量比单调衰减**。`restPhase`（服务端曲线概念）在客户端没有对应的"回体力"代码 —— 未确认（不排除在共享桩丢失的方法体内）。
- 客户端存在的"休息"是**行为层**的：机动可让鱼停下（目标速度=0），此时受阻项×期望速度 ≈ 0，消耗自然≈0 —— 隐式省体力，但不回体力。
- **"体力越低冲刺越少"的耦合：存在，共 3 处**（都吃 `mhkoobkkifl()`，高置信推断其返回值就是力量比 `akooinkapig`/`llhelgdmpnf` 的解码，方法体被共享桩吞掉 —— 未确认）：
  - 趴底/停止概率（每秒）：`((1 - mhkoobkkifl()) * 0.2f + 1.0f) * (aacfnejgfej() * 0.05f * jlofffeogbi())` —— 力量越低越容易停（`AC\eabkpandbch.cs:780-783`）；
  - 起跑（逃窜）概率：`mhkoobkkifl() * 0.4f + 0.4f` —— 力量越低越难起跑（`AC\eabkpandbch.cs:919`）；
  - 机动中途放弃概率：`(1.0f - mhkoobkkifl()) * 0.15f`（`AC\pipkjodjmli.cs:153`）。
- 另有消耗侧的"越拉越轻"耦合：`Fish.cs:253-276` `kancgoiainm = clamp01(线速 / min(1, 力量比×极速) - 0.075)³`，力量比下降该值趋近 1（鱼被拖动的度量；本 dump 内无下游读者，未确认消费点）。

## 4. 体力归零意味着什么

- **客户端没有"体力=0"事件**：力量比钳底 0.1（`nmhcohgdjon.cs:713`），鱼永远不会被客户端判为"完全没电"。翻肚/停止表现为上面第 3 节的概率行为（`mhfcchepbie`/`ofiaigpneom` 标志，`eabkpandbch.cs:891-910, 947-951`）。
- **可收鱼判定与体力无关**：上岸触发是距离+收线进度条件集合 `keaadophhbp.cbhjcnkojgc`（`AC\keaadophhbp.cs:63-…`），子条件：
  - `agikabffcia.cs:26-40`：鱼高 `apajggddldc() < -0.2` 且水平距离 ≤ 0.801×范围 且收线进度 `fjamjbjmfdd() > 0.85`；
  - `bemnjjapbec.cs:29-…`：距离 ≤ 1.5×范围、进度 > 0.85、与玩家水平距 < 1.25。
  命中后 `Fish.hdakcoelhfa()` 切状态（调用点 `AC\RF4\Client\FishingScene\Fisher.cs:11900-11904`），另有收线进度 >0.92 的抄网标志（`Fisher.cs:11896-11897`）。
- 搏斗的"失败出口"是**挣脱/脱钩**而非力竭：`kejihbadeej.Update` 按累计概率掷骰 `fcalbphgmjk`（`AC\kejihbadeej.cs:275-281`），概率基数来自 **f20**（`:313-339`，见第 6 节），且搏斗 60 秒后安全余量按 smoothstep 衰减到 1.25%（`:120-149`）；AI 层 `gfgapohfogm.bachphcpjff` 汇总脱钩/挣脱原因并上报 14/12（`AC\gfgapohfogm.cs:230-265`）。
- 结论：**客户端不把"鱼没体力"作为收鱼条件**；体力的作用是把鱼的反抗（拉力表现、起跑、趴底）逐步压弱，服务端才是结算方（收鱼=玩家 14/5、结束=14/4，`FishingSet.cs:5107` 发 `(moabncmfjej != null ? 1 : 0)`）。

## 5. HUD 上的鱼体力条

- **未发现**。全 UI 目录（`RF4\Client\UI\`，含 `HUD\` 全部控件）没有任何控件绑定 `akooinkapig / kdjlhmlpgan / llhelgdmpnf / pjpmochnmim`（grep 零命中）。搏鱼 HUD 的张力/负载表现来自线/竿物理，不是鱼体力。
- 存在的是**玩家的**姿态/能量条：tid 308（DTO `pmdcpeegbbd` → 包装类 `gnjdcldedba`，`AC\gnjdcldedba.cs:19-31` 也有个同名混淆字段 `odbiihjlfop`，勿与鱼 f5 混淆），docs 注记"玩家姿态/能量条"（`ida-docs\fields\ghfbofgdflm.md:15`、`object_chains\player_pnbphpdmppe.md`）。
- 鱼体力在本 dump 的唯一"出客户端"通道是 **14/8 搏鱼快照上行**：`FishingSet.cs:6495-6523` 构造 `komaddgfhmo`(tid 21) 前读取当前鱼的 `didgkcmloig`（min 速度）、`kdjlhmlpgan`（疲劳）、`llhelgdmpnf`（力量比）三个值（`:6497-6507`），随后发送（`:6523`）——服务端可见但客户端 UI 不显示。docs 亦记 21 为纯上行、服务端无需解析（`ida-docs\object_chains\fishing_rest.md:146-178`）。

## 6. f9~f20 中与耐力相关的字段及消费点

本 dump 中 f9~f20 的直接消费者（其余只是存为 `nmhcohgdjon` 属性待机动类使用，方法体多被共享桩吞掉）：

| 字段 | 属性/消费 | 位置 | 与耐力关系 |
|---|---|---|---|
| f8 `fanpgngohlp` | 拉力参数 → 线物理 | 属性 `nmhcohgdjon.cs:250-265` | 不进疲劳公式；决定受阻封顶的实际吃紧程度（间接） |
| f9 `ehankclajep` | 属性 `:287-302`；docs：进曲线重建比较（`gokjlbkhfch.cs`，旧源码） | 本 dump 无直接消费者 | 未确认 |
| f10 `gjebjkpbigo` | 属性 `nkomlcnfnkd :460-475` | 本 dump 无直接消费者 | 未确认 |
| f11 `hlmajchojod` | 属性 `:325-340` | 同上 | 未确认 |
| f12 `cbbkdlclmfg` | 属性（clamp01）`:438-459` | 同上 | 未确认 |
| f13 `obephhmklbi` | 属性（clamp01）`:303-324`；docs：机动触发概率 `0.05×f13×…`（`eabkpandbch.cs:527` 旧源码）、`0.5×f13`（`hgeljkpnhne.cs:27`） | docs `fields/fgogccpglhb.md:72-75` | 行为激进度（间接耐力） |
| f14 `lcejaaenejf` | 属性（max0）`:186-202` | 无直接消费者 | 未确认 |
| f15 `hdbmdboaecp` | 属性（max0）`:203-219` | 无直接消费者 | 未确认 |
| f16 `ljamphapcmd` | 属性 `:220-235`；docs：`0.4+0.4×f16`、`0.15×(1-f16)` 概率（`eabkpandbch.cs:647`、`pipkjodjmli.cs:114` 旧源码） | docs `fgogccpglhb.md:77-81` | 行为激进度 |
| f17 `lodbpnehbnm` | 属性 `:107-122` | 无直接消费者（docs：曲线重建比较） | 未确认 |
| f18 `lmjpapepndf` | 属性 `:123-138` | 无直接消费者 | 未确认 |
| f19 `mgcgpgcjmil` | **实消费**：105 型机动随机特判 `mkkielhmcgn(f19)` → `emgklbgklki` | `AC\pipkjodjmli.cs:97-110`（读 `ackcocopopk` 偏移 0xdc） | 特殊动作概率（间接） |
| f20 `adiddjkcdan` | **实消费**：挣脱概率基数 `clamp01(f20 [×0.05 非漂钓])`，`逃脱力 = 1 - f20'` | `AC\kejihbadeej.cs:313-339`（基值）、`:275-281`（掷骰）、`:120-149`（60s 后衰减） | **决定"能遛多久会跑"，是耐力的服务端代理量** |
| f7 `ajknpjhphkj`（bool） | 消耗公式 76 倍分支开关 | `Fish.cs:1240-1249` | 官服恒 0；true 时深水消耗放大 |

**直接结论**：客户端疲劳积分器（第 2 节）只吃 **f5（容量）和 f6（速率系数）**；f9~f20 没有任何一个进入 `dpjclmgalfb`。与"耐久"最相关的是 **f20（挣脱基数）** 和 f13/f16/f19（机动激进度/概率）。

## 7. 明确标注"未确认"的事项汇总

1. `dpjclmgalfb` else 分支的丢失常量（`real30 = clamp01(·)×-0.15 + C`，C 疑为 1.0）——`Fish.cs:1249`。
2. 容量 `Random.Range(0.7f, X) × f5` 的上界 X（疑为 1.0）——`nmhcohgdjon.cs:667/816`。
3. 初始疲劳 `kdjlhmlpgan` 的构造值（是否 `= (1-f6)×容量`，即"f6=60% 起步"的客户端体现）——构造器 `:501-509` 被桩吞。
4. `mhkoobkkifl()` ≈ 解码 `llhelgdmpnf`（力量比）——依据是三处用法都在 [0,1] 且与"低体力→易停/难起跑"同向（`eabkpandbch.cs:783,919`、`pipkjodjmli.cs:153`），方法体丢失。
5. 客户端是否存在体力恢复（本 dump 证据：无；restPhase 无客户端对应代码）。
6. HUD 鱼体力条（本 dump 无绑定；不排除旧版本有）。
7. f9/f10/f11/f12/f14/f15/f17/f18 的具体行为语义（docs 旧源码有消费点，本 dump 相应方法体为共享桩）。

**给服务端模拟器的可直接复用结论**：客户端模型可抽象为 `fatigue += (1-f6)×5×rate×dt`（rate∈[0.25..1]×速度项，封顶 3×线力），`strength = clamp(1-(fatigue/(rand(0.7..1)×f5))², 0.1, 1)`；f6=1 即无限体力；strength 通过 `stopProb∝((1-strength)×0.2+1)`、`runProb∝(strength×0.4+0.4)` 反馈为"鱼越来越温顺"；客户端无恢复、无"力竭判负"，收鱼只看距离/收线进度。
