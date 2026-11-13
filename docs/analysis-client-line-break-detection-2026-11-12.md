# 客户端"脱钩/断线"本地判定机制专项调研（2026-11-12）

> 本文是针对客户端反编译源码（`E:\RF4\cs\il2csharp_full`）的专项调研结论，聚焦一个问题：
> **玩家看到的"脱钩"到底是客户端判断还是服务器判断？客户端的判定逻辑是什么？**
> 实测对照数据（2026-11-12 中午模拟器会话）附于文末。
> 全链对照与根因汇总见 `docs/analysis-bite-hook-fight-line-break-2026-11-12.md`。

所有路径均在 `E:\RF4\cs\il2csharp_full\il2csharp_full\Assembly-CSharp\` 下（混淆名保留原样）。

---

## 1. 客户端"线断裂/渔具超载"本地判定（张力→断线完整公式链）

### 1.1 每帧入口与门控

- `RF4/Client/FishingScene/FishingSet.cs:1637` `LateUpdate()` → `gpnmjgkigmg(dt)`（`FishingSet.cs:3383`, RVA 0x5DE1C0）——渔具状态机每帧更新；在搏鱼/收线态里调用 `this.dncjeoomgco(bgholdfbpjj)`（`FishingSet.cs:3431`）。
- `dncjeoomgco(float dt)`（`FishingSet.cs:1652`, RVA 0x5D8550）——**断线判定的总入口**，三门控：
  - **门控 1**：`nibneamemnh()`（`FishingSet.cs:6626`）——定期判定窗口（打包定时器 `kpnmdjjdipp` 对比 `Time.realtimeSinceStartup`），且要求渔具状态 `opmmalbglpl == kieibhlheed.nnioeaijfic(=2)`（搏鱼态且挂着鱼）或 `== jfgjniaaopb(=3)`（收线态）。`kieibhlheed` 枚举：0=空闲 1=抛投 2=搏鱼 3=收线。
  - **门控 2**：`this.fjamjbjmfdd() > 0.5f`（`FishingSet.cs:1658`）——线"绷紧度/搏鱼强度"系数（鱼脑 `gpepdmafomg.cs:374-389` 也用它 0.5/0.9 阈值调节体力消耗）。**松线时不判定**。
  - **门控 3**（挂底支路）：`kckicokgbla` 异或反混淆值 == 0（`FishingSet.cs:1712-1713`）。

### 1.2 张力计算：鱼拉力 vs 各部件承力上限

**鱼拉力（分子）**：`this.faioflmebgi()`——当前作用在线上的鱼拉力，单位牛顿（`FishingSet.cs:1666/5867/6040`，HUD 平均值累计器 `hffmjocmdhh` 累的就是它，`FishingSet.cs:5518-5529`）。由客户端本地鱼物理产生（`hlgdagpgdij.cs`：水密度 1010、鱼密度 7800、`mbjoplnijhm = Physics.gravity`，静态构造 `hlgdagpgdij.cs:88-94`；`efobombfhom = max(fishForce*1.25, 鱼重kg)` 见 `lkgakdhinig.cs:85-95`）。

**部件承力上限（分母）**：`hmbjimllpme(dt)`（`FishingSet.cs:5851`, RVA 0x5E2F40）构造上下文 `hcmipdihcap`（`fbankbpbdbf.hpljojjdabn`）：
- `oiogkapnkhe = faioflmebgi()`（鱼拉力，`FishingSet.cs:5867-5868`）
- `lebphiopfpf` = 渔具是否在水中（5876-5887）
- `kfpmicloead = okncibgkfdd.elgalhmkkjb()`（竿相关强度，5888）
- `bdjbmheiicb` = 渔轮离合/自由出线状态（5891-5901）
- `oljonldfgff = onjllpnkklj()`（5902）

然后**遍历渔具链**（rig 的 `+0xb0/+0xa8` 组件表、组件间 `+0x50` next 指针，`FishingSet.cs:5904-5941`），对每个部件调虚方法（vtable slot 0x2e8）`ojnhkfehpie(context, dt, List<jaedpgkjgjn>)`，各部件把"我超载了"的候选塞进 `khjfefflelo` 列表：

| 部件 | 类 | 上限公式 | 候选 entry 过载系数 |
|---|---|---|---|
| **线** | `dgmaokfcpke.cs:404` (`ojnhkfehpie`) | `olgnlodanfm(flag)` = `线.nlmnpekipjc(线强度kg) × g`，自由线时 ×0.5（`dgmaokfcpke.cs:270-280`） | 1.5（411-412） |
| 子线 | `bjmacccgpho.cs:226` 同款 `olgnlodanfm` | 同上 | 1.5 |
| **渔轮** | `lkgakdhinig.cs:2493` (`ojnhkfehpie`) | `poadfcckmhj()`（**摩擦泄力上限**）；挂底(`khdiajkiehh`)时视为 ∞ | **1.75**（2506） |
| 钩 | `jbipdjaplgk.cs:253` | `item.nlmnpekipjc × g`（`acldbloonme`, `jbipdjaplgk.cs:169-177`） | 1.5 |
| 环/别针等 | `bccbpcnmnhf.cs:222` | `poadfcckmhj()` | 1.5 |
| 主线卷(剩线) | `annldpcofdn`（`egdhcnlmgeh` 分派 `effjdeblglh.cs:9989-10194`） | 折算到主线的 `poadfcckmhj()`（10042） | — |

统一分派器 `effjdeblglh.egdhcnlmgeh(item, freeFlag, pos)`（`effjdeblglh.cs:9989`）把每类的取法写死。

**entry 结构** `effjdeblglh.jaedpgkjgjn`（`effjdeblglh.cs:11600-11624`）：

```csharp
public readonly fbankbpbdbf lnipaakahco; // 0x10 断的部件句柄(→guid)
public float kflopmfokna;               // 0x18 该部件当前承力上限(N)
public readonly float fbenloglcie;      // 0x1c 过载斜坡系数(线1.5/轮1.75)
```

### 1.3 断线判定条件：瞬时越限 + 概率累积（混合模型，不是纯磨损条）

`FishingSet.cs:6016-6072`（仍在 `hmbjimllpme` 内）：

```csharp
// 6017: real4 = +∞ → 找 limit 最小的部件 num5
if (real6 /*F=faioflmebgi()*/ <= num5.limit /*+0x18*/) → 不断
else:
    real10 = clamp01((F - limit) / (limit*factor /*+0x1c*/ - limit))  // 过载率
    if (lphjocjojcp.mkkielhmcgn(real10 * 4.0f, 1.0f, dt)) return num5; // 概率断裂
