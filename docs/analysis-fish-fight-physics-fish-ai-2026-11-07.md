# RF4 搏鱼物理分析（1/4）：鱼的运动轨迹（客户端 Fish AI）

> 分析日期：2026-11-07
> 性质：**一次性分析快照**，非长期维护文档；行号以当日反编译树与仓库代码为准，后续提交可能漂移。
> 来源：AI 子代理逆向报告（Explore agent 逐类回读源码），服务端侧行号已按 `fix/settled-catch-guard`（d30e45b）核对。
> 对象：`E:\RF4\cs\il2csharp_full\il2csharp_full\Assembly-CSharp\`（下称 `ACS\`）+ `E:\RF4\cs\ida-docs\`。
> 标注：凡写"未确认"处不可单独作为结论引用。总览与问题清单见 [analysis-fish-fight-physics-2026-11-07.md](analysis-fish-fight-physics-2026-11-07.md)。

---

## 0. 总体架构（先给全景）

```
Fish (RF4\Client\FishingScene\Fish.cs, 状态机 eabpihljeli)
 └─ nedbhbcadgc : naihiiaooka（鱼脑，按状态切换子类）      Fish.cs:660-707
     └─ hoogiggaoam : dnblinaache（机动调度器）            dnblinaache.cs:44-138
         └─ lcjgigbhhki : llkfcijmgfd（曲线行池+工厂）      llkfcijmgfd.cs
             └─ chookefhpic : List<paldjjcggdd>（机动对象池，按阶段排序）
每帧： Fish → naihiiaooka.ibofhdekmpf → dnblinaache.Update → 当前 paldjjcggdd.Update
机动把参数写入 jikpiiidfol(实现 ndmfaiddmhj) → Fish.nlchmjebogf 拷入 BodyMotor
     → BodyMotor.fgcblpplhob 对刚体 figeacbhfhj 施力/转向 (BodyMotor.cs:260-350)
```

状态→大脑映射（`Fish.cs:660-707`，`eabpihljeli` 枚举共 0..7）：

- 状态1 `fhhiahlanpd`（刺中初始段）→ `pmlepacfcfm`，曲线集模式 **0**（`mleamkclfed` = 100..105 行）
- 状态2 `ibhmnnlodmk`（搏鱼主阶段）→ `gfgapohfogm`，曲线集模式 **1**（`gifnagginji` = 300 系行）——`gfgapohfogm.cs:9-27`，且它持有 `kejihbadeej`（脱钩安全余量，`gfgapohfogm.cs:13`）
- 状态3 `gchkehljkne`（已上岸）→ `bmdlnminpdk`；状态4 → `hnffdebalii`（模式3）；状态7 → `dnnonohdenc`（模式4）

曲线行的拆分在 `nmhcohgdjon` 构造函数：`mleamkclfed`(0xa0) = f0∈{100..105} 的行（**组100 的 aojbojfcjam/mfmgipnjmjh ×4**，`nmhcohgdjon.cs:905-916`）；`gifnagginji`(0x98) = f0-300 ∈ {0,1,2,4,5,6,9} 即 **{300,301,302,304,305,306,309}** 的行（`nmhcohgdjon.cs:851-877`）。即 303/307/308/310-312 的服务端行**不会被收进 300 系曲线池**（307/308/310-312 只能用客户端内置默认行；官方抓包同样不发送这些组，见 ida-docs `fields/oiikpeaajln.md` f0 观测计数）。

## 1. 状态机与阶段选择

### 1.1 行池构建（llkfcijmgfd 构造器，llkfcijmgfd.cs:349-472）

- 按模式取鱼模型的两条曲线数组之一（0xa0 / 0x98），逐行 `fdhcahmpkpa` 建机动对象（仅当 `row.aiobpbocjjg`(wire f1) > 1e-45，即**权重列为 f1**，llkfcijmgfd.cs:388/419）。
- 若池为空，`ppglgmgmlhb`（llkfcijmgfd.cs:962-1021）按模式补默认机动（模式0→组100+`ljnponddgpp`；模式1→`hajkjiaijid`/301/307/308 兜底，见 `dfldpgcngah` llkfcijmgfd.cs:287-347 与 `hlmhhmjmggm` llkfcijmgfd.cs:531-679——缺 100/301/307/308 时强制补入）。
- 最后按 `opichdlalhg`(wire f15) **升序排序**：比较器 `__c.liojconicpb`（llkfcijmgfd.cs:1057-1065）。

### 1.2 下一机动选择 `iolmanmnenc`（llkfcijmgfd.cs:764-943）——两遍扫描

关键逻辑（摘自 llkfcijmgfd.cs:789-835，第二遍 897-938）：

```
从列表末尾（=阶段号最大）向前遍历：
  if (row.hlbkfjnodei(wire f16) > 0 && maneuver.jnlebaijpbe(0x8c) >= row.hlbkfjnodei)
      RemoveAt  // 该行重复次数用尽 → 从池中删除（0x8c 在 fihemfmfepl 里 ++，paldjjcggdd.cs:898）
  else if (row.opichdlalhg(wire f15) != 上一行的阶段号 && 已累计权重>0) break;  // 只在"最高可用阶段组"内选
  weight = maneuver.beiifmgdehc(0x5c) × row.aiobpbocjjg(wire f1)   // 动态权重 × 行权重
