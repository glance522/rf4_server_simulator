# RF4 搏鱼物理分析（2/4）：动作拉力与线物理（客户端力学链）

> 分析日期：2026-11-07
> 性质：**一次性分析快照**，非长期维护文档；行号以当日反编译树与仓库代码为准，后续提交可能漂移。
> 来源：AI 子代理逆向报告（Explore agent 逐类回读源码）；总览与问题清单见 [analysis-fish-fight-physics-2026-11-07.md](analysis-fish-fight-physics-2026-11-07.md)。
> 对象：`E:\RF4\cs\il2csharp_full\il2csharp_full\Assembly-CSharp\`（下称 `AC\`）+ `E:\RF4\cs\ida-docs\`。

**先说两个全局性发现（影响所有后续引用的读法）**

1. **命名别名**：本树的 il2csharp 反编译与 Il2CppDumper 的 `script.json`（`E:\RF4\cs\il2csharp_full\il2csharp_full\script.json`，120483 条方法名→RVA）对同一 RVA 给出了不同名字。凡 script.json 名与反编译名冲突的，本文用反编译名（源码可查）并附 script.json 名与 RVA。例如 `faioflmebgi()` ≡ `RodRigFishingSet.oanceiemgpo.get`（RVA 0x181158CB0 完全一致）。
2. **单位**：客户端内部线载/阈值一律是**牛顿**（强度 kg × `mbjoplnijhm`(=Physics.gravity, `hlgdagpgdij.cs:94`)）。上报/显示时用 `/9.81` 还原 kg。

---

## 0. 一页总览（力学链条）

```
服务端 14/14 obj27 (fgogccpglhb)
  f8 fanpgngohlp (拉力参数) ─┐
  f21 hcehpnihaia (jiefmbnjlcp[] 曲线行) ─┤→ nmhcohgdjon (鱼模型, 持有 wire DTO 原文)
  f5 体力池 / f6 初始体力比率 ─┘          │
        ↓ ackcocopopk (搏鱼参数持有者, 加密float eikjjhjpfca)
llkfcijmgfd.fdhcahmpkpa()  ← 行为组 mhombohajbc 工厂 ("Fish maneuver {0} is not implemented")
  → 逐行为组建 paldjjcggdd 子类（100..105/200/300..312/400/500 各一个类）
      进入动作时从当前曲线行 jiefmbnjlcp 采样：
      拉力 = Random.Range(row.cpglhiegahi, row.fanpgngohlp) × 鱼体乘数   ← ★动作拉力
      写入鱼体运动参数 jikpiiidfol (param 12/3/1/5/18)
        ↓ RF4.Client.BodyPhysics (BodyMotor/BodyEnvironment) 施力于鱼刚体 (Fish.figeacbhfhj)
线 = 多段 ConfigurableJoint 弹簧 (RigConnector + MultipleJointsConnector/NatureJointConnector/JointConnector)
      张力 = 刚度 × (两端距离 − 自然长度) × 0.102 × 9.81        ← 胡克定律
RodRigFishingSet 每帧:
      behhjginbnl(dt)  线角度/距离/线长几何
      pbaleojpaam(dt)  线载平滑: target = min(pkiphmblekj[线弹簧汇总], bdoegemdipn()[需求负载])
                        → fcpagkkekeb (SmoothDamp) → faioflmebgi() = 当前线载(N)
FishingSet.gpnmjgkigmg(dt) (每帧总入口之一):
      gebalmbahao(dt)  统计(均值/极值)   injcimnljpk(dt) 拉力尖峰检测
      dncjeoomgco(dt)  超载判定 → hmbjimllpme(dt) 收集各件超载记录 → 最弱件
                        → dboabijkbif{断力/线载/泄力/线长…} → 17/9 上报 → 服务端回 11028
