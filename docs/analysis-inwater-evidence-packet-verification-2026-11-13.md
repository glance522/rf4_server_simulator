# 入水证据官方报文核验与浮钓顺流修复方案

> 排查日期：2026-11-13
>
> 前置文档（同批排查、随独立 PR 提交）：四类钓法状态门排查
> （analysis-fishing-method-state-gates-2026-11-12.md）、
> 浮钓顺水漂流不发鱼排查（analysis-float-drift-fish-spawn-2026-11-12.md）。
>
> 本文完成前两篇遗留的"待报文确认"项：六份官方 pcap 全量重解密，解码约 1130 帧
> 14/7 的 f5 位序列并做逐杆时间线对照，给出完整修改方案（尚未实施）。

## 一、抓包来源与工具

| 产物 | 位置 |
|---|---|
| 原始 pcap | `logs/captures/rf4_attempt2_20261110.pcapng`、`tools/wireshark/{底钓,底钓2,浮子,海钓,路亚,路亚2}.pcapng` |
| 解密管线 | `logs/captures/official_trace/decrypt_rf4.py`（本次复制到临时目录并加 `RF4_DUMP_147` 开关，完整导出 14/7 信封） |
| 批量解码器 | `tmp_analysis/bulk147/main.go`（`go run ./tmp_analysis/bulk147 <tsv>`，输出每帧 `时间戳 f5/f6/f7`） |
| 时间线合并 | 14/2/3/9/11/12/14 事件与 14/7 f5 序列按时间戳合并 |

解密要点沿用 `logs/captures/official_trace/HANDOFF.md` 第一节；路亚流3（原判失败）
本次解密成功，224 帧 14/7 + 3 次 14/14 完整可用。

**注意**：本次使用的 Go 工具链在 `C:\Users\losei\tools\go\bin`（go1.27.3），
前两篇文档"Go 不可用"的结论已过时。

## 二、各钓法官方实测 f5 位序（客户端 4.0.25034）

| 会话 | 钓法 | 14/7 帧数 | f5 分布 |
|---|---|---|---|
| rf4_attempt2 | 底钓 | 435 | `0x800`×402、`0x0`×33 |
| 底钓2 | 底钓 | 284 | `0x0`×243、`0x800`×41 |
| 浮子 | 浮钓 | 100 | `0x0`×49、`0x40`×35、`0x80000`×4、`0x800`×4、`0x2000`×4、`0x2040`×2、`0x200`×2 |
| 海钓 | 海钓铁板 50+ | 82 | `0x880000`×35、`0x800000`×3、`0x480000`×3，其余动作帧全含 `0x400` |
| 路亚流2 | 路亚 | 5 | `0x400`×2、`0x0`×2、`0x20`×1 |
| 路亚流3 | 路亚 | 224 | `0x410`×164、`0x400`×23、`0x420`×14、`0x20`×9、`0x2400`×6、`0x0`×6、`0x40400`×1、`0x140400`×1 |

### 关键时间线摘录（浮子会话，t=0 为首个 14/2）

```text
cast@277.27  0x80000(278) → 0x0(279) → 0x40(281)         [纯顺流证据]
14/14 @289.50、14/11 @293.95  ← 官方在无任何核心入水位的杆上照常派鱼

cast@301.26  0x0(302-303) → 0x40 持续 304.98~368.14（约 64 秒）
14/14 @369.67 ← 纯 0x40 期间发鱼

cast@44.72   0x40/0x2040 交替持续约 28 秒，官方该杆未发鱼（概率使然，
             不影响结论：顺流状态不阻止派鱼，旧掩码则会永久阻止）

cast@375.10  0x0 → 0x200(383) → 0x200(400) → 14/14 @403.44（就位杆对照）
```

路亚会话（流3）：

```text
每杆模式：0x20(激溅,1-2帧) → 0x420 → 0x400/0x410（收线立即开始）
cast@348.96：0x0 六秒（静置下沉）→ 0x140400(356.75) → 17/15 → 0x400 → 14/14 @359.28
← 官方在 0x0 静置期不派鱼，收线后才发；与当前服务端 0x0 拦截行为一致
```

海钓会话：

```text
cast@231.45：0x800000/0x880000 下沉 23 秒（233~254）→ 0x480000 → 0x400000
→ 0x400(258.60) → 14/14 @258.68
← 海钓铁板下沉相位 14~23 秒完全不含核心入水位
```

## 三、结论（对照前两篇文档的分级）

1. **浮钓顺流 `0x40` 由"高概率"升为"实锤"**：官方在只有 `0x40`/`0x2040` 证据的杆上
   正常派鱼；旧掩码下同序列必然停在 `NeedBite=true`。
