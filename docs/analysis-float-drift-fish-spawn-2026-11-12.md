# 浮钓顺水漂流不发鱼排查

> 排查日期：2026-11-12
> 
> 范围：服务端浮钓发鱼链路、14/7 钓组动画状态、客户端动作位定义。

## 结论

当前服务端存在一个与“浮钓随水漂流不发鱼”高度吻合的状态门问题。

浮钓顺流动作使用客户端动画位 `0x40`（`StreamMoving`）。服务端的核心发鱼入口却只把 `0x200`、`0x400`、`0x800`、`0x1000` 认定为“钓组在水中”，没有把 `0x40` 纳入入水判定。

如果浮钓顺流期间 14/7 只上报 `f5=0x40`，服务端会一直停在 `NeedBite=true`、`InWater=false`，不会执行判鱼，也不会推送 14/14。

## 当前发鱼链路

1. 14/2 抛竿后调用 `scheduleBite`。
2. 新一杆将 `InWater` 重置为 `false`，并设置咬钩定时器。
3. 定时器到点进入 `onRodBiteTimerTicket`。
4. 如果 `InWater` 仍为 `false`，服务端只设置 `NeedBite=true` 并返回，不调用 `rollFishForRig`。
5. 后续 14/7 只有命中 `rigAnimInWaterMask`，才会设置 `InWater=true`。
6. 如果此前已经设置 `NeedBite=true`，入水状态变化会触发 `scheduleBiteForHooked`，随后才继续判鱼和推送 14/14。

## 直接证据

### 1. 入水位掩码遗漏 `0x40`

服务端当前定义如下：

```go
const rigAnimInWaterMask = 0x200 | 0x400 | 0x800 | 0x1000
```

位置：`internal/gameserver/fishing.go:300-303`。

14/7 处理器只按这个掩码设置 `InWater`：

```go
if st.CastActive && !st.InWater && anim&rigAnimInWaterMask != 0 {
    st.InWater = true
    enterWater = true
    if st.NeedBite {
        st.NeedBite = false
        needBite = true
    }
}
```

位置：`internal/gameserver/handlers_game.go:1169-1179`。

### 2. 未入水时不会判鱼

定时器到点后，`InWater=false` 会直接走挂起分支：

```go
if !st.InWater {
    st.NeedBite = true
    st.mu.Unlock()
    return
}
```

位置：`internal/gameserver/fishing.go:2169-2174`。

这条分支发生后，必须再收到能命中 `rigAnimInWaterMask` 的 14/7 状态，才会恢复发鱼。

### 3. `0x40` 的客户端语义已确认

服务端在 `fish_ecology.go` 中将 `0x40` 定义为顺水漂流：

```go
const (
    lureMotionDriftBit uint32 = 0x40
    lureMotionJigBit   uint32 = 0x80
)
```

位置：`internal/gameserver/fish_ecology.go:410-415`。

本地客户端逆向资料 `E:\RF4\cs\ida-docs\enums\fhiclnhgoge.md` 也将数值 64 对应为 `StreamMoving`，并记录为顺流移动。

### 4. 浮钓类型确实走这条通用门

浮钓类型包括 1、2、3、4、21、30、31，位置：`internal/gameserver/rigmeta.go:70-73`。

这些类型不属于 `rigTypeLureFamily`，因此不会绕过 `InWater` 检查。浮钓即使已经记录了 `LureMotionMask=0x40`，也仍然会在核心发鱼入口被 `InWater` 拦住。

## 状态表现

当一杆在定时器到点前没有收到有效入水位时，典型状态是：

```text
CastActive = true
InWater    = false
NeedBite   = true
Pending    = nil
```

如果后续只持续收到 `f5=0x40`，`InWater` 不会改变，`NeedBite` 也不会被清除，因此不会出现 14/14。

如果 `0x40` 同时叠加了 `0x200`、`0x400`、`0x800` 或 `0x1000`，当前代码可以正常继续发鱼。这解释了为什么普通浮钓可能正常，而顺流模式可能表现为完全没口。

## 相关语义不一致

服务端的动作前置条件已经把 `0x40` 视为水中运动状态，见 `internal/gameserver/fishing_bait_rules.go:437-459` 和 `internal/gameserver/fish_ecology.go:417-420`。

但是这套动作掩码只参与拟饵规则和生态权重，不能解除核心发鱼流程的 `InWater` 门槛。当前代码因此出现“漂流动作判定认可，发鱼入口不认可”的口径分裂。

另外，`E:\RF4\cs\ida-docs\object_chains\fishing_rest.md` 仍保留旧版“14/7 的 `flag&0x200` 触发判鱼”描述。当前服务端已经改为“延迟定时器 + 入水状态门 + `rollFishForRig`”，该文档与现行实现不是同一套时序，应在后续协议资料整理时单独校准。

## 建议修复

建议增加按钓组类型区分的入水判定，不直接把所有动作位都当作入水：

```go
func rigAnimInWater(anim uint32, rigType int64) bool {
    return anim&rigAnimInWaterMask != 0 ||
        (floatRigTypes[rigType] && anim&lureMotionDriftBit != 0)
}
```

14/7 处理器改用该判定，并补充回归测试：

- 浮钓类型收到 `0x40` 时设置 `InWater=true`。
- `NeedBite=true` 时收到 `0x40` 会清除挂起状态并补排咬钩。
- `0x0` 仍不能被当作入水。
- 非浮钓类型不因单独的 `0x40` 改变现有入水语义，除非有新的客户端报文证据。

## 验证状态

本次未修改业务代码，也未改变现有用户工作区改动。已通过当前源码、提交历史和本地客户端逆向资料完成静态核对。

尝试运行 Go 回归测试时，当前环境没有可用的 `go` 可执行文件，因此尚未完成编译测试和实机报文复现。上线修复前应先抓取一组浮钓顺流 14/7 报文，确认是否存在“持续 `0x40`、没有水中位”的实际序列。