```

## 1. 鱼拉力（27.f8 / 27.f21）如何进入物理；动作拉力生成

### 1.1 wire 数据落地

- wire 27 类 `fgogccpglhb`（DTO `gkfghccolil`）字段 f8 名就叫 `fanpgngohlp`：`fgogccpglhb.cs:160`（序列化把 `fanpgngohlp` 写进 `bfledcfpicj[num1]`）。
- f21 `hcehpnihaia` 的元素类型就是曲线行 `jiefmbnjlcp[]`，鱼模型**原样持有 wire DTO**：`nmhcohgdjon.cs:12`（字段 `nbhppkkldpm : gkfghccolil`）、`nmhcohgdjon.cs:552-570`（`this.nbhppkkldpm.hcehpnihaia` 过滤后拆成两组 `gifnagginji`/`mleamkclfed`，`nmhcohgdjon.cs:29-30`；过滤谓词按行的行为组枚举 100..109 分支，`nmhcohgdjon.cs:857-885` `joiajkjehmk`）。
- 搏鱼参数持有者 `ackcocopopk`（`ackcocopopk.cs:5-24`）把 f5..f20 存成加密 float `eikjjhjpfca`（**注意：`eikjjhjpfca` 是防篡改 float 包装，不是曲线**，解码= `(hjleipmhfen>>16) ^ hhpofihmmio`，`eikjjhjpfca.cs:164-174`；ida-docs 里"12 条 eikjjhjpfca 曲线"的说法不适用于本树）。
- 曲线行 wire 对象是 tid 26 `oiikpeaajln`（DTO `jiefmbnjlcp`，见 `oiikpeaajln.cs:33`）。

### 1.2 行为状态机（动作从哪来）

`llkfcijmgfd.fdhcahmpkpa(...)`（`llkfcijmgfd.cs:45-283`）= **鱼动作工厂**：按曲线行的行为组 `row.fopbhlmkcpe`(`mhombohajbc`) 创建 `paldjjcggdd` 子类（错误时打日志 `"Fish maneuver {0} is not implemented"`，`llkfcijmgfd.cs:113/188/258`）：

| 行为组 | 状态类 |
|---|---|
| 100/101/102/103/104/105 | `lgnplfdlplg` / `miblonjfekn` / `nkhpjedejnh` / `hgeljkpnhne` / `bhnjkmgknoe` / `pipkjodjmli`（llkfcijmgfd.cs:66-106） |
| 200 `opilhpedlai` | `jpkkkbiacja`（:119） |
| 300..302,304..312 | `hbiipolahad`/`bnaafdmhbio`/`nkbcnogiiel`/`llgijjadnhg`/`pkpchnafgpd`/`mlpeddobkhb`/`mogeamcakfl`/`fghcdeoegog`/`acigmkabpao`/`nflpnkljckd`/`ifmhpkmblhb`/`fmegcohebam`（:148-240） |
| 400 `mojajgafglc` / 500 | `gnbbaebhmmg`（:245）/ `cfcockfpagh`（:264） |

行为基类 `paldjjcggdd` 持有：当前曲线行 `meddlapeebi : jiefmbnjlcp`（`paldjjcggdd.cs:60-78`）、搏鱼上下文 `peipahjmida : gjhiologibj`（`paldjjcggdd.cs:261-279`；运行时实现就是 `Fish : MonoBehaviour, gjhiologibj, gclanlcddlf`，`RF4\Client\FishingScene\Fish.cs:18`）。

### 1.3 ★动作拉力的生成（曲线行驱动，确认）

动作状态进入时（`paldjjcggdd` 状态初始化，`paldjjcggdd.cs:1900-2032`）**从当前曲线行逐列随机采样**：

```csharp
// paldjjcggdd.cs:415-435 (RVA 0x1287370) hneikgpgmdk() —— 瞬时拉力
float real1 = UnityEngine.Random.Range(row.cpglhiegahi /*0x38 拉力下限*/,
                                       row.fanpgngohlp /*0x20 拉力上限*/);