2. **新发现 A：浮钓"下沉行程接口"（onTheDrop，fishing.go:2197）是死路径**。
   该分支 2026-11-05（88b3f5a）才加入，晚于入水门 e36e187（2026-11-03）两天；
   浮钓下沉相位实测报 `0x80000`（不在掩码），定时器到点永远先被 `!st.InWater` 挂起，
   `onTheDrop` 从不可达。
3. **新发现 B（实机验证后升级）**：海钓铁板下沉期 14~26 秒无基础位
   （`0x800000`/`0x880000`），且 **14/3 在海钓语义下是"触底"而非"入水"**——
   2026-11-13 03:06 实机实测（loc12_23，rig_MarinePilker2）：03:06:09 抛竿，
   下沉 26 秒无口，14/3 于 03:06:36.7（铁板触底瞬间）才到达，鱼在触底后 4.5 秒
   才发出。深水下沉窗口整个丢失。`0x800000` 是真实动画位（"底上移动"），
   被 `lureMotionStatusMask=0x007FFFFF` 误归为阶段高位。修复见 P4（路亚族
   下沉/底层位证据）。
4. **新发现 C（2026-11-13 二次修订）**：路亚全部实测运动帧（`0x410/0x420/0x2400`）
   都含 `0x400`，收线场景现有掩码已覆盖。但**线杯开着自由下沉是拟饵的正常玩法
   （拟饵接口），该相位实测 `f5=0x0`、无任何姿态位**（路亚流3 cast@348.96：
   `0x20` 激溅后 6 秒纯 `0x0`，14/3 正常上报）——`0x0` 拦截会吞掉下沉接口口。
   补救证据见「14/3 的客户端发送条件」：14/3 是客户端"钩体在水中"的权威上报，
   可作为路亚（及全体钓法）的入水证据。`0x20 激溅` 不应加为路亚证据
   （线杯开着也激溅，区分度差）。
5. **新发现 D**：fishing.go:300-302 注释把 `0x400` 称作"静止状态"，
   权威枚举表（`E:\RF4\cs\ida-docs\enums\fhiclnhgoge.md`）证实它是
   UniformMotion 收线运动（动态标签），注释需修正。
6. 记录不在本次范围：`fish_ecology.go` 的 `0x40000=LittleJerk/0x20000=MediumJerk`
   与枚举表（`0x10000=LittleJerk`、`0x40000=DEV 下潜一阶段`）冲突，
   海钓实测 `0x810400` 含 `0x10000`，动作分类问题另行排查。

### 14/3 的客户端发送条件（关键证据）

客户端发送点 `FishingSet.cs:3383 gpnmjgkigmg`（每杆一次性，`pkhlpkmhghe` 锁存）：

```csharp
if (!this.pkhlpkmhghe) {
    affmmoiodac1 = this.bgjdabgkbla.baglokloobi();   // 钩体 BodyEnvironment
    if (affmmoiodac1.khmdmakaipg()) {                // 仅当钩体已在水中
        this.pkhlpkmhghe = true;
        bbpmdcodcee.fooolnnjbao(...);                // → 14/3 fall（复用 obj19 带落点）
    }
}
```

`khmdmakaipg()` 是 `RF4.Client.BodyPhysics.BodyEnvironment` 的"在水中"判定
（`RigBottomMarker.cs:414`、`LureBottomTouchSimulator.cs:21` 均以它作为
"钩体已入水"前置）——**岸上落点不会发 14/3**。抓包侧：路亚 7 杆中 6 杆有 14/3
（下沉期 `f5=0x0` 的那杆也有）；浮钓 9 杆只有 5 杆按时出现（顺流杆多缺失，
立漂条件未满足），说明 14/3 **精度高、召回不全**，只能做补充证据、
不能替代 f5 位族。

### 线杯开着与入水门的关系（2026-11-13 维护者纠正后修订）

> 维护者两次纠正：①**线杯开着是一些钓法的正常玩法**；②**路亚不关线杯、
> 拟饵自由下沉也会接口**。客户端侧线杯/离合（`pocnjfgklcp.hjndpplmjbd()`）
> 与卸力全松（`bhhaopagglk.khdiajkiehh`）只改变线张力，不改 14/7 钓组动画位；
> 服务端也没有任何线杯字段。因此入水门从不"拒绝线杯开着"，
> 它只看钓组姿态证据是否齐备：