第二遍：real6 = Random.Range(0, 总权重)；逐个累加，命中即选（排除当前正在跑的机动）
```

**结论**：不是按耐力/力量比直接挑，而是：①只考虑**阶段号最大的非零权重组**（高阶段优先）；②组内按 `行权重 f1 × 机动动态权重 beiifmgdehc` 加权随机；③每行有 `hlbkfjnodei(f16)` 次使用上限，用完即从池中移除。动态权重 `beiifmgdehc` 由各机动 `cmlcklecelo()` 依据**鱼的实时状态**（距底/距玩家/水深/线张力等，见 §2 各类）计算——这就是"耐力/力量比"间接介入的地方。

### 1.3 机动结束与重复

- `paldjjcggdd.Update`（paldjjcggdd.cs:437-512）：计时≥时长→`phbnalahbfj()`；进度 `imfhocjbabl≥1`→结束。
- `phbnalahbfj`（paldjjcggdd.cs:2061-2081）：`nibhjbmkopo>0` 时 `bophlgpgabj(this)` **原地重掷参数**（重复次数 = `RandomRangeInt(row.pkllkbnojof(f17), row.leeiddmpnbl(f18))`，paldjjcggdd.cs:899-900）。
- 单次机动内参数重掷 `bophlgpgabj`（paldjjcggdd.cs:1915-2033）：
  - 时长 `ekbbniapkjj = Random.Range(row.bjfefmobfih(f2), row.pabfoglopio(f3))`（`kddeefnbpmc` :2846-2850）
  - 游动距离/力度 `npobmacgkmf = Random.Range(row.ficekblljic(f4), row.gjebjkpbigo(f5))`（`llojgohfmid` :2278-2282）
  - 强度插值 `apnejoaemof = lerp(aojbojfcjam(f6), mfmgipnjmjh(f7), 时长归一化t)`（:1961-1982）——即**行首/行末两个强度乘子随该次机动时长位置插值**
  - 拉力 `gkfioaaffli = Random.Range(cpglhiegahi(f8), fanpgngohlp(f9)) × 鱼0x7c系数`（`hneikgpgmdk` :415-435）；`dlfmnahgeab = gkfioaaffli × Random.Range(0.8,1.2)`（:2010）

### 1.4 行为组语义（各类描述）

| 组/类 | 行为语义（从代码推断） | 证据 |
|---|---|---|
| **100 `lgnplfdlplg`** | 基础待机游动（兜底，阶段号=int.MaxValue 永远最低优先）。时长∞；`fbiflhjglah` 中经 `plhckfgipag=0.8×pmlepacfcfm.hjbdfjmgkbn(...)` 秒后：张力比 `jlkphadoecp()>0.7`→直接结束换机动，`>0.5`→置暂停位 | lgnplfdlplg.cs:11-38(默认行), 105-161, 174-183(进度=1−张力比) |
| **101 `miblonjfekn`** | **强制左转游**：转角列覆写为 `oeedloepfdm=-0.99, inpnojfjggg=-0.9` → 每段左转 −81°..−89° | miblonjfekn.cs:6-31（继承 104） |
| **102 `nkhpjedejnh`** | **强制右转游**：`+0.9..+0.99` → 右转 +81°..+89° | nkhpjedejnh.cs:6-31 |
| **103 `hgeljkpnhne`** | 直行/停顿位：不许重复（`phbnalahbfj` 直接结束）；开始时 `gjhiologibj.mdjjfandcdc()`；权重 50% 概率清零（`aacfnejgfej()*0.5` 抽签）→ 一半情况被跳过 | hgeljkpnhne.cs:21-56 |
| **104 `bhnjkmgknoe`** | **通用转向游动**（101/102 的基类）。`dblnepddfckg`(bhnjkmgknoe.cs:83-245)：按行角度列转 `dffdanfnfaj(dir, angle×90°)`；若线绷紧(`pdeegmjgkjj()`) 基准角 135°、某些钓组状态 160°/60°/20°（:57-81, 99-110, 154-177）；浅水/特定鱼种再 ×3.25~×5.25 时长（:181-200）。默认行转角列两值都是 −1.0（cs:36-37）→ 默认硬左转（**未确认是否有意**，服务端行会覆盖） | bhnjkmgknoe.cs |
| **105 `pipkjodjmli`** | **复合主搏段**：内嵌第二个调度器 `kdpenfpphej`（持 llkfcijmgfd，每帧 `iolmanmnenc` 挑子机动并 Update，pipkjodjmli.cs:255-331）；经 `lfkpjklfhpk=(0.4..1.0)×时长` 秒后全额扣体力 `lbakoabeoaf(edokffmogcn())`；鱼不在水中(水状态≠64)时权重×10（cs:583-588） | pipkjodjmli.cs |
| **200 `jpkkkbiacja`** | **贴底/朝玩家方向游**：方向来自钓组(`eebnkgiopnn`)经 `eiajnmbkmaf` 俯仰修正；张力比>0.85 → 暂停；`lkdpgdhoclm()<0.05`（贴底受阻）→ 结束。默认权重 0.001（很少自发出现） | jpkkkbiacja.cs:41-151, cmhbmdopdnj :11-38 |
| **300 `hbiipolahad`** | 300 系基础：朝平滑目标点 `aamdblnpbmo` 游（写 param17/13/1/12）；权重 = clamp01((距底度量pjonppibhbl−0.8)/0.2)×max(0.15,鱼系数) | hbiipolahad.cs:12-31, 46-158 |
| **301 `bnaafdmhbio`** | **冲向玩家/落点**：记录与目标距离，`dnaemflajbl`=平滑自遊速、`ncglbihokpm`=接近速度；计时>2.25s 后进度被"接近速度/期望"加速；30% 概率触发水面动画参数（cs:39-55）；完成=目标达成或受阻。默认权重 0.001 | bnaafdmhbio.cs:20-123 |
| **302 `nkbcnogiiel`** | **下潜段**：0.65~0.95×时长后拉力×(0.35..0.75)（衰减）；权重 ×`jlofffeogbi()`；进度公式 `1-(1-p)×jlofffeogbi()` → jlofffeogbi→0（深处）才完成；70% 以下概率水面动画 | nkbcnogiiel.cs:12-117 |
| **304 `llgijjadnhg`** | **横向折向急游**：npobmacgkmf×1.25、apnejoaemof×2、dlfmnahgeab×5 强化；左右交替 `ceenggipjje=±1`（mfgcgaabepn 里取反）；目标点 5–10m，到达阈值半径内或换向角 `Random.Range(25°,50°)×±1` 重选；`cemgbkfmhge=false`（不可被外部打断） | llgijjadnhg.cs:33-192 |
| **305 `pkpchnafgpd`** | **翻滚/打转**：每 `elapsed+Random.Range(0.15,0.45)` 秒对刚体 `AddRelativeTorque(±1×力×0.8)`，正反交替（`ceenggipjje` 取反）；npobmacgkmf×0.2、dlfmnahgeab×0.05（短促）；`cemgbkfmhge=false` | pkpchnafgpd.cs:24-104 |
| **306 `mlpeddobkhb`** | **水面翻腾/甩头**（需 `lakkkedgapa()`：特定标志+水况+`jlofffeogbi()>0.5` 才激活）：权重=10（强烈优先）；行时长×0.01（极短）；`lnfdnlmacmc = 绕z轴90°` 四元数（翻滚姿态，静态初始化 cs:40-55）；方向=某 Transform.forward 写 param13 | mlpeddobkhb.cs |
| **307 `mogeamcakfl`** | **暴冲/甩头 burst**（0.2..0.667s）：开始时一次性 `Rigidbody.AddForce(拉力×(1或jlofffeogbi×0.3)×1.25)` + `AddRelativeTorque`（随机 right/up 组合方向，cs:69-125）；空 `ibofhdekmpf`；`pfdkplenmif()=0.4` | mogeamcakfl.cs |
| **308 `fghcdeoegog`** | **被动动作位**：空 Update；开始时清参数、param14=1，50/50 触发 `acdnncfcblo` 动作 5 或 0（两种待机/受击表现）；`pfdkplenmif()=0.1` | fghcdeoegog.cs |
| **309 `acigmkabpao`** | **贴身最后挣扎**（权重条件极苛刻：`pjonppibhbl>0.995` 且 `hmedeknmdgc()×15>pipffmcoolh()`，cs:150-190）：行覆写为 5..6s、强度下限 max(4/8)、拉力下限 ×1.3/×1.7（cs:44-148）；0.6s 后写 param17(向下 Vector3.down)+param12，`pjonppibhbl>0.7` 即结束；后段 `(mfconjhnjmp-60)/25` 参与 `AddForce`（工厂注入 mfconjhnjmp=njimlmganam=45，工厂 cs:219-222；每日起始 Random.Range(30,85)，cs:299-312） | acigmkabpao.cs |
| **310/311/312 `nflpnkljckd`/`ifmhpkmblhb`/`fmegcohebam`** | 同一基类 `cgdcefgcmhb` 的三个变体，仅 f0 枚举不同。基类用 **AnimationCurve 按 elapsed/duration 求值** 生成"上抛弧线"方向（`Vector3.up × curve(t)`，可翻转符号 `hibmjmdcipn`）+ `eiajnmbkmaf` 俯仰 → **跃出水面类抛物线机动** | nflpnkljckd.cs:11-18; cgdcefgcmhb.cs:23-60 |
| **400 `gnbbaebhmmg`** | **远距离逃窜**：权重 = clamp01((pjonppibhbl−0.75)×4)（鱼很远才触发）；目标距离 `Random.Range(clamp(d×10,10,20), min(25, max(同, d×25)))` 米（d=`hmedeknmdgc()`，cs:111-137）；每 0.5s 用 `gjeolmjhgac` 射线检查去路被挡则重选（cs:139-173）；时长默认∞ | gnbbaebhmmg.cs |
| **500 `cfcockfpagh`** | **瘫软/被制服**：开始即清空全部 body 参数（`fihemfmfepl`）+ param14=1，空 Update，时长 1..3s（鱼不再主动施力，随物理漂） | cfcockfpagh.cs |

通用兜底：`lgnplfdlplg`（组100，阶段 int.MaxValue，时长∞）+ `hajkjiaijid`/`ljnponddgpp`（llkfcijmgfd.cs:16-43, 546-551, 983-1000，后两者为占位机动）。

## 2. 轨迹生成（方向 / 转向 / 距离）

### 2.1 方向采样核心 `paldjjcggdd.ddifeamfhdm`（paldjjcggdd.cs:531-894）

"绕圈扫描 + 障碍剔除 + 目标偏向加权"：

- 从 0° 起步，步进 `Random.Range(9°,17°)` 扫满 360°（:612, 754）；
- 每个候选方向生成 `origin + dir × dhgcjnleooc`（距离）候选点，若与目标点 `gckpaadlimm` 的夹角在 `real3`（0..180 窗口）内，权重 `w = clamp01(dot)×1000×clamp01(dot)+1`（:711-713, 736-737）——**强烈偏向目标方向，但保留全向可能**；
- 候选点经 `PhysicsScene.Raycast(clnhmkllcff.difomnajplb 层)` 验证水路通畅（:1411, 1644）；两侧 ±175° 旋转再射线测距取差值>0.025 判可通行，不通则转 60° 俯仰避让（:1419-1627）；
- 最终按权重随机（或候选为空时 `Random.Range(0.5,1.0)` 缩距重试，:780-797）。

### 2.2 每类如何用

- **104/101/102（eabkpandbch 系）**：方向向量存在 `hbandobdmfk`(0xb8)（eabkpandbch.cs:10）。张力比 `jlkphadoecp()<0.05` 时每帧从钩/线方向重取（:186-198）；`dblnepddfckg` 里 `ijmfflndkom(1.0, 角度)` 采样 + `dffdanfnfaj(方向, Random.Range(行0x28..0x30)×90°)` 转向（bhnjkmgknoe.cs:111-134），`dffdanfnfaj`=绕 up 轴旋转向量（eabkpandbch.cs:608-686）。**轨迹=分段折线**，每段一个方向。
- **每帧转向输出**（`eabkpandbch.fgcblpplhob`，eabkpandbch.cs:12-139）：`real3 = dot(方向×距离参数)/npobmacgkmf + (1−张力比)` → 写 param17；俯仰修正 `eiajnmbkmaf` → 写 param13；param1=`npobmacgkmf`、param12=`gkfioaaffli`。方向经 `ndmfaiddmhj.jgamblffchk`（平滑转向器，kpnkkhmokfe setter 触发）平滑后成 `aepgpcmefbl`（ndmfaiddmhj.cs:233-282）。
- **300 系（hkhhbgfhlji）**：目标点 `djpopaoifag`（5–10m 外），`mkkaeaecein`（hkhhbgfhlji.cs:772-897）= "当前朝向 ± 随机转角（范围由 `belbfbbpleg` 依线长/距离动态定：线距>15m 且超出→转回玩家 [−0.8,−0.4]×90°；玩家在线长内→[0.1,0.3]×90°，cs:713-770）+ ddifeamfhdm"。**同 300 系连续机动会继承上个目标点**（概率控制，cs:414-616）→ 形成连续弧段而非每次重新随机。
- **换向频率**：由行时长列（f2/f3）+ 重复数（f17/f18）+ 到点判定（300 系 `lkdpgdhoclm()<0.05` 或进度参数<0.5 即结束，hkhhbgfhlji.cs:178-199；304 到达阈值半径²即换点，llgijjadnhg.cs:113-120）共同决定。

### 2.3 俯仰（垂直分量）`eiajnmbkmaf`（paldjjcggdd.cs:1082-1110 实例入口，静态体 :2327-2825）

- `real4 = clamp01((pipffmcoolh() − pcilaefoclj())/(pcilaefoclj()×1.5))`、`real8 = clamp01((水面Y(deghjmihlcf.eioinjodlag()) − oblfcknojkn() − pipffmcoolh() − pcilaefoclj())/(pcilaefoclj()×1.5))`（:2334-2379）；
- 两个约束角：`（1−real4)×(−105°)+90°`（离底越近越上抬）与 `（1−real8)×105°−90°`（离水面越近越下压），各以 `90°−maxPitch` 截顶，取平均后 `Quaternion.Euler(pitch)` 旋转 forward（:2399-2450）。
- **即：俯仰不是曲线列直接给出，而是由"距底/距面余量"反馈控制**，把鱼约束在水层内（这就是水层约束的实现）。maxPitch 参数（obj36/obj60/...）来源未确认。
- 跳跃离水：306/310-312（弧线/翻滚）与 `lbakoabeoaf` 中的跳跃触发（eabkpandbch.cs:914-937：疲惫态 `nlfbdiackkj()==fgmlmopbebd` 时以 `mhkoobkkifl()×0.4+0.4` 概率置 `nmhcohgdjon.aijdkdfbopd=true, jjgmggmcebe=扣力, jecahdffgoc=Random.Range(0.3,1.2)`——跳出水标志）。

## 3. 深度/水层

- 纵向速度/朝向 = param13（俯仰修正方向）+ param18（姿态四元数 `lnfdnlmacmc`，一般 identity，306 为绕 z 90°）。
- 贴底减速：`lkdpgdhoclm`（paldjjcggdd.cs:2285-2325）——当 `pjonppibhbl()>0.95`（近底）向下射线 `min(2, pcilaefoclj()×1.8)`，返回 `clamp01((剩余−d×1.25)/(d×0.75))` 作为速度缩放。
- 上浮限制：`eiajnmbkmaf` 的水面项（§2.3）；302 下潜完成条件（§1.4）；309/200 的深度条件。跳跃=上述 `aijdkdfbopd` 标志 + 306/310-312 机动。

## 4. 速度链路

写入侧（Fish.cs:1001-1192 `nlchmjebogf`）：

- 疲劳缩放 `real4 = LUT(ddddkpebkoi.igbmfilokgp(min(f²,f)))/999`，f = 1−`BodyEnvironment.eiaglnkpokj`（Fish.cs:1017, 1050）——**极速随体力衰减经 1000 项查找表衰减**；
- `lideomglbli`=平滑目标方向、`jgfcpihfdhm`=期望方向（Fish.cs:1059/1070）；
- `oljaegpagci`(最大速度) = `ndmfaiddmhj.kejedhafmmj() × real4`（Fish.cs:1073-1077）；`djefgfbjemh`(加速度) = `cmiggaknepg() × real4`（:1079-1083）——两方法体在反编译中丢失（未确认读哪个 param；由 param1=`npobmacgkmf`=Random.Range(**行 f4..f5**) 经 `ndmfaiddmhj.Update` 的点积 `gafemfkjglc = max(0, dot(平滑方向, dir×fgkfkgimfgo+外力))`（ndmfaiddmhj.cs:376-407）派生，是最可能来源）；
- 转向敏捷 `lnehpfdpfkh = nnpkmkiclhg()×1.25(omonoljpfad)×real4×0.3`（Fish.cs:1107-1111），进而 `dplfdbchkfp/gmmooofimef = clamp01((lnehpfdpfkh−1)/19)×24+1`（关节角驱动强度，:1115-1133）；
- 力上限 `pmmpbdphlck = afbdfgplfii()×1.25×real4×real10`（real10=线弯折惩罚 1−clamp01(角/90)×0.75，:1088-1106）。

BodyMotor 侧（BodyMotor.cs:260-350，每帧由 Fish.cs:1192 直接驱动）：

```
real6  = max(0, dot(刚体速度, 期望方向))                      // 当前沿方向速度
real9  = real6 + djefgfbjemh×dt;  real10 = min(oljaegpagci, real9)
real14 = lerp(real10, oljaegpagci, clamp01(kpbnmjmnpdj))      // 期望速度；kpbnmjmnpdj=线张力(Fish.cs:1040-1046)
real20 = clamp01(real6/real14) → hlllcemfpbh (0xb4)           // BodyMotor.cs:342-343 "达成比"
aciegmolcfm = min(pmmpbdphlck, real14 × clamp01(1−drag×dt))   // BodyMotor.cs:349 极速（obj54=real14，
                                                              //   反编译变量丢失，语义按上下文推断）