if (鱼体乘数) real2 = real1 * <鱼模型+0x7c 加密float>;
```

```csharp
// paldjjcggdd.cs:1277-1307 (RVA 0x12892F0) kapnonieegm() —— 平均拉力
float real1 = (row.fanpgngohlp + row.cpglhiegahi) * 0.5f;   // 区间中值 × 鱼体乘数
// paldjjcggdd.cs:1944  时长/强度采样:  Random.Range(row.ficekblljic, row.gjebjkpbigo)
// paldjjcggdd.cs:1961-1982  另一参数在 [row.aojbojfcjam*2, row.mfmgipnjmjh*2] 内按上一动作进度插值
// paldjjcggdd.cs:2010  变体系数 = Random.Range(0.8f, 1.2f) × 拉力
// paldjjcggdd.cs:2020  速度/幅度 = Random.Range(row.bdgcidiaihk, row.fgkkoaameeh)
// paldjjcggdd.cs:409-413 (mdbhakbaddh) 次数 = RandomRangeInt(row.pkllkbnojof, row.leeiddmpnbl)
```

鱼体乘数 = `nmhcohgdjon` +0x7c `llhelgdmpnf`（getter `akooinkapig`，`nmhcohgdjon.cs:157-169`；赋值 `nmhcohgdjon.cs:733-738`：`clamp(1 − v², 0.1, 1.0)`，v 由体力/比率的函数构成，详见鱼耐力报告）。

采样结果写入鱼体运动驱动接口 `jikpiiidfol`（`jikpiiidfol.cs:5-16`）的**按号参数**：param 1=`npobmacgkmf`、5=`apnejoaemof`、**12=`gkfioaaffli`（拉力）**、3=`dlfmnahgeab`、18=朝向四元数 —— 见 `eabkpandbch.cs:244-285`（`sub_1800085f0(ID, typeof(jikpiiidfol), motor, value)`）。`BodyEnvironment : affmmoiodac`（`RF4\Client\BodyPhysics\BodyEnvironment.cs:13`）与 `BodyMotor`（同目录）负责把参数变成刚体上的力。**结论：动作拉力确实由 27.f21 曲线行（tension 列 cpglhiegahi/fanpgngohlp）驱动，逐动作随机采样。**

一个"客户端本地动态构造曲线行"的直接证据（并给出列的量纲）：`ljnponddgpp.cmhbmdopdnj()`（`ljnponddgpp.cs:10-28`，行为组 400）：

```csharp
row[0x2c] (fopbhlmkcpe) = 400;  row[0x50] = 0.1f;  row[0x10] = 0.15f;  row[0x58] = 1.0f;
row.cpglhiegahi (0x38) = fish.bhjneejbjmc() * 10.0f * hlgdagpgdij.mbjoplnijhm;  // 拉力下限 = 鱼质量×10×g (N)
row.fanpgngohlp (0x20) = fish.bhjneejbjmc() * 30.0f * hlgdagpgdij.mbjoplnijhm;  // 拉力上限 = 鱼质量×30×g (N)
```

其中 `bhjneejbjmc()`（script.json 名，反编译名 `Fish.aoijopejgkn`，`Fish.cs:97-104`，RVA 0xB73940）= **`figeacbhfhj.mass`（鱼刚体质量 kg）**。⇒ 曲线行拉力列单位是**牛顿**，量级为鱼重的 10~30 倍（该 maneuver）。

### 1.4 f8 的消费点（未完全定位，明确标注）

- **本地测试鱼构造**里 f8 的公式：`nmhcohgdjon.cs:785-786` `fanpgngohlp = w_g×0.001×g + w_g×0.001×g` = **2 × 体重kg × 9.81 (N)**。
- 客户端搏鱼物理里，找到的动作拉力全部来自**曲线行列/鱼刚体质量**，**没有找到"直接读取 f8 乘进力"的代码路径**。f8 在客户端可能只用于（a）动作选择/曲线重建比较（`ackcocopok.cs:47-55` 存了它，`lmnjahidbcn()` 全字段等值比较 `ackcocopopk.cs:207-208`）、（b）服务端侧模拟。**标注：f8 → 力的直接换算点未定位，不确定。**

### 1.5 鱼的"出水程度"对载荷的放大

`BodyEnvironment.pjonppibhbl()`（script.json 名；反编译名 `apkilpdheeh`，`BodyEnvironment.cs:53-60`，RVA 0x1811C39D0）= **`1 − eiaglnkpokj`（1−入水度）**：全在水中=0，完全离水=1。

### 1.6 线上"需求负载" `bdoegemdipn()`

`RodRigFishingSet.bdoegemdipn()`（`RodRigFishingSet.cs:1003-1041`，RVA 0x1159390）：

```csharp
if (this.opmmalbglpl != kieibhlheed.jfgjniaaopb) {        // 非搏鱼态(状态枚举见 §7)
    real3 = rig.baglokloobi().pjonppibhbl();               // 鱼出水程度 0..1
    real4 = fightCtx.bhjneejbjmc();                        // 鱼刚体质量 kg
    real5 = fishEnv.pjonppibhbl();
    real1 = (real5 * 9.0f + 1.0f) * real4;                 // 鱼重 × (1..10)
    real2 = this.mingmpofgco * (real3 * 9.0f + 1.0f);      // 钓组总重 × (1..10)
    return (real1 + real2) * hlgdagpgdij.aopedkcamcd;      // × g×15 ≈ ×147.15 → N
}
return hlgdagpgdij.aopedkcamcd * 500.0f;                   // 搏鱼态: 恒定 73575 N (≈500kg×g×15)
```

- `mingmpofgco` = `rig.npgbjopcolg()`（`RodRigFishingSet.giglhpmnjic`，`RodRigFishingSet.cs:443`；script.json 的 `Rig*.npgbjopcolg` 与反编译的 `gfalepkamdg`（如 `RigMarineBottomClassic.cs:146-179`）同 RVA 0x5D5FE0）= **钓组各刚体质量之和（kg）**。
- `aopedkcamcd = mbjoplnijhm × 15`（`hlgdagpgdij.cs:95`）。**标注：×15 的物理含义（竿/线杠杆放大？动态系数？）未确定**；搏鱼态需求恒为 500×147 N 意味着搏鱼中线载实际由 `min(线弹簧张力汇总, 73575)` 决定——即由物理弹簧与泄力主导。
- **标注**：反编译把非搏鱼分支包在 `if (0f != 0)`（恒假，`RodRigFishingSet.cs:1009`）里——原条件被反编译器常数折叠丢失，上面的 real1/real2 逻辑是该死分支的原文，语义（何时走）不确定。

## 2. 每帧线载（line load）计算

### 2.1 线的物理表示与张力公式（胡克定律）

线 = 一串 `ConfigurableJoint` 弹簧（竿尖→铅/鱼），由 `RigConnector` 管理：字段 `ehkekkfbffo : MultipleJointsConnector`（主线，`RigConnector.cs:26`）、`nejmbgjcing : NatureJointConnector`（挂鱼时创建，`RigConnector.cs:29`、创建点 `RigConnector.cs:1501-1508`）、`pegmopfbcob : SurfaceConnector`（前导段，`RigConnector.cs:14`）、`ejdddphoeph : JointConnector`（`RigConnector.cs:17`）；`pkpijfjceof`(0x80)=线自然长度（setter 触发 `dlgdidblmaj()` 重建关节，`RigConnector.cs:102-121`）。

```csharp
// dgmaokfcpke.cs:748-758 (RVA 0x131C420) —— 距离→伸长量
inhiliepdbf(Vector3 a, Vector3 b, float 自然长, float 刚度):
    return inhiliepdbf(sqrt(dist²(a,b)) − 自然长, 刚度);

// dgmaokfcpke.cs:534-542 (RVA 0x131B6C0) —— 伸长→张力
inhiliepdbf(float 伸长, float 刚度):
    return 刚度 * 0.102f * hlgdagpgdij.mbjoplnijhm * 伸长;   // ≈ 刚度 × 伸长 (0.102×9.81≈1)