| 钓法 | 线杯开着是否正常玩法 | 线杯开时的姿态证据（官方抓包/客户端语义） | 结论 |
|---|---|---|---|
| 底钓（12-17、23-29、32、34） | **是**（自由放线等咬） | 就位 `0x800` 照报；下沉期 `0x0` | 现状即可发鱼（就位后）；14/3 可把入水提前 |
| 浮钓（1-4、21、30、31） | 是 | 静置 `0x200`、顺流 `0x40`、下沉 `0x80000`；顺流杆常无 14/3 | **P1+P2 补齐三种姿态**；顺流卡死即"线杯开浮钓没鱼"的直接根因 |
| 路亚（5-11、19、20、22） | **是**（自由下沉等接口） | 下沉/静置期 `f5=0x0`，无姿态位；14/3 正常上报 | **P3（14/3 证据）补上**；收线场景 `0x400` 现状已覆盖 |
| 海钓铁板（50-54） | 否（需收线/抖停） | 动作帧全含 `0x400`；下沉期 `0x880000` | P3 使下沉期同样可达（铁板 on-the-drop），行为前移见方案说明 |

原始用户反馈"线杯不关闭没有鱼"的观察大概率落在浮钓：线杯开 → 浮子随流
自由漂移 → 只上报 `0x40` → 被旧掩码卡死。与浮钓顺流是**同一根因**。

## 四、修改方案（2026-11-13 已实施，走快速路径）

实施状态：P1+P2+P3+P4 全部落地（P4 为 2026-11-13 03:06 实机回归反馈后的第二轮：
海钓铁板 26 秒下沉期无口，14/3 触底才上报——路亚族补下沉/底层位证据）。
改动文件：`internal/gameserver/fishing.go`（位族定义与 rigAnimInWater）、
`internal/gameserver/handlers_game.go`（14/7 调用点 + hFishingFall 入水锁存）、
`internal/gameserver/fishing_semantics_test.go`（回归测试）、
`docs/README.md`（本文索引登记）。

门禁结果（go1.27.3，`/c/Users/losei/tools/go/bin`）：

- `go build ./...` 通过；`go vet ./...` 通过。
- 新增回归测试
  `TestFishingStepInWaterEvidenceByRigFamily`、`TestFishingFallLatchesInWaterAndReschedules`
  及相关既有测试（TickRepair/TickRecomputes/LeftStatus/Groundbait 全系）全部 PASS。
- 全量 `go test ./...`：仅 `TestFishMarketFlow`、`TestCafeNativeFlowPaysAndPushesRemoval`
  两个"等待推送超时"失败——**用 git stash 对照基线复跑，两例在未改动树上同样失败**
  （与 HANDOFF 2026-11-11 记录的既有状态泄漏一致），本次改动零新增失败。
- doccheck：本文已登记 docs/README.md；其余 5 条"未登记"是 2026-11-12 遗留的
  未跟踪分析文档（基线，未处理）。

待实机回归（三组）：①浮钓顺流水域（线杯开）一杆 → 日志 `钓组在水中 f5=0x40`，
正常出 14/14；②路亚抛竿不关线杯、等拟饵下沉 → 应出现 `钓组在水中（14/3），补排咬钩`
并按时接口；③岸上抛一竿 → 不得出现"钓组在水中"（14/3 岸上不发，验证
BodyEnvironment 判定口径）。

三项改动：P1 浮钓顺流、P2 浮钓下沉（救活 onTheDrop）、
P3 14/3 入水证据（路亚线杯开自由下沉接口，维护者 2026-11-13 玩法裁定）。

### 代码改动（2 文件）

`internal/gameserver/fishing.go`（rigAnimInWaterMask 旁）：

```go
// rigAnimDiveBit 是饵/拟饵下沉相位的动画位（官方抓包 2026-11-13：浮钓下沉期
// 上报 0x80000；海钓铁板下沉为 0x880000 含此位。底钓/路亚下沉期实测 0x0，
// 它们的下沉入水由 P3 的 14/3 证据覆盖）。
const rigAnimDiveBit uint32 = 0x80000

// rigAnimLureSinkMask 是路亚族（含海钓铁板 50-54）下沉/底层运动的动画位：
// 0x80000 VerticalDive、0x800000 底上移动（枚举表 DEV 标签实为铁板下沉期的
// 真实动画位）、0x400000 MotionInBottomLayer。海钓下沉相位 0x800000/0x880000/
// 0x80000 持续 14~26 秒且不含基础位，且实机实测 14/3 触底才上报，路亚族
// 下沉期必须由 f5 位本身构成入水证据。
const rigAnimLureSinkMask uint32 = 0x80000 | 0x800000 | 0x400000

// rigAnimInWater 判断 14/7 动画位是否构成"钓组在水中"的证据。
// 基础位族（0x200/0x400/0x800/0x1000）之外：浮钓接受顺流 0x40 与下沉
// 0x80000（顺流杆 28~64 秒无基础位，官方照常派鱼）；路亚族接受下沉/底层位
// （海钓铁板 26 秒下沉期无基础位、14/3 触底才来）。0x0 仍不是证据，挡岸上抛竿。
func rigAnimInWater(anim uint32, rigType int64) bool {
	if anim&rigAnimInWaterMask != 0 {
		return true
	}
	if floatRigTypes[rigType] {
		return anim&(lureMotionDriftBit|rigAnimDiveBit) != 0
	}
	if rigTypeLureFamily[rigType] {
		return anim&rigAnimLureSinkMask != 0
	}
	return false
}
```