```

玩家侧反作用（Fish.cs:1212-1251）：`real23 = max(1−hlllcemfpbh, 0.25) × (kpbnmjmnpdj × aciegmolcfm)`，被 `min(, 玩家侧 kmnbkbgbpij()×3)` 截断后按 `/10` 或原值（视 `nklaengaiaf()`）乘 `real30` 喂给 `nmhcohgdjon.dpjclmgalfb`（体力增减，见鱼耐力报告）。

- **极速与体型/体重**：服务端 wire f8 拉力 ∝ 体重（ida-docs/object_chains/fishing_rest.md:355 "f8 拉力 ∝体重"），鱼参数经 `ackcocopopk` 包装（nmhcohgdjon.cs:651-653）；`kejihbadeej` 中阈值 `bihlodbgigm = min(bjilibapfdi()×0.025, 0.25)×重力`（kejihbadeej.cs:340-346，bjilibapfdi=fkfghccolil 派生属性≈体重）——**体重只在此处显式进入力阈值；极速本体由 wire f8..f20 模板参数决定，∝体重的 f8 即其载体**（直接算式未确认）。

## 5. 鱼到玩家身边（上岸/抄网判定全集）

调度：`Fisher.dmlahmohcpi`（Fisher.cs:11826-11905）每帧调 `keaadophhbp.cbhjcnkojgc(鱼, 装备, dt)`；命中→ `gclanlcddlf.hdakcoelhfa()`（Fish.hdakcoelhfa，Fish.cs:962-981：结束调度器、状态→3 `gchkehljkne`）+ `lklhemjaeij()`。

- `keaadophhbp.cbhjcnkojgc`（keaadophhbp.cs:63-138）：钓组状态≠`nnioeaijfic` 时仅累加 `ngpacgmacio(dt)`，累计≥**0.01s** 即真；否则遍历检查器列表，任一为真即真。检查器集合在 `fidoenfoijo`（:140-190）：`bemnjjapbec` 恒注册；钓具种类 `lmogjhgbgjj()`∈{1,2,15,16} 再加 `agikabffcia`；另加 `iajekdeicjh`。
- **`bemnjjapbec`**（bemnjjapbec.cs:29-87）：全部满足才判可上岸——
  1. `affmmoiodac.mfgmldbfaad() > 0.5`；
  2. 距离 `dghnodilfdk() ≤ 玩家参数 hflelkknkfa()×1.5`（构造 :22-27）；
  3. 收线进度 `fjamjbjmfdd() > 0.85`；
  4. 鱼 x > 玩家x−0.5 且 < 玩家x+hflelkknkfa+0.5（:57-69）；
  5. 水平距离 |Δxz| < **1.25 m**（:76-80）。
- **`agikabffcia`**（agikabffcia.cs:22-37，浮漂/馈送类钓组）：`apajggddldc() < −0.2`（鱼高于某参考面）且 `dghnodilfdk() ≤ hflelkknkfa×0.801` 且 `fjamjbjmfdd() > 0.85`。
- **`iajekdeicjh`**：需 `ihoebonagaj.ccdgkkioghb()`（抄网就位），比较玩家持鱼Transform与抄网Transform一致，距离 ≤ `hflelkknkfa×c`（`level_012_north_sea` 特判，文件内 :20-35）。
- 附：Fisher 侧"可提上岸"标志 `cpjphijiian`：钓具=`adkkkfpcdjo` 时要求收线进度 `real8 > 0.92`，其余由子条件给出（Fisher.cs:11888-11898）；收线进度 `real8 = clamp01(mdpeemdgbgp(线长,dt,...)×dt + 累计)`（:11851-11855）。

## 6. 搏鱼 60 秒后安全余量衰减（kejihbadeej.cs:99-281）

驱动：`gfgapohfogm.ibofhdekmpf` 每帧 `ogokcpppaep.Update(鱼, 当前机动.pfdkplenmif(), 水况, dt)`（gfgapohfogm.cs:210-211）——**衰减速率被当前机动的速度系数缩放**（base 1.0，300=0.7，307=0.4，306/308=0.1）。

完整公式（kejihbadeej.cs:116-149）：

```
基准余量 coffdfkgfaa = 1 − clamp01(f20.adiddjkcdan 经水况修正; 非eeajjmkbjbi时×0.05)   // ctor :313-339
if (nklaengaiaf())  // 搏鱼激活
    t += dt
    rate real1 =
        t > 3600        → 0
        t ≤ 60          → coffdfkgfaa                       // 前 60 秒不衰减
        60 < t ≤ 3600   → lerp(coffdfkgfaa, coffdfkgfaa×0.0125,
                              smoothstep(clamp01((t−60)/300)))   // u³(6u²−15u+10)，300 秒内平滑衰减到 1.25%