```

概率函数 `lphjocjojcp.mkkielhmcgn`（`lphjocjojcp.cs:338`，单参版 `:156`：`value>=1 → true；否则 value >= Random.value`，三参版=按 dt 分步的随机尝试）——**过载率越高，每秒断裂概率越高（满过载≈4 次/秒的尝试率）**。

**摩擦泄力在哪折算**：渔轮上限 `poadfcckmhj()`（摩擦泄力设定+过热降额）；玩家泄力值 `pocnjfgklcp.kabahnodibh()`（kg）进上报 payload（`FishingSet.cs:1677-1681`），过热度 `lmjemmkiocc()` 用于 HUD 报警（`RUITorpedoIndicators.cs:742-758`，>0.75 红色 + `kNotification_DragOverheat`）。竿角度/水深不直接进断线公式，而是先汇入鱼拉力 `faioflmebgi()`（水下重量、深度、出线距离由 `hlgdagpgdij` 常量系在鱼物理里折算），再与上限比较。

**挂底磨断支路**（同方法 `FishingSet.cs:1716-1758`）：鱼"钉桩力" `real14`(clamp01) / 渔具实体 `+0xb8` 阈值 > 1 → `mkkielhmcgn(ratio*0.01, 3.0, dt)` → 异步 `cheagijhfoi(cb)` 断组请求 → 成功回调 `gakiebgkaoh`（`FishingSet.cs:2121`）弹 **kNotification_RigCutOffByFish**。手动版 `aflbiaanpla()`（`FishingSet.cs:1764`）。

### 1.4 14/8 快照字段（komaddgfhmo）与内部变量对应

结构体 `komaddgfhmo.cs`（25 字段），线序化顺序由 `cdhlhfejmai.cs:101-158`（`cmjcbaheigb`）给出：

```
f0 ikkddefdhih  f1 ncppnggkdlj  f2 apmkihobjlp   f3 efjpggkleba  f4 iaekglpehnb
f5 ekgecfgghfi  f6 gmpohjnnnfa  f7 hmnmoboplli   f8 jlfnnkjgjka  f9 olpdkpghckp
f10 canchjkioik f11 lommdacgmeo f12 dikdodjkina  f13 ojomaafpgni f14 lnpfikaonem(int)
f15 ikjnhgbjeno(int)            f16 bihpkfpmgbk  f17 mcaobgpnaoc f18 adnlhjbmkml
f19 cdhmoecfblh f20 hkgndoipdlc f21 emegkdcimkp(long 位掩码 fagdjklnnof)
f22 iepkaldameb f23 ednjempddap f24 kjfbhckmolp
```

填充处 `FishingSet.coaeplnbggb()`（`FishingSet.cs:6392`, RVA 0x5E51E0），来源是各滑动平均累计器（`cpcgfhjihco`）：

| 字段 | 累计器 | 内部量 |
|---|---|---|
| f2 | `hffmjocmdhh` | 累计 `faioflmebgi()`（鱼拉力）→ 平均（5518-5529）→ ÷g 折 kg 上报（1666-1669 同款） |
| **f6 / f0 类比值** | `oefbjaccdhc` | 累计 `faioflmebgi()/ekmmphnpjjl()`（5530，**张力比**，HUD 载荷条同款比例 `RUILoadIndicator.cs:36-40`，≥1.0 变红）；f0 上限 ~2.02 对应 clamp 通道 |
| — | `dmkpkhchhji` | 累计 `ekmmphnpjjl()`（当前渔具链**最小**承力上限，5498） |
| — | `ofhbinlenni` | 累计 `elgalhmkkjb()`（竿强度），再经渔轮 `ckoecdfmhbe()` 修正（5540-5543） |
| f7/f9 类 | `acailfjjknd` / `oiclljjgpjn` | 累计渔轮泄力 `kabahnodibh()`（5557-5566）/ 泄力档位 `(渔轮+0xd8)`（5568-5575） |
| — | `ahnkcheohpg` | 累计渔轮 `bjilbfpbldl()/mpamnfccfhf()/cfgoflbjahg()`（6470-6476）；鱼体 `max(0.035, fish.bjilibapfdi())`（6497） |
| — | `ejldhgkfjlc()` / `nhepenglhnb()` | 出线距离/深度 double（5519）/（5527） |
| **f4** | 渔具磨损系数 | 线组件 `aomkdnjcgji = wear × iejhpfpngbf × 0.07`（`dgmaokfcpke.cs:30-49`，`iejhpfpngbf` 由 `dficlmekoeo` 设 0.1~1）、剩线比 `kcgjlnmdbol()/pjkhmifjcng()`（`kldglnchdec.cs:67-73` 直接用它折算拉力） |
| **f13** | 线强度上限 | `olgnlodanfm` 的 `nlmnpekipjc×g` 折 kg（实测恒 ≈34.8） |

注：`coaeplnbggb` 中各字段赋值被反编译器折叠（6460-6530 区段出现 `komaddgfhmo1 = real29` 这类塌缩），以上为可辨认的累计器对应；发送点 `nnfmkefpbdi.caconpicefk.odpoajipkak(num4, guid, snapshot)`（`FishingSet.cs:6523`），2 秒定时再装填见 `FishingSet.cs:1618-1623/3878-3968`。

---

## 2. 17/9（渔具断裂上报）触发点与载荷

**完整调用链**（全部有文件:行号）：

1. 断裂判定返回最弱部件 → `FishingSet.cs:1708` `this.gmbjlhkaeik(entry.lnipaakahco, payload)`
2. `gmbjlhkaeik(fbankbpbdbf item, dboabijkbif payload)`（`FishingSet.cs:3034`, RVA 0x5DC7F0）：取 `item.canpbkiogjm...mgkfaekpnoj`（部件 category）放入回调闭包，调 `cnefjpnennb.midjpmhapnp(payload, brokenGuid, callback)`（3055）
3. `cnefjpnennb.midjpmhapnp`（`cnefjpnennb.cs:1153`, RVA 0x878910）→ `bbpmdcodcee.hikfdalijmn(玩家渔具实体guid, brokenItemGuid, payload, onSuccess, onError)`（1177）
4. `bbpmdcodcee.hikfdalijmn`（`bbpmdcodcee.cs:1714`, RVA 0x5F4420）→ 传输层 `kdnajnloahj.adlombejfmc(byte main, byte sub, object[] args)`（`kdnajnloahj.cs:1570`）。发送语句被反编译器塌缩为共享桩（1749 行），但同文件其余 17 系发送均可见字面量：`(17,8)`:5700、`(17,12)`:8657、`(17,13)`:4167、`(17,14)`:1806、`(17,15)`:1124——`hikfdalijmn` 的参数形状 [Guid 实体, Guid 断件, dboabijkbif] 与服务器日志 17/9（guid+快照）唯一对应，即 **17/9 发送点**。
   对照组：14/8 的字面量在 `bbpmdcodcee.cs:677`（`adlombejfmc(14, 8, [Guid, komaddgfhmo, int])`，函数 `odpoajipkak`:649，调用点 `FishingSet.cs:6523` + `deghjmihlcf.cs:6208`）。

**载荷 `dboabijkbif`**（`dboabijkbif.cs`，构造见 `FishingSet.cs:1664-1708`）：

| 偏移 | 字段 | 内容 |
|---|---|---|
| +0x10 | icdanpcjoch int | `(渔具状态==3)?1:0 + 1`（搏鱼=2） |
| +0x14 | kflopmfokna | **断件承力上限 kg** = `entry.kflopmfokna / g` |
| +0x18 | epfoccjnimj | **鱼拉力 kg** = `faioflmebgi() / g` |
| +0x1c | gcejfagalgi | `渔轮.ckoecdfmhbe(elgalhmkkjb())` |
| +0x20 | cdjnfjinpom | **泄力设定 kg** = `kabahnodibh()/g`（无轮=0） |
| +0x24 | eiaffflnfjc | `ejldhgkfjlc()` 出线距离（有轮再 `+hflelkknkfa()`） |
| +0x28 | badpjfpnfkp | `kkblmbgbdfj.labedbjihjk()` 位域 |
| +0x2c | danjplakmhh | 泄力档位 `(渔轮+0xd8)+1` |
| +0x30 | gilhgpedkhc | `onjllpnkklj()` bool |

**断的是哪件**：`hmbjimllpme` 选出的 **limit 最小的那一件**（各部件独立报名候选，谁的上限被鱼拉力突破且最小谁先断）。**category=11 = `oinpmjophkh.caehialblnl` = 线**（找线组件用 `edhgfkekdmg<dgmaokfcpke>(oinpmjophkh.caehialblnl)`，`FishingSet.cs:5945`；断线音效分派 `fmnckhiamjl` 中 `caehialblnl → bnadmcaekpn()`，`FishingSet.cs:2789-2807`；文案选择 `caehialblnl → kNotification_LineBroken`，`FishingSet.cs:6849-6860`；`lcdihnabclk.lgehlmjagie` 的 category-11 → `kItemState_LineIsBad`，`lcdihnabclk.cs:1668-1675`）。

**上报前后本地演出**：
- 请求成功回调 `icmffdlemgc.inigefidmam`（`FishingSet.cs:6796-6878`）：成功 → `lklhemjaeij()`（5595，渔具复位到 state0/摘鱼）+ 左侧提示条 `clllmakjgcg.nnfagicdine`：线 = **kNotification_LineBroken**（6855），其它部件 = `lgehlmjagie(itemType)` 的 `kItemState_RodBroken/ReelSpoolBroken/HookBroken...`（6859）→ `mlhbgncbdkc()`（6867）；失败 → 状态置 392478/890544。
- 音效/动画经 `fmnckhiamjl(oinpmjophkh)`（2789）分派：竿(3)→`ihlojgckimg`（`RodSounds.rodBreakAction` 断竿声 `Rod.cs:1129`）；轮(6)→`ammllfegfob`；线(11)→`bnadmcaekpn`（4066，= `Rod.fgjhejbpbnk()`，`Rod.cs:674-698`，随机播放 `lineBreakAction[]` 断线声）。
- 服务器随后下发聊天事件，聊天区显示：id5 `kDlg_Chat_EventRodBreak`、id4 `kDlg_Chat_EventReelBreak`、**id6 `kDlg_Chat_EventLineTore`**（`RUIChatStyle.cs:227/239/251`，事件类型枚举 `gconpmgilga.cs`）。

---

## 3. UI"脱钩"提示文案映射

**提示条（左侧滚动条）绘制器**：`clllmakjgcg.nnfagicdine(text, donpgkimldc.npjbloigmen)`（`clllmakjgcg.cs:159/230`）。本地弹出的相关 key：

| 场景 | 提示 key | 代码点 |
|---|---|---|
| 断线（17/9 本地回执） | **kNotification_LineBroken** | `FishingSet.cs:6855`（key 常量 `limnkfhkgdm.cs:2639`） |
| 割线/线切 | kNotification_LineCutOff | `FishingSet.cs:4171` |
| 鱼把组磨断（挂底支路） | kNotification_RigCutOffByFish | `FishingSet.cs:2136` |
| 部件级断裂 | kItemState_*Broken 族 | `lcdihnabclk.cs:1588-1700` |
| HUD 常驻：部件磨损到 20% | kNotification_TackleComponentUltimateDepreciation | `RUITorpedoIndicators.cs` :391 |
| HUD 常驻：泄力过热 | kNotification_DragOverheat | `RUITorpedoIndicators.cs:753` |
| HUD 常驻：剩线<5m | kNotification_LineOnReelAlmostOver | `RUITorpedoIndicators.cs:827` |

**聊天区事件（服务器发的聊天消息，"鱼跑了/脱钩"在这里）**：id7 → **kDlg_Chat_EventFishLeft**（`RUIChatStyle.cs:263`），id8 → kDlg_Chat_EventRigHitched（:275），id9 → kDlg_Chat_EventLineCut（:287），id10 → kDlg_Chat_EventRigCutOffByFish（:299）。本地代码中没有"鱼跑了"的提示条调用点。

**14/12 是客户端→服务器的搏鱼结束报告**（发送 `bbpmdcodcee.efhpjiloomi`，`bbpmdcodcee.cs:6692`，`adlombejfmc(14, 12, [渔具guid, 鱼guid, boekjmipdgb])` :6702；组装于 `deghjmihlcf.bjkkdngcmfm`，`deghjmihlcf.cs:4875-5060`）。载荷 `boekjmipdgb.cs`：f0 = `badpjfpnfkp : gnfinieaahc`（`gnfinieaahc.cs`）：

| f0 | 含义 | 代码点 | hooked |
|---|---|---|---|
| 0 | 仍在搏鱼（状态 2/3） | `naihiiaooka.cs:246-249` | 现场值 |
| 1 | **线松弛/张力归零**（`kldglnchdec` 0.5~2s 随机计时器到点） | `gfgapohfogm.cs:236-238`，`kldglnchdec.cs:96-101` | — |
| 2 | **钩松脱**（`kejihbadeej` 挂钩保持进度衰减到 0；衰减率随搏鱼时长 60s→3600s 二次缓动下降、随鱼重/收线速度加快，`kejihbadeej.cs:60-100/254-340`） | `gfgapohfogm.cs:241-244` | true |
| 3 | 渔具已不在搏鱼态/被收走 | `deghjmihlcf.cs:4971/5011`，`naihiiaooka.cs:233-235` | false/现场 |
| 5 | 钩 guid 与当前渔具不匹配（鱼不在本钩上；断线重建装备树后的下游回执） | `naihiiaooka.cs:247` | false |
| 6 | 渔具实体丢失 / 钓组上已有鱼未清 | `gfgapohfogm.cs:258-260`，`deghjmihlcf.cs:4884/4895` | false |
| 7 | rig 内部组件空 | `naihiiaooka.cs:237-239` | — |
| 8 | 放流/脱逃流程完成 | `pmlepacfcfm.cs:47` | false |

**f0=5 回执不会触发第二条本地提示**：`bjkkdngcmfm` 是 void 发送、无成功回调；后续表现完全取决于服务器推送，客户端不会因 f0=5 再弹本地文案。

**`lbkpigfelil.beljepjjmmk(hcjjamcnfba.xxx)`（`lbkpigfelil.cs:108`）是新手引导提示分发器**（RUIHints），不是事件提示。例：`gfflcngjgma.cs:207-226`——"减摩擦"引导：张力比 `faioflmebgi()/ekmmphnpjjl()` 落在 0.5~0.75 且泄力档位 <30 时弹 `hcjjamcnfba.fppbknfpjaj(=8)`（hint.reducefriction）。`hcjjamcnfba` 14 个值 = 引导 ID（`hcjjamcnfba.cs`）。

**结论**：玩家看到断线时的直接提示是**"线断/渔具断裂"**（kNotification_LineBroken + 聊天 kDlg_Chat_EventLineTore）；**"脱钩/鱼跑了"（kDlg_Chat_EventFishLeft）只在鱼因张力归零(f0=1)或钩松脱(f0=2)离开、由服务器发聊天事件时出现**——两者在 UI 上可区分。

---

## 4. 服务器主动让鱼脱钩的下行消息

main=14 的接收处理器：`gdpondeemjf : hblakcbejpa`（`gdpondeemjf.cs:3-6`，`liighppacmp == 14`），分发 `pagenilfpij(conn, sub, args)`（`gdpondeemjf.cs:17-111`）：

- **sub=14**（服务器推送鱼设置 `gkfghccolil`）：args=[渔具guid, gkfghccolil] → `kgcojlddoli.jhleifkgdie()`（`gdpondeemjf.cs:114-146`）→ `deghjmihlcf.bjkkdngcmfm(fishingGearId, fishSetup)`（`deghjmihlcf.cs:4875`）：客户端按**本地状态**计算 `gnfinieaahc` 原因（找不到渔具→6；渔具不在搏鱼态→3，`deghjmihlcf.cs:4945-5011`）并**立即回发 14/12**。即服务器"询问/裁决"循环：推 14/14 → 客户端回 14/12(原因) → 服务器据此结算。
- **sub=2**：实体状态更新（`gdpondeemjf.cs:70-100` → `dnhipfkdfmb(args[0])`）。
- **其余所有 sub（服务器强制放鱼）**：`iebkfdalalb.ncnnmbljdmd()`（`gdpondeemjf.cs:154-176`）→ `deghjmihlcf.aglphhgicmi(fishGuid)` 找到该鱼所在渔具 → `rig.lennnlocboa()` → `Fish.lennnlocboa()`（`RF4/Client/FishingScene/Fish.cs:2045-2051`, RVA 0xB73EE0）：若鱼 FSM 状态 ∈ {1,2}（咬钩/搏鱼），**强制切到 `eabpihljeli.bfbanpjgkdb(=4)`（逃逸游走态）**——服务器强制脱钩放鱼；客户端不做任何提示条演出，事后由服务器的聊天事件（FishLeft）呈现。

触发条件（服务器侧）无法从客户端代码看出，但客户端契约明确：服务器可随时推"放鱼"指令，客户端无条件执行；同时服务器可用 14/14 主动查询，客户端用本地公式链（第 1 节）自证断线或脱钩原因回 14/12。

---

## 5. 结论（对照实测）

1. **断线是客户端本地判定**：`F > min(各部件 limit)` 才产生候选，断裂概率 = 过载率 ×4/秒（`FishingSet.cs:6072`），断件 guid = 上限最小的部件 → 17/9。服务器被动确认（模拟器日志原文"17/9 按客户端上报断件"）。
2. **"脱钩/鱼跑了"提示只在鱼咬钩之后**：断线判定有搏鱼/收线态门控；f0=1（张力归零）/f0=2（钩松脱）也必是咬钩后。f0=8 = 没咬就走。
3. 14/8 实测对照（2026-11-12 中午会话）：
   - 成功鱼 f6（张力比）≤0.59 → 瞬时不过限 → 100s 入护
   - 断线鱼 f6 反复 0.7~0.898（滑动平均接近 1 = 瞬时峰值多次越限；线磨损使实际 limit 下探）→ 66s 掷中断裂
   - 刺鱼（14/9）冲击 + 外冲 → 中鱼 3.9s 瞬断
   - 两例断件都是线（category=11，同一根主线 guid 9367778b）
4. 客户端内部还有"减摩擦"新手引导（张力比 0.5~0.75 且泄力档位<30 时弹 hint.reducefriction）——说明官方设计预期玩家在此张力区间主动减压；模拟器曲线池外冲拉力偏高（官方真值仅为线强度 10~30%）会让玩家长期处在引导区间之上。

---

## 附：相关文档与数据索引

- 全链分析（咬钩→搏鱼→断线 + 根因汇总）：`docs/analysis-bite-hook-fight-line-break-2026-11-12.md`
- 官方黑草抓包：`logs/captures/diaodiao2/`（`dd2_1414_950.hex` = 27 对象真值 + 13 条曲线；`dd2_422_*.hex` = 搏鱼期装备快照）
- 模拟器实测日志（2026-11-12 13:10~13:45 会话，含 17/9 断线逐帧时序）：`D:\QQFiles\858037698\FileRecv\gameserver - 副本.log`（含登录态，勿外传）
- 曲线池数据：`internal/gameserver/assets/fight_curves_sets.json`