同时修正 rigAnimInWaterMask 注释（0x400=UniformMotion 收线运动，非静止）。

`internal/gameserver/handlers_game.go:1172`（14/7 调用点）：

```go
if st.CastActive && !st.InWater && rigAnimInWater(anim, st.RigType) {
```

`internal/gameserver/handlers_game.go hFishingFall`（14/3，P3）：入水证据与
14/7 的 enterWater 同构——钩体在水中（客户端 BodyEnvironment 判定，岸上不发）
就锁存 InWater、清 NeedBite 并补排；窝料扣减沿用既有幂等键（竿+抛竿时刻），
14/3 先到则 14/7 的 enterWater 不再触发，两路不重复扣。**打窝竿必须排除**：
它不钓鱼，今天发鱼定时器到点时因“永不入水”挂起（意外护法），若给它锁存
InWater 就会开始出鱼：

```go
st.FallSeen = true
st.FallTs = time.Now()
rigType, gear := st.RigType, st.GearGuid
enterWater, needBite := false, false
if st.CastActive && !st.InWater && !spodRigTypes[rigType] {
    st.InWater = true
    enterWater = true
    if st.NeedBite {
        st.NeedBite = false
        needBite = true
    }
}
st.mu.Unlock()
if spodRigTypes[rigType] {
    // 既有打窝器扣窝料分支不动
} else if enterWater {
    if err := srv.consumeCastBait(ctx.Sess, st); err != nil { … }
}
if needBite {
    ctx.Sess.Srv.scheduleBiteForHooked(ctx.Sess, st)
}
```

P3 的行为影响面：路亚线杯开下沉/静置从"永不发鱼"变为可接口（目标修复）；
底钓入水提前到落水时刻，但 12 秒就位守门照旧，实际派鱼仍在就位后；海钓铁板
下沉期从"挂起到收线"变为可判鱼（铁板 on-the-drop，与玩法一致）；打窝竿
**排除在锁存之外**，行为与改动前完全一致（14/3 只扣窝料）。

### 明确不做

- 底钓/海钓不加 f5 位（实测证据充分，见结论 3：底钓 `0x800` 完美覆盖、海钓动作帧全含 `0x400`）
- 路亚不加 f5 位（全部实测运动帧含 `0x400`，下沉期 `0x0` 由 P3 兜底）；不加 `0x20` 为证据；不动 `lureMotionStatusMask`；不动 jerks 位映射
- 入水门不引入任何线杯字段/开关——服务端本来就不感知线杯，线杯开着的正常玩法全部由姿态证据 + 14/3 覆盖（见「线杯开着与入水门的关系」表）
- 不启用 `water_pos.go` 位置判定（`water_bounds.json` 数据表不存在，建表另行立项）

### 回归测试（`fishing_semantics_test.go`，复用 hFishingStep/hFishingFall 假帧模式）

| 断言 | 期望 |
|---|---|
| 浮钓 rig=1：`0x40` → InWater=true、NeedBite 清除并补排 | 新增 P1 |
| 浮钓：`0x2040`、`0x80000` → 入水 | 新增 P2 |
| 浮钓：`0x0` → 不入水（挡岸上） | 保持 |
| 路亚 rig=5：抛竿后仅 14/3（无 14/7 或 `f5=0x0`）→ InWater=true、补排 | 新增 P3 |
| 路亚 rig=5：`0x400` 入水（现状回归） | 保持 |
| 底钓 rig=12：`0x40` 单独 → 不入水 | 保持 |
| 海钓 rig=50：`0x880000`、`0x800000`、`0x400000` 下沉/底层位 → 入水（P4）；`0x400` 入水（基础位族） | 新增 P4 |
| 打窝竿：14/3 不锁存入水、只扣窝料（防"打窝竿出鱼"回归） | 新增 P3 守卫 |

### 验证

```bash
export PATH=/c/Users/losei/tools/go/bin:$PATH
go build ./... && go vet ./... && go test ./internal/gameserver/ -run TestFishing
```

实机回归三组：①浮钓顺流水域（线杯开）一杆 → 日志 `钓组在水中 f5=0x40`，正常出 14/14；
②路亚抛竿不关线杯、等拟饵下沉 → 应出现 `钓组在水中（14/3）` 类日志并按时接口；
③岸上抛一竿 → 服务端不得进入 `InWater`（14/3 不应上报，日志无"钓组在水中"）。
