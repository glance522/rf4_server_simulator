# RF4 搏鱼物理分析（4/4）：玩家耐力/体力条（308.f0）

> 分析日期：2026-11-07
> 性质：**一次性分析快照**，非长期维护文档；行号以当日反编译树与仓库代码为准，后续提交可能漂移。
> 来源：AI 子代理逆向报告（Explore agent 逐类回读源码），服务端现状段已按 `fix/settled-catch-guard`（d30e45b）核对；总览与问题清单见 [analysis-fish-fight-physics-2026-11-07.md](analysis-fish-fight-physics-2026-11-07.md)。
> 对象：`E:\RF4\cs\il2csharp_full\il2csharp_full\Assembly-CSharp\` + `E:\RF4\cs\ida-docs\` + 本仓库 `internal/gameserver`。

## 0. 一句话总结

**玩家耐力（Energy）= 308 命令 f0（`pmdcpeegbbd.odbiihjlfop`，0..1 浮点），纯服务端权威、每 ~15s 覆盖下发；客户端只有"显示与 10 秒平滑"，没有任何本地消耗/恢复公式，也不上报能量值。** 搏鱼动作（摇轮/顶竿/拉拽）在客户端不改能量条；客户端上报的是物理量（14/7 杆态、14/8 快照、14/9 拉拽、3/9 活动统计），能量结算是服务端拿这些输入自己算的。竿弯量（`dhehnkcgonh`）影响的是**装备侧竿载上限**，与玩家耐力无代码关联。

## 1. 字段定位：308 的哪个 f 是耐力？取值范围？谁消费？

### 1.1 308 DTO 内存布局与 wire 序号对应

`pmdcpeegbbd.cs:5-13`（字段偏移）+ `pnbphpdmppe.cs:37-63`（读取器，按 wire 顺序 f0..f8 依次写入）：

| wire | cs 名 | DTO 偏移 | 语义（已证实） | 官服实测（n=268，~15s/条 ≈ 67 分钟会话） |
|---|---|---|---|---|
| f0 | `odbiihjlfop` | +0x30 | **玩家能量条 energy** | [0.830159, 0.85]（一小时会话内 0.85→0.83 缓降） |
| f1 | `odmpgbjjlnc` | +0x10 | 饥饿 hunger | [0.874274, 1] |
| f2 | `gcljiimpfdm` | +0x1c | 未消费（恒 0） | [0,0] |
| f3 | `pfmkddjjegc` | +0x24 | 舒适 comfort | [1,1] |
| f4 | `clccbkjffpd` | +0x14 | 幸运（HUD 显示 **1−f4**） | [0,0] → 幸运条=1 |
| f5 | `afmboidbbaj` | +0x18 | 非进度条；进 3 个物理量 getter（见 §6 备注） | 恒 11.375 |
| f6 | `cogbdbniaop` | +0x20 | 未消费（恒 0） | [0,0] |
| f7 | `kjfbhckmolp` | +0x28 | 世界时钟 FILETIME | — |
| f8 | `gnijkfnhcbl` | +0x34 | 语义未定（写静态+48） | 官服=0 |

读取器 `pnbphpdmppe.plphdfehkjl`（`pnbphpdmppe.cs:44-60`）原文顺序：`+0x30 ← f0; +0x10 ← f1; +0x1c ← f2; +0x24 ← f3; +0x14 ← f4; +0x18 ← f5; +0x20 ← f6; +0x28 ← f7(u64); +0x34 ← f8`；`fkibigfplgc` 定长 40 字节（`pnbphpdmppe.cs:77-80`）。

### 1.2 消费链（全部只读，共 4 个 UI/逻辑入口）

1. **308 处理器** `hncnmlgnhjm.ojhmpipmhlo(pmdcpeegbbd)` — `hncnmlgnhjm.cs:7317`（VA 0x180699450，即 push 2/2；另由 307.f12 档案在 `hncnmlgnhjm.cs:1444/3132` 调用）。行为：写入模型 `gnjdcldedba.canpbkiogjm` → 重建逐通道变化检测器 `fananjfdjbb` → f8 写 `nnfmkefpbdi.ghnkjbondpb.gnijkfnhcbl`（:7383）→ f7 `DateTime.FromFileTimeUtc` 写世界时钟（:7386）→ 派发 UI 失效。
2. **HUD 能量条** `RF4/Client/UI/HUD/RHUDPlayerParamsWidget.cs`：
   - 订阅 `nnfmkefpbdi`+0xa0（= `gnjdcldedba`）失效回调（`oedldbgahen` :213-239）→ `jfjfdhdmmkn` :438-445：

     ```csharp
     float real1 = obj14 == null ? 0f : ((byte*)obj14 + 0x30)[0];   // = pmdcpeegbbd.odbiihjlfop = 308.f0
     this.beomhfknplm.koomajjcigp = real1;  ... this.beomhfknplm.nemmcmfbbef = real1;
     ```

   - 每帧 `Update()` :110-128 把平滑值灌入 `energyBar.Set(...)`（hunger=+0x10/f1 :148-153，luck=1−f4 :174-179，comfort=+0x24/f3 :201-207）。
3. **个人资料面板** `RF4/Client/UI/Explorer/RUIUserParamsWidget.cs:70-74`：`m_energy ← obj12+0x30`（同 308.f0）。
4. **遥测** `emkbkapcnfm.cs`（jennhnaobfc :15-53 + cbhjcnkojgc :164-310）：把 f0..f6 打成 float3 列表上报分析（不含游戏逻辑）。

### 1.3 "客户端约 10 秒平滑"的出处

四个条各持一个平滑器 `ihlkhcenhfc`，构造参数 10.0：`RHUDPlayerParamsWidget.cs:351-392`（`ihlkhcenhfc1.ctor(10.0f)` ×4）；`ihlkhcenhfc.cs` 构造把 `smoothUpTime=smoothDownTime=10.0f`（RVA 0x111E150），`Update` 用 `Mathf.SmoothDamp(current, target, ref vel, 10s, ...)`（RVA 0x111DE90）。所以**下发跳变会被 10 秒缓动到 HUD**，但模型值本身不插值。

## 2. 搏鱼中哪些动作消耗玩家耐力？消耗速率公式？

**结论：客户端源码中不存在任何耐力消耗公式——未找到，且可基本断言"不在客户端"。**

证据：

- 全库 grep `stamina|fatigue|tired|exhaust` 仅命中 SevenZip 无关文件；`energy` 命中仅 HUD/UI 控件与 3 个动物导航类。
- 全库引用 `odbiihjlfop`（能量字段名）的仅 9 个文件：`pmdcpeegbbd / pnbphpdmppe(序列化) / gnjdcldedba(模型) / fananjfdjbb(变化检测) / emkbkapcnfm(遥测) / RUIUserParamsWidget / RHUDPlayerParamsWidget`，以及同名陷阱 `gkfghccolil/ackcocopopk/nmhcohgdjon`（那是 27 判鱼对象的**鱼体力池**字段，见 §5 陷阱）。**Fisher.cs / FishingSet.cs / Reel.cs / Rod.cs / 搏鱼控制器（llkfcijmgfd/paldjjcggdd/effjdeblglh）零引用。**
- Fisher.cs 中的摇轮/手部动画（`reelFingersController` Fisher.cs:50/1361-1437、手部 IK :9979-9984 等）只吃动画参数，不读能量。

**收线速度是否受耐力限制？** 客户端不受。搏鱼收线物理由服务端下发的 26 曲线段（`oiikpeaajln`→本地曲线）+ 27 鱼对象参数驱动（`object_chains/fishing_rest.md` 使用链：`paldjjcggdd.cs:817-918` 力度/速度/角度 `Mathf.Lerp/Clamp` 插值；`llkfcijmgfd.cs` 排序/挑选当前阶段曲线；`eabkpandbch.cs:400-436` 拉线/出线阈值）。**"耐力低→摇轮变慢/变沉"在客户端代码中无对应实现（未确认服务端是否有）。**

服务端可用的扣减输入（推断，客户端只能看到这些上行）：14/7 每帧杆态、14/8 搏鱼快照、14/9 拉拽、3/9 `vital_step` 每 12.5s（位置 + 活动位标志，`FisherStatisticsController.cs:496-546`，flags 枚举 `opbohchhbhl` 0..256，观测组合 180/244/308/436/500/501/502，**具体位义未确认**）。

## 3. 玩家耐力如何恢复（速率公式）？

**客户端无恢复公式（未确认服务端）。** 客户端唯一行为：每 ~15s 收到 308 覆盖 `gnjdcldedba`，HUD 以 10s SmoothDamp 逼近新值（§1.2/§1.3）。官服实测 f0 只在 [0.830, 0.85] 窄带内缓变，恢复/消耗的净速率无法从客户端样本分离。恢复公式必须在服务端自行设计。

> ⚠️ 总览补充（服务端现状核对，2026-11-07）：本服务端把 f0 **恒定为 0.85**——`store/repos_player_condition.go:9` 的 `DefaultPlayerConditionPrimary`，`UpsertPlayerCondition` 无任何业务调用（仅 `profilebuild.go:37` buildPose308 与 `cast_distance_inputs.go:452` castPlayerCondition 读同一常量）。即当前服务端**没有玩家耐力模型**，与官方短会话样本一致，长会话/高频拉拽会露馅。

## 4. 耐力归零的后果

- **客户端没有**"不能收线/强制放杆/锁摇轮"的本地逻辑：无任何输入门控代码读取能量（§2 证据）。
- **服务端操作结果错误码 84**：`abadjfbfdjb.cs:87` `nndjgcepmbj = 84` → 文案映射 `dchjlkidjdc.cs:104` `return Localization.Get("kOperationResult_LowEnergyLevel", true);`（错误码枚举笔记 `enums/abadjfbfdjb.md:119` 确认该段为 kOperationResult 主表）。即**低能量以"操作被拒 + 错误文案"形式体现**（具体是哪个 C→S 操作触发，客户端 dump 中 mapper 是通用分派，未确认到具体命令）。
- 本地化串 `kNotification_NotEnoughtEnergy` 存在：`limnkfhkgdm.cs:1050`（常量）与 `:43684`（getter `maofdmiingg`）——**直接触发点在反编译中不可见（shared stub），未确认**。

## 5. 客户端是否上报耐力？14/8 里有没有玩家侧的值？

**不上报。308 是纯 S→C**：`IDA_SEMANTIC_INDEX.md:500/974-976` 中 `pnbphpdmppe` 只有读取器/写入器/构造/GetSize 四个序列化设施，**没有任何 `bbpmdcodcee$$xxx` 网络 C→S 入口**（对照：21 有 `0x1805F0380 bbpmdcodcee$$odpoajipkak` = 14/8）。客户端代码也没有任何对 `pmdcpeegbbd` 字段的业务性写回（唯一"写"在读取器内部）。

**14/8（tid 21 `cdhlhfejmai`/DTO `komaddgfhmo`）不含玩家耐力。** 构造点 `FishingSet.coaeplnbggb`（`FishingSet.cs:6408-6523`）：全部 25 个 float 来自本地平滑比值（`hffmjocmdhh/oefbjaccdhc/kibbglckpko/ofhbinlenni/dmkpkhchhji` + 搏鱼专用 `oiclljjgpjn/acailfjjknd/ahnkcheohpg`，形如 `gbangjnhndj/npappaaacpm` 累计量÷窗口）与鱼对象 `pocnjfgklcp` 的三个值（:6470-6476），**没有引用 `gnjdcldedba/pmdcpeegbbd`**。字段语义对账（`fields/cdhlhfejmai.md`）：f19=客户端线拉力估计、f20=鱼拉力 echo（==27.f8）、f21=flags 恒 2714、f22=装备上限、f23=收线过程量、f24=累计量（做功）。**最接近"玩家侧"的是 f23/f24（收线过程量/累计做功）——服务端可作能量结算对账信号（推断，未确认）。**

**同名陷阱（重要）**：`odbiihjlfop` 这个混淆名在 27 判鱼对象（`gkfghccolil`）里是 **f5"搏鱼体力池"**（867 量级，测试值 1e7=无限体力）、f6 `oeflihacoij`=初始体力比率——那是**鱼的**体力，不是玩家的（`fields/fgogccpglhb.md:19-20`）。搜"odbiihjlfop"时务必区分。

## 6. 举竿角度/竿弯量与玩家动作、线载的关系

`dhehnkcgonh`（Rod 属性 `kfpmicloead`，`Rod.cs:176-181`）= **竿弯角**：竿物理 `ijnbplockdo.jgclphkjhgg` 在 `ijnbplockdo.cs:641-645` 计算"线方向 vs 竿尖 forward 夹角"（`sub_180571070`=Angle）。相关量 `dmpbmhobnob`（`chlllefopnf`）= 竿弧长−直线距离（弯曲缩短量，`ijnbplockdo.cs:446-484`）。

**它影响装备侧负载上限，不影响玩家耐力**（两者无交叉引用）。竿载上限公式：

`cnefjpnennb.ckoecdfmhbe`（`cnefjpnennb.cs:2969-2976`）：

```csharp
float real1 = Clamp01(figoamhhjgb / 15.0f);                      // 竿弯角/15 归一
float real2 = (轮类型 != fjdcacpoole) ? 2.5f : 0.5f;
return real2 * (1.0f - real1) + 1.0f;                            // 0°→2.5(或0.5)，≥15°→1.0
```

`cnefjpnennb.olgnlodanfm`（`cnefjpnennb.cs:2922-2927`）：

```csharp
return rod.nlmnpekipjc * ckoecdfmhbe(bend) * hlgdagpgdij.mbjoplnijhm;   // 竿极限负载 × 弯角系数 × g(9.81)
```

即**竿压得越弯，允许的负载乘子越小**；`Rod.egdhcnlmgeh`（`Rod.cs:544-560`）取"竿载限"与"线载限"（`effjdeblglh.egdhcnlmgeh`，`effjdeblglh.cs:2080`）的 min 作为当前上限。超载判定 `cnefjpnennb.ojnhkfehpie`（`cnefjpnennb.cs:3520-3543`）：`if (负载 > olgnlodanfm(弯角)) → 加入警告/断裂事件(3.5f)`。

举竿角度本身由玩家输入经 Fisher IK/动画改变竿尖姿态→改变弯角→改变上述上限；**与玩家耐力消耗无代码关联（0 交叉引用）**。`elgalhmkkjb()` 的定义体在 shared stub 中未反编译出来（全库仅 3 个调用点 `FishingSet.cs:1696/5508/5888`），用途是把当前竿弯量写入本地钓组状态 `fbankbpbdbf.kfpmicloead`（`FishingSet.cs:5888-5889`，`fbankbpbdbf.cs:6216`）——其**返回值与 `Rod.kfpmicloead`(=`dhehnkcgonh`) 的关系未确认**（推断为同一弯量的另一访问路径）。

## 7. 备注/坑

- `f5(afmboidbbaj)=11.375` 另被 `hncnmlgnhjm.cs:170-175/344-349/396-401` 与 `Fisher.cs:912-986` 的三个 getter（`fmelinkcckm/lhjjcdinkil/gfggbcjaogb`，重力常数 ×f5）使用，接口 `ijpbbddagkc`/`ildhbppmanh` 声明但**反编译 dump 中未见调用点**——疑似抛竿力度相关，未确认。
- 服务端模拟建议（综合）：能量显示权威走 308.f0（0..1），推送节奏 ~15s、10 秒平滑交给客户端；消耗/恢复全在服务端，可用 14/8.f23/f24、14/9 拉拽、3/9 活动 flags 作扣减依据；低能量拒绝用操作错误码 84。