```

弹簧刚度由线参数动态决定：`dgmaokfcpke.jdpolppbedm()`（`dgmaokfcpke.cs:602-687`）：

- 基础刚度 = clamp(两端较轻刚体质量 / 0.00020400001, `gphifghcohk`=7.0, `lpogpfkbaoe`=400)（:615，常量 `hlgdagpgdij.cs:97-98`）；
- 上限 = `cabhpclfmkk(limit)`（:624；定义 `dgmaokfcpke.cs:510-531`）= 使"伸长×刚度×0.102×g = 线强度 N"的刚度，其中含磨损乘子 `iejhpfpngbf` 与延展/线径因子（cfg+0x7c, clamp 0..1）；
- 再保证张力 ≤ 较轻刚体质量 × `lkllchobffb`(10.0) × g（:666-674）。

每帧各段张力汇总：`RigConnector.beogkjajhjl(ref A, ref B, ref C, ref D)`（`RigConnector.cs:1276-1376`，RVA 0x10EA9D0）：D=7.0（刚度基值）；`MultipleJointsConnector.inhiliepdbf(ref C, ref A, ref D)` 把每段 `inhiliepdbf` 张力累入 C、再含分段附加项累入 A（`MultipleJointsConnector.cs:432-451` 区域），其余三个连接器把各自张力同时加进 A、B、C（`RigConnector.cs:1313-1365`）；最后 `if (A > C) A = C`（:1368-1373）。
- A=`hmobnmboloo`、B=`ghpoldpieig`、C=`pkiphmblekj` 由 `RodRigFishingSet.mgmhgkfdghb()`（`RodRigFishingSet.cs:1351-1399`）取回：`alefohkinll = hmobnmboloo + rig.kaloaamecia()`。

### 2.2 线载平滑（faioflmebgi 的来源）

`RodRigFishingSet.pbaleojpaam(dt)`（`RodRigFishingSet.cs:891-1001`，RVA 0x1158ED0）——每帧（由 `behhjginbnl` 末尾调用，`RodRigFishingSet.cs:1162`）：

```csharp
// RodRigFishingSet.cs:987-996
float real3 = this.bdoegemdipn();                       // 需求负载 (N)
float real4 = min(this.pkiphmblekj, this.bdoegemdipn());
this.fcpagkkekeb.nemmcmfbbef = real4;                   // 目标值
this.fcpagkkekeb.Update(dt);                            // SmoothDamp
```

`fcpagkkekeb : ihlkhcenhfc`（`ihlkhcenhfc.cs:8-14`）= SmoothDamp 平滑值容器（目标 `nemmcmfbbef`、当前 `koomajjcigp`、`ValueTimedModifierParameters` 的 smoothUp/DownTime）。

**当前线载 getter**：

```csharp
// RodRigFishingSet.cs:414-421 (RVA 0x181158CB0) —— script.json 名 faioflmebgi()
public override float oanceiemgpo { get { return this.fcpagkkekeb.koomajjcigp; } }   // 平滑线载 (N)
```

调用方：`FishingSet.cs:1666/3577/5518/5521/5528/5867/6040`、`Fisher.cs:12631`、HUD `RUILoadIndicator.cs:36`。

### 2.3 泄力/刹车（drag）如何限制载荷

- 刹车旋钮 0..1 = `Reel.mmoiekbpoam`（0xbc，`Reel.cs:40`；getter `nfpemdplkol`=script.json `lmjemmkiocc`，`Reel.cs:111-118`）。
- **泄力值**（拉力上限代理）：`Reel.alldcfooodg`（script.json 名 `kabahnodibh`，`Reel.cs:96-110`，RVA 0xA7B670）：

  ```csharp
  real1 = ((1.0f − clamp(旋钮,0,1)) * 0.05f + 0.95f) * reelComp.bmpepnpmjho;
  ```

  **标注**：反编译显示为 (1−x) 形（0.95..1.0×上限），若原文是 `0.05f + 0.95f*x` 则是经典的"5%..100% 泄力曲线"——此处存疑，需 IDA 复核字节码。
- `bmpepnpmjho`（卷线器组件 `lkgakdhinig` 0xc0）由 `mclbpbanebo()`（`lkgakdhinig.cs:2300-2310`）更新：`=(mchebcdibdl()−1)×clamp(完好度,0,1)+1`，≥29 级时 =+∞。
- 泄力对线载的限制体现在两处：(a) 物理层——线杯打滑放线使弹簧张力被压在泄力设定附近（**打滑放线代码体未定位，标注不确定**；线自然长度 `pkpijfjceof` 增大即放线，`RigConnector.cs:102-121`）；(b) 显示层——`faioflmebgi` 的目标被 `min(pkiphmblekj, bdoegemdipn())` 封顶（§2.2）。
- 泄力状态标志：`bdjbmheiicb`（有无卷线器，`FishingSet.cs:5897-5901`）、`hjndpplmjbd()`（卷线器离合/线杯状态，`pocnjfgklcp` 接口，`FishingSet.cs:5897-5899`）、卸力全松标志 `bhhaopagglk.khdiajkiehh`（见 §3.1）。

## 3. 组合钓力 min(竿,线,前导,刹车) 与 17/9 断件上报

### 3.1 每帧超载收集：各件 `ojnhkfehpie`（虚槽 +0x2e8）

每帧上下文 `fbankbpbdbf.hpljojjdabn`（`fbankbpbdbf.cs:6212-6223`）：

```csharp
public float oiogkapnkhe;  // 0x10 当前线载 (N)  ← faioflmebgi()
public bool  lebphiopfpf;  // 0x14 挂鱼 (bgjdabgkbla.npfgkgdkofe())
public float kfpmicloead;  // 0x18 okncibgkfdd.elgalhmkkjb() (竿端量, 见下)
public bool  bdjbmheiicb;  // 0x1c 有卷线器
public bool  oljonldfgff;  // 0x1d 搏鱼中 (onjllpnkklj())
```

（填充点 `FishingSet.cs:5867-5902`。）树根 `effjdeblglh.ojnhkfehpie`（`effjdeblglh.cs:5407-5479`）沿激活件链递归调用各件实现：

| 部件 | 阈值函数 | 阈值公式（N） | 超载记录倍率 `fbenloglcie` | 位置 |
|---|---|---|---|---|
| **线** `dgmaokfcpke` | `olgnlodanfm(fighting)` | `line.nlmnpekipjc × g × (搏鱼中? 0.5 : 1)` | **1.5** | `dgmaokfcpke.cs:270-279, 404-428` |
| **竿** `cnefjpnennb` | `olgnlodanfm(kfpmicloead)` | `rod.nlmnpekipjc × ckoecdfmhbe(x) × g` | **3.5** | `cnefjpnennb.cs:2921-2983`，override 在该文件 `ojnhkfehpie` |
| **前导** `lapcaghijic` | `acldbloonme`(=script.json `poadfcckmhj`) | `leader.nlmnpekipjc × g` | **1.5** | `lapcaghijic.cs:122-127`，override `lapcaghijic.cs:620+` |
| **卷线器/刹车** `lkgakdhinig` | `acldbloonme` | `reel.nlmnpekipjc × g`（线杯卸力标志 `khdiajkiehh` 置位则跳过） | **1.75** | `lkgakdhinig.cs:410-415, 2493-2517` |
| 钩 `bccbpcnmnhf` / 连接环 `jbipdjaplgk` | `poadfcckmhj()` | 各自 `nlmnpekipjc × g`（仅当为当前激活件） | — | `bccbpcnmnhf.cs:228`、`jbipdjaplgk`（script.json 0x1807F2B00） |
| 子线/其他 `bjmacccgpho`/`ggpdhonfacp` | — | `nlmnpekipjc × g (搏鱼×0.5)` / `ddhcineacka.nlmnpekipjc × g` | — | `bjmacccgpho.cs:229-233`、`effjdeblglh.cs:10058-10062` |

其中 `nlmnpekipjc` = 各装备 wrapper 的"拉力/强度 kg"（copy 自 config `kngaaefkpka`，如 `fipkoikmnca.cs:230`）。竿的系数（`ckoecdfmhbe`，`cnefjpnennb.cs:2969-2983`）：

```csharp
float real1 = clamp(kfpmicloead / 15.0f, 0, 1);
float real2 = (竿类型 != celffbjpmma.fjdcacpoole) ? 2.5f : 0.5f;
return real2 * (1.0f − real1) + 1.0f;        // ∈ [1, 3.5] 或 [1, 1.5]
```

`kfpmicloead = okncibgkfdd.elgalhmkkjb()`，Rod 端实现 = `jnmbpcemfao.dhehnkcgonh`（`Rod.cs:177-181`，RVA 0x13B5E20，竿弯组件的量，**单位/确切语义未定，量纲疑为 m，≥15 时竿系数=1**）。

每件实现（例：线）：

```csharp
// dgmaokfcpke.cs:404-428
float real1 = this.olgnlodanfm(ctx.oljonldfgff);
if (ctx.oiogkapnkhe > real1) {                    // 线载 > 该件阈值
    record = new effjdeblglh.jaedpgkjgjn(this, real1, 1.5f);  // (件, 阈值, 倍率)
    list.Add(record);
}
```

记录结构 `effjdeblglh.jaedpgkjgjn`（`effjdeblglh.cs:11600-11625`）：`lnipaakahco`(件)、`kflopmfokna`(0x18 阈值 N)、`fbenloglcie`(0x1c 倍率 readonly)。

### 3.2 判定与断件概率：`FishingSet.hmbjimllpme(dt)`

`FishingSet.cs:5851-6130`（RVA 0x5E3CB0）：

1. 清空 `khjfefflelo`（List<jaedpgkjgjn>，构造于 `FishingSet.cs:6232-6246`），填上下文，遍历组件树收集超载记录（:5911-5999，含线/卷线器/树）。
2. 取**阈值最小的件**（:6016-6031，`real4` 从 +∞ 取 min）。
3. 以当前线载对该件算超载比率并掷断件骰：

```csharp
// FishingSet.cs:6040-6072 (反编译近似还原)
real6 = this.faioflmebgi();                       // 当前线载 N
if (real6 <= item.kflopmfokna) return null;       // 未超最弱件阈值 → 不断
ratio = clamp((real6 − thr) / (thr×mult − thr), 0, 1);   // mult: 线1.5/轮1.75/竿3.5
return lphjocjojcp.mkkielhmcgn(ratio * 4.0f, 1.0f, dt) ? item : null;
```

`lphjocjojcp.mkkielhmcgn(p, 1, dt)`（`lphjocjojcp.cs:338+`；单参版本 `:156-166`：`p≥1 恒真 / p≥Random.value`）= **按 dt 折算的概率判定** ⇒ 每帧断件概率 ≈ 4×超载比率（时间归一）。另有帧门限：只有当归一化载荷 `fjamjbjmfdd()`（=平滑泄力端载荷/0.005 clamp 0..1，`RodRigFishingSet.cs:176-193`，script.json 名 fjamjbjmfdd 同 RVA 0x11592F0）**> 0.5** 才做上述检查（`FishingSet.cs:1656-1661`）。

### 3.3 组合钓力 = min(...) 的显式实现

`Rod.egdhcnlmgeh(bool fighting, naheooefgop type)`（`RF4\Client\FishingScene\Rod.cs:544-558`，RVA 0x13B1B90）：

```csharp
float real1 = this.coffdfkgfaa.olgnlodanfm(this.jnmbpcemfao.dhehnkcgonh); // 竿阈值(含角度系数)
float real2 = effjdeblglh1.egdhcnlmgeh(jclakecchen, hpainbakldb);          // 其余件 min
return min(real1, real2);
```

其内部 min 归约 = `effjdeblglh.egdhcnlmgeh`（`effjdeblglh.cs:2080-2194`，RVA 0xF0B8A0，沿激活链 `real3 ≤ real2 则取 real3`），逐件阈值 switch = `effjdeblglh.egdhcnlmgeh(gkghkfcgaic, bool, naheooefgop)`（`effjdeblglh.cs:9989-10195`，含"浮漂钓组时前导不参与（+∞）"等 rig 类型分支：`effjdeblglh.cs:10104-10151`；卷线器卸力标志 → +∞：`:9996-10000`）。逐件倍率 `nknmmbkjnmc`→`cmbebbkjgbj()`（`effjdeblglh.cs:10197-10206`，默认 1.0）。

### 3.4 17/9 上报体（含断裂力/线载/泄力/线长）

发送链：`FishingSet.dncjeoomgco(dt)`（`FishingSet.cs:1652-1710`，RVA 0x5D8550；由每帧 `gpnmjgkigmg` 调用 `FishingSet.cs:3431`）→ 有鱼且 `fjamjbjmfdd()>0.5` 且 `hmbjimllpme` 返回记录 → 构造 `dboabijkbif`（`dboabijkbif.cs:4-15`）：

| dboabijkbif 字段 | 填充（`FishingSet.cs:1664-1707`） | 语义 |
|---|---|---|
| `icdanpcjoch`(0x10 int) | `reel_obj[0xd8] + 1` | 卷线器计数/圈数+1 |
| `kflopmfokna`(0x14 float) | `record.kflopmfokna / 9.81` | **断裂力 kg（最弱件阈值）** |
| `epfoccjnimj`(0x18 float) | `faioflmebgi() / 9.81` | **线载 kg** |
| `gcejfagalgi`(0x1c float) | `rod.ckoecdfmhbe(elgalhmkkjb())` | 竿系数 |
| `cdjnfjinpom`(0x20 float) | `reel.kabahnodibh() / 9.81`（有轮时） | **泄力 kg** |
| `eiaffflnfjc`(0x24 float) | `(float)ejldhgkfjlc()`（+有轮时 `+= hflelkknkfa()`，:1702-1707） | **线长** |
| `badpjfpnfkp`(0x28 enum `hfhclbkibol`) | `(opmmalbglpl==3 ? 1 : 0) + 1` | 模式（搏鱼=2） |
| `danjplakmhh`(0x2c int) | `kkblmbgbdfj.labedbjihjk()` | 校验 |
| `gilhgpedkhc`(0x30 bool) | `onjllpnkklj()` | 搏鱼中标志 |

→ `FishingSet.gmbjlhkaeik(item, msg)`（`FishingSet.cs:3034-3057`，RVA 0x5DC9D0）→ `cnefjpnennb.midjpmhapnp(msg, 断件guid, callback)`（`cnefjpnennb.cs:1153-1180`）→ `bbpmdcodcee.hikfdalijmn(sender, 竿guid, 件guid, msg, ...)`（mailbox.md:41/525：**17/9 rod/break**，响应对象 `afaglefjfpn` = 11028 `ikmdmfoojfg`，f0 断线标志、f1 断件 guid、f2/f3/f4 容器快照）。回调 `FishingSet.icmffdlemgc.inigefidmam`（`FishingSet.cs:3053-3055`）。

**附带**：拉力尖峰检测 `chghklecljk.Update(load, dt)`（`chghklecljk.cs:47-112`；FishingSet 实例参数 (8.0, 1.8, 0.25)，`FishingSet.cs:6208-6212`）：平滑值>8 且瞬时载荷 > 1.8×平滑 → 记一次尖峰；尖峰数变化且有鱼时经 `nnfmkefpbdi.caconpicefk.ekjmbihegpn(次数, 鱼guid, 泄力kg)` 上报（`FishingSet.cs:3632-3667`，`injcimnljpk`）。装备统计（均值/极值，含 `faioflmebgi()` 与 `faioflmebgi()/ekmmphnpjjl()` 比率）在 `gebalmbahao`（`FishingSet.cs:5476-5576`；统计结构 `cpcgfhjihco.cs:32-62`）。`ekmmphnpjjl()` 使用点 `FishingSet.cs:3638/5498/5530`、`RodReelRigFishingSet.cs:821-828`（**函数体缺失，语义"组合上限"为高置信推断但无原文**）。

## 4. 收线（crank）与张力、放线速度

- 收线改变的是**线自然长度** `RigConnector.pkpijfjceof`（`efhpiigojai` setter，`RigConnector.cs:102-121`）：缩短自然长度→弹簧伸长增大→按 §2.1 公式张力上升；超过泄力→线杯打滑放线（自然长度变大）→张力回落。**打滑放线的具体方法体未定位（不确定）**。
- 收线距离限制（"把鱼拉到脚边"）：`FishingSet.lnpdconmknc`（`FishingSet.cs:3060-3124`，RVA 0x5DD3C0 一带；接口 `okidaldngci.fkdnjcoaijb`，`FishingSet.cs:3676-3679`）：

```csharp
// FishingSet.cs:3096-3124
real7 = this.okncibgkfdd.hflelkknkfa();          // 线轴上剩余线长 (cnefjpnennb, 体缺失但有引用)
real27 = real7 − 0.5f + real2;                   // 非活鱼饵: 剩余 −0.5 + 输入量
// 活鱼饵分支:
real5 = real4 * (有鱼? 1.5f : 1.0f);             // 待收线长
real6 = this.imdaclihign.pkpijfjceof + real5 − 0.3f;   // 最多收到距玩家 0.3 m
real3 = min(real2, real6);                        // 输入量被钳制
```

- 线杯余量 UI/警告：`RUITorpedoIndicators.cs:770-834`：`fill = reel.nblkibdcgep() / reel.fembccnhgej()`；**剩余 <5 m → `kNotification_LineOnReelAlmostOver`**（`RUITorpedoIndicators.cs:820-834`；本地化键 `limnkfhkgdm.cs:2416`）；颜色阈值 5/7.5 m（:810）。这就是"线收尽"的客户端判定（物理后果是弹簧被拉直、张力骤升直到断）。
- 每帧装备磨损/疲劳累积与拉力挂钩：`FishingSet.cs:3444-3448`：`FisherStatisticsController[0x5c] += fambejfanjj() × (kmnbkbgbpij() × 15.0f × dt)`，其中 `kmnbkbgbpij()`（script.json 名；反编译名 `fackmclpcoh`，`RodRigFishingSet.cs:160-167`，RVA 0x1811583A0）= **平滑后的 pkiphmblekj（线端载荷）**。×15 又见（§1.6）。
- **玩家耐力**：`Fisher.cs`/`Reel.cs`/`FishingSet.cs` 中 grep "stamina/endurance/Tired" 零命中。本树未发现耐力直接挂收线动作的实现——此点由玩家耐力报告（4/4）单独考证。

## 5. 鱼被拉向玩家 / 线收尽 / 相关判定

- **"有鱼"**：`FishingSet.nibneamemnh()`（`FishingSet.cs:6626-6680`，RVA 0x5E6250）= 状态 ∈ {2 水中, 3 搏鱼} 且 `bgjdabgkbla.pmdnlpafcol()`（挂着的鱼 fight context）非空（返回加密 bool `kckicokgbla`）。状态枚举 `kieibhlheed`：0=收起 `mmnncjiihib`、1、2=水中 `nnioeaijfic`、3=搏鱼 `jfgjniaaopb`（`kieibhlheed.cs:4-8`）。
- **"鱼正被线拉动/绷紧"**：`RodReelRigFishingSet.cjmhejjcodg`（`RodReelRigFishingSet.cs:236-256`，RVA 0xB3C1E0；接口 `oljcomcodha.cs:13`）= `rig.bcdbmneclaf()`（`Rig` 方法 @0x18062B040，**反编译体缺失，语义为"线绷直/鱼被拖动"的 rig 级判定——推断，未验证**）。它被用于 `FishingSet.cs:4219/4240`（状态位拼装）与线载上下文 `oljonldfgff`（`FishingSet.cs:5902`）。
- **鱼被拉到玩家跟前**：位置状态枚举 `eabpihljeli`（`bfbanpjgkdb` / `gchkehljkne` 两个已见值）：`FishingSet.hmkikjmbnop(fish)`（`FishingSet.cs:5699-5768`，RVA 0x5E34B0）在 `eabpihljeli1 == eabpihljeli.bfbanpjgkdb` 时构建 `boekjmipdgb`(type=5) 上报对象并 `nnfmkefpbdi.caconpicefk.efhpjiloomi(鱼guid, 容器guid, obj)` 发送（`:5729-5754`）——即"鱼已到身边"的结算上报；`hehdmapljok`（`FishingSet.cs:3454-3505`）处理另一状态 `gchkehljkne`（鱼靠近，暂存 `moabncmfjej`）。
- 折断的竿的掉落物生成（含质量=竿重×0.001×0.5、初速=max(15, oocojdkkcbn())）：`FishingSet.mhicekbibni`（`FishingSet.cs:3311-3379`）。

## 6. 曲线行 `jiefmbnjlcp` 19 个字段语义表

`jiefmbnjlcp.cs:5-26`（19 个成员 = 14 float + 4 int + 1 枚举；"19 个 float"应为此 19 列）。**wire 序号↔内存偏移映射**以 ida-docs `fields/oiikpeaajln.md` 字段值域表为准（f0=行为组、f1=权重、f2/f3=时长、f4/f5=幅度、f8/f9=拉力(牛)、f10/f11=转向、f14=消耗/速度缩放、f15=阶段序、f16=次数上限、f17/f18=重掷区间）：

| 内存偏移 | 字段 | 类型 | 语义 | 证据 |
|---|---|---|---|---|
| 0x10 | bjfefmobfih | float | 时长下限（wire f2；ljnponddgpp 动态行默认 0.15） | `ljnponddgpp.cs:19` |
| 0x14 | bdgcidiaihk | float | 动作参数采样下限（`ecgnfjbihim = Random.Range(bdgcidiaihk, fgkkoaameeh)`） | `paldjjcggdd.cs:2020` |
| 0x18 | leeiddmpnbl | **int** | 次数采样上限 `RandomRangeInt(pkllkbnojof, leeiddmpnbl)` | `paldjjcggdd.cs:409-413` |
| 0x1c | ficekblljic | float | 时长/幅度采样下限（与 0x54 配对） | `paldjjcggdd.cs:1944` |
| 0x20 | **fanpgngohlp** | float | **动作拉力上限 (N)**（与 wire f8 同名） | `paldjjcggdd.cs:420`、`ljnponddgpp.cs:24-26` |
| 0x24 | fgkkoaameeh | float | 动作参数采样上限 | `paldjjcggdd.cs:2020` |
| 0x28 | inpnojfjggg | float | 转向角（未定/转向用） | — |
| 0x2c | fopbhlmkcpe | enum `mhombohajbc` | 行为组（动作工厂路由） | `llkfcijmgfd.cs:58-272`、`mhombohajbc.md` |
| 0x30 | oeedloepfdm | float | 转向角 | — |
| 0x34 | pkllkbnojof | **int** | 次数采样下限 | `paldjjcggdd.cs:412` |
| 0x38 | **cpglhiegahi** | float | **动作拉力下限 (N)** | `paldjjcggdd.cs:420`、`ljnponddgpp.cs:22-23` |
| 0x3c | hlbkfjnodei | **int** | 行使用次数上限 | `llkfcijmgfd` 选择逻辑 |
| 0x40 | hkjohejlkgo | float | 消耗/速度缩放：`lbakoabeoaf(fishModel.jfhcjbbkalp() × row.hkjohejlkgo)` | `eabkpandbch.cs:148-150`（apegdodhpio） |
| 0x44 | mfmgipnjmjh | float | 参数插值上界（×2） | `paldjjcggdd.cs:1962,1982` |
| 0x48 | aojbojfcjam | float | 参数插值下界（×2） | `paldjjcggdd.cs:1961` |
| 0x4c | opichdlalhg | **int** | 行优先级/阶段序号（行排序比较键） | `llkfcijmgfd.cs:1059-1064` |
| 0x50 | aiobpbocjjg | float | 权重（动态行默认 0.1） | `ljnponddgpp.cs:18` |
| 0x54 | gjebjkpbigo | float | 时长/幅度采样上限（与 0x1c 配对） | `paldjjcggdd.cs:1944` |
| 0x58 | pabfoglopio | float | 时长上限（动态行默认 1.0） | `ljnponddgpp.cs:20` |

（"duration 列"与 0x1c/0x54 配对最吻合，但**反编译未直接给列名，duration 归属为推断**。）

## 7. 关键常量与结构速查

- 物理常量 `hlgdagpgdij`（`hlgdagpgdij.cs`）：`mbjoplnijhm`=gravity(:94)、`aopedkcamcd`=g×15(:95)、`caagofjlgoj`=g×7.5、`kiempfkfhhi`=g×5(:100-101)、`gphifghcohk`=7.0(刚度下限,:97)、`lpogpfkbaoe`=400(刚度上限,:98)、`lkllchobffb`=10.0(:96)。
- 统计结构 `cpcgfhjihco`：min(0x18)/max(0x10 `imiogkkacih:cibdhdgkoho`)/∫value·dt(0x20)/Σdt(0x1c)，均值=integral/time（`cpcgfhjihco.cs:32-62`）。FishingSet 的 8 组统计含义（`FishingSet.cs:5490-5572`）：`kibbglckpko`←obj13+0x18（未知载荷）、`dmkpkhchhji`←`ekmmphnpjjl()`、`ofhbinlenni`←`elgalhmkkjb()`、`hffmjocmdhh`←**faioflmebgi()（线载）**、`oefbjaccdhc`←**faioflmebgi()/ekmmphnpjjl()（负载率）**、`acailfjjknd`←reel `kabahnodibh()`（泄力）、`oiclljjgpjn`←reel obj+0xd8、`ahnkcheohpg`←reel `lmjemmkiocc()`（旋钮）。

## 8. 明确的不确定项汇总

1. `faioflmebgi` ≡ `oanceiemgpo.get`（同 RVA 0x181158CB0）为两工具命名分歧；`kabahnodibh`≡`alldcfooodg`、`lmjemmkiocc`≡`nfpemdplkol`、`kmnbkbgbpij`≡`fackmclpcoh`、`fjamjbjmfdd`≡`mfejlichkdj`、`npgbjopcolg`≡`gfalepkamdg`、`poadfcckmhj`≡`acldbloonme` 同理。
2. `bdoegemdipn()` 的非搏鱼分支被 `if (0f != 0)` 常数折叠（`RodRigFishingSet.cs:1009`），原触发条件丢失；×15（aopedkcamcd）的物理含义未定。
3. 泄力公式 `(1−x)×0.05+0.95` vs 预期 `0.05+0.95×x`（`Reel.cs:106`）需 IDA 复核。
4. 线杯打滑放线（张力>泄力→自然长度增加）的具体方法体未定位。
5. f8 → 客户端力的直接换算点未找到（§1.4）。
6. `ekmmphnpjjl()`、`bcdbmneclaf()`、`hflelkknkfa()`、`ejldhgkfjlc()`、`kaloaamecia()`、`elgalhmkkjb()` 的函数体在本反编译树缺失（仅调用点），语义为高置信推断。
7. UI 载荷条 `/0.005f` 的归一化除数（`RodRigFishingSet.cs:181/200/222/290/343/377`）可疑（可能为反编译瑕疵）。
8. 本树无 `gokjlbkhfch`（ida-docs 引用的鱼曲线类）：其角色在本版本由 `effjdeblglh`（组件树根）+`ackcocopopk`+`llkfcijmgfd` 承担。

**关键文件绝对路径**：`E:\RF4\cs\il2csharp_full\il2csharp_full\Assembly-CSharp\RF4\Client\FishingScene\FishingSet.cs`、`...\RodRigFishingSet.cs`、`...\RigConnector.cs`、`...\JointConnector.cs`、`...\NatureJointConnector.cs`、`...\MultipleJointsConnector.cs`、`...\Rod.cs`、`...\Reel.cs`、`...\Fish.cs`、`...\Rig.cs`；以及根目录 `dgmaokfcpke.cs`（线）、`lapcaghijic.cs`（前导）、`lkgakdhinig.cs`（卷线器）、`cnefjpnennb.cs`（竿）、`effjdeblglh.cs`（组件树根/min 归约）、`fbankbpbdbf.cs`（件+帧上下文）、`jiefmbnjlcp.cs`（曲线行）、`nmhcohgdjon.cs`（鱼模型）、`ackcocopopk.cs`、`paldjjcggdd.cs`、`llkfcijmgfd.cs`、`eabkpandbch.cs`、`lphjocjojcp.cs`（概率）、`chghklecljk.cs`（尖峰）、`cpcgfhjihco.cs`（统计）、`hlgdagpgdij.cs`（常量）、`dboabijkbif.cs`（17/9 请求体）、`RF4\Client\BodyPhysics\BodyEnvironment.cs`、`RF4\Client\UI\HUD\RUITorpedoIndicators.cs`。