然后：
if 线拉力 hpihecdpehm() < 阈值 bihlodbgigm(=min(体重×0.025,0.25)×g):
    rate = max(1, (1−√(拉力/阈值))×35) × coffdfkgfaa        // 线松弛时脱钩速率放大至多 35 倍
    （nklaengaiaf()==false 时再 ×1.3）
else:
    rate = real1 × (clamp01(pjonppibhbl)×0.75 + 0.25)
rate ×= clamp(mckpinnohmg(), 0.3, 1)                        // 鱼钩固定度
rate ×= 钓组系数: 钓具id−12 ∈ {0..5,12..17,20,22} → 1/150, 其余/默认 → 1/30   // :48-96, 207-247
!nklaengaiaf() → ×1.2;  nklaengaiaf()&&mggbpmablco() → 0;  meaopokeiod()<0.01 → 0
进度累积器 oplaiopnmcc 初值= coffdfkgfaa，每帧 += rate；平滑值 ≥ 1 → mkkielhmcgn →
fcalbphgmjk = true（脱钩）。gfgapohfogm.bachphcpjff 里该标志 → 原因码2（gfgapohfogm.cs:236-245）
```

## 7. 主要未确认项（明确标注）

1. `ndmfaiddmhj.kejedhafmmj()/cmiggaknepg()/afbdfgplfii()/nnpkmkiclhg()` 方法体在反编译中丢失（仅 Fish.cs:1073-1107 调用点），期望速度与 param1 的精确换算未确认。
2. BodyMotor.cs:349 的 `obj54` 变量被反编译器吞掉，按上下文推断 = real14（期望速度）。
3. `affmmoiodac` 三个 0..1 度量 `pjonppibhbl / mfgmldbfaad / pipffmcoolh` 与 `gjhiologibj.hmedeknmdgc / pcilaefoclj / oblfcknojkn / jlkphadoecp / meaopokeiod` 的精确定义（距离/深度/张力的具体归一化）未确认；文中语义均为多点用法反推（如 400 组权重条件、kejihbadeej 用法、lkdpgdhoclm 用法）。
4. `bhnjkmgknoe`（104）内置默认行转角列两值同为 −1.0（恒硬左转），是否有意未确认（服务端曲线行会覆盖）。
5. `jlofffeogbi`（302/306 深度相关乘子）精确定义未确认（从两处相反的用法推断为"接近水面程度"类度量）。
6. 309 后段 `AddForce` 的完整弹道参数（`mfconjhnjmp/njimlmganam` 30..85°）只确认数值范围，物理含义（跃出角）未确认。
