#!/usr/bin/env python3
"""重新生成鱼种生态表的水层区间（profile_version 3 -> 4）。

背景：v3 表 252 种鱼没有一条表层鱼（layer_pref 全部 >= ~0.46），且区间普遍
宽 0.66~0.9，水面系/中层/跳底拟饵共享同一份水层权重，无法分层。生成器
（docs/gameplay.md 提到的 tools/genfishdata）不在仓库，本脚本补回该能力，
只重写水层三元组，其余字段（食性/温度/动作权重等）原样继承 v3。

水层坐标：0=水面、1=水底（与 internal/gameserver/fish_ecology.go 一致）。

证据源：
  1. companion_bait_fish（主库，官服周纪录拟饵×鱼证据量）——主驱动；
  2. 食性组先验（v3 diet_json 分组）——平滑与兜底；
  3. fishing_bait_rules.txt 的 bottomOnly（"底钓只吃"）——硬下界。
拟饵水层带 = loadLureAttributes 的 TypeID->DiveDepth 表（fish_bite_environment.go），
对 stickbait(9)/Flying worm(16) 按习性 override，popper/mouse 等名词特判表层。

用法：
  python -I tools/regen_fish_layers.py \
      --static server_data/static.sqlite3 \
      --db data/rf4server.db \
      --in internal/gameserver/assets/fish_ecology_defs.json \
      --rules internal/gameserver/fishing_bait_rules.txt \
      --out /tmp/fish_ecology_defs_v4.json \
      --report /tmp/fish_layers_report.md

--out 不传则只出报告不落盘。落盘后由 ensureFishEcology（profile_version 升级
条件 WHERE profile_version < excluded）在下次启动时覆盖落库。
"""

import argparse
import collections
import json
import math
import re
import sqlite3
import sys

# 拟饵 TypeID -> 代表泳层中心。除标注 override 外与
# internal/gameserver/fish_bite_environment.go loadLureAttributes 的 DiveDepth 一致。
TYPE_DEPTH = {
    1: 0.34,  # spinner 旋转饵
    2: 0.48,  # spoon 勺子亮片
    3: 0.56,  # foam 泡沫橡胶鱼
    4: 0.62,  # crank 曲柄饵
    5: 0.10,  # popper 波扒
    6: 0.76,  # pilker 海钓铁板
    7: 0.44,  # jerkbait 抽停饵
    8: 0.56,  # tube 铅管
    9: 0.30,  # stickbait 沉水铅笔（override：代码给 .56，习性为上层搜索）
    10: 0.10,  # surface 老鼠/松鼠水面系
    11: 0.18,  # fly 飞蝇
    12: 0.72,  # monojig 海钓单钩
    13: 0.56,  # soft_eel 软鳗
    14: 0.56,  # octopus 章鱼饵
    15: 0.56,  # shrimp 虾饵
    16: 0.18,  # flying worm 浮水缓沉飞钓饵（override：代码给 .56）
    17: 0.78,  # color beads 串珠（海钓跳底）
    18: 0.72,  # jig head 铅头钩
    19: 0.72,  # jig 铅头钩组
    20: 0.72,  # spinnerbait/vib 复合亮片
}

SURFACE_RE = re.compile(r"popper|topwater|mouse|squirrel|walker|walker|浮水|水面")
BOTTOM_RE = re.compile(r"jig head|flip jig|pilker|monojig|color beads|jighead")

SURFACE_CENTER = 0.08
BOTTOM_CENTER = 0.80

# 分位数 -> 区间；先验混合比例；区间宽度钳制
Q_LOW, Q_MID, Q_HIGH = 0.10, 0.50, 0.90
PRIOR_POINT_SHARE = 0.12  # 先验 3 点各占总证据的份额
MIN_EVIDENCE_ROWS = 3
MIN_EVIDENCE_WEIGHT = 50.0
MIN_WIDTH, MAX_WIDTH = 0.22, 0.45

# 分辨力指标用的简化因子（与 PR-2 拟定公式同形：区间内地板、区间外指数衰减）
IN_RANGE_FLOOR = 0.75
DECAY_K = 1.5
FACTOR_FLOOR = 0.02

SURFACE_ACTIONS = ("walk_dog", "surface_motionless", "splash")
BOTTOM_ACTIONS = ("bottom_motionless", "bottom_contact", "bottom_move", "bottom_layer_move")


def clamp01(v):
    return max(0.0, min(1.0, v))


def bait_depth(item_id, name_zh, type_id):
    """拟饵的代表泳层中心。"""
    lower = (item_id + " " + (name_zh or "")).lower()
    if SURFACE_RE.search(lower):
        return SURFACE_CENTER
    if BOTTOM_RE.search(lower):
        return BOTTOM_CENTER
    return TYPE_DEPTH.get(type_id)


def diet_group(diet):
    """按 v3 diet_json 把鱼分到水层先验组。

    diet 键：crustacean/worm/lure/fish/plant/boilie/plankton（全表统计）。
    boilie（煮饵颗粒）是鲤科主导食性，与 plant 合并算底栖侧；
    worm 主导而 plant 不足的（欧鲌等上层食虫小鱼）归杂食，不进底栖；
    plankton 主导是上层滤食（鲱/西鲱类）。
    """
    lure_fish = diet.get("lure", 0) + diet.get("fish", 0) + diet.get("crustacean", 0) * 0.5
    plant_side = diet.get("plant", 0) + diet.get("boilie", 0)
    worm = diet.get("worm", 0)
    if diet.get("plankton", 0) >= 1.0:
        return "pelagic", 0.10, 0.55, 0.30  # 上层滤食
    if lure_fish > plant_side * 1.3 and lure_fish > worm * 1.2:
        return "predator", 0.22, 0.72, 0.45  # 掠食：中上层
    if plant_side > lure_fish and plant_side > worm:
        return "bottom", 0.52, 0.96, 0.78  # 底栖草食/颗粒
    return "omnivorous", 0.38, 0.88, 0.62  # 杂食中下层（虫食为主等）


def load_rules_bottom_only(path):
    """规则文本里 bottomOnly 非空、含 jig 阶梯前置的鱼名（归一化键）。"""
    bottom_only, jig_cond = set(), set()
    if not path:
        return bottom_only, jig_cond
    for line in open(path, encoding="utf-8"):
        if "  吃 " not in line or "  前置条件 " not in line:
            continue
        name, tail = line.split("  吃 ", 1)
        bait, cond = tail.split("  前置条件 ", 1)
        key = re.sub(r"[\s()（）\ufeff]", "", name).lower()
        if "底钓只吃" in bait:
            bottom_only.add(key)
        if "jig阶梯" in cond and "或" not in cond:
            jig_cond.add(key)
    return bottom_only, jig_cond


def load_lures(static):
    """item_id -> (type_id, name_zh)，只取 category 17。"""
    out = {}
    for item_id, cat, raw in static.execute(
        "SELECT item_id, category, COALESCE(attrs_json,'{}') FROM item_def WHERE category=17"
    ):
        try:
            attrs = json.loads(raw)
        except ValueError:
            attrs = {}
        t = attrs.get("mbemakijdeh")
        out[item_id] = int(t) if isinstance(t, (int, float)) else 0
    names = {}
    for item_id, name in static.execute("SELECT item_id, name_zh FROM item_catalog"):
        if item_id in out:
            names[item_id] = name
    return out, names


def load_fish_names(static):
    names = {}
    for key, name in static.execute(
        "SELECT fish_key, fish_zh FROM loc_wiki_fish WHERE fish_zh<>'' "
        "UNION SELECT fish_key, name_zh FROM loc_fish WHERE name_zh<>''"
    ):
        names.setdefault(key, name)
    return names


def load_evidence(db):
    """fish_key -> [(bait_id, evidence)]，合并本服 loc_bait_fish 中 item_id 形式的行。"""
    ev = collections.defaultdict(lambda: collections.defaultdict(float))
    for bait, fish, e in db.execute("SELECT bait_id, fish_key, evidence FROM companion_bait_fish"):
        if e > 0:
            ev[fish][bait] += float(e)
    return ev


def load_rf4records_layer(db, lures):
    """rf4records_catch 聚合每鱼的拟饵水层证据中心（第二独立证据源）。

    返回 fish_key -> (中心, 纪录数)。只统计已映射到拟饵 TypeID 的纪录；
    天然饵水层中性不参与。来源 tools/fetch_rf4records.py（官方周纪录纪录级，
    可溯源），与 companion（累计快照）时间窗独立，互为校验。
    """
    w = collections.defaultdict(lambda: [0.0, 0])
    for fk, bait_id, n in db.execute(
        "SELECT fish_key, bait_id, count(*) FROM rf4records_catch"
        " WHERE fish_key<>'' AND bait_id<>'' GROUP BY fish_key, bait_id"
    ):
        t = lures.get(bait_id)
        depth = bait_depth(bait_id, "", t) if t else None
        if depth is None:
            continue
        w[fk][0] += n * depth
        w[fk][1] += n
    return {k: (v[0] / v[1], v[1]) for k, v in w.items()}


def wquantile(sorted_pairs, q):
    """sorted_pairs: [(layer, weight)] 按 layer 升序；返回加权分位数。"""
    total = sum(w for _, w in sorted_pairs)
    if total <= 0:
        return None
    acc = 0.0
    for layer, w in sorted_pairs:
        acc += w
        if acc >= total * q:
            return layer
    return sorted_pairs[-1][0]


def clamp_width(lo, pref, hi, max_w=MAX_WIDTH):
    wid = hi - lo
    if wid < MIN_WIDTH:
        pad = (MIN_WIDTH - wid) / 2
        lo, hi = lo - pad, hi + pad
    elif max_w and wid > max_w:
        pad = (wid - max_w) / 2
        lo, hi = lo + pad, hi - pad
    shift = 0.0
    if lo < 0:
        shift = -lo
    elif hi > 1:
        shift = 1 - hi
    return clamp01(lo + shift), clamp01(hi + shift)


def layer_factor(target, lo, pref, hi):
    """简化水层因子：区间(±0.08 容差)内地板，区间外指数衰减。"""
    half = max(0.08, (hi - lo) / 2)
    if lo - 0.08 <= target <= hi + 0.08:
        return IN_RANGE_FLOOR
    out = max(lo - 0.08 - target, target - hi - 0.08) / half
    return max(FACTOR_FLOOR, IN_RANGE_FLOOR * math.exp(-DECAY_K * out))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--static", required=True)
    ap.add_argument("--db", required=True)
    ap.add_argument("--in", dest="src", required=True)
    ap.add_argument("--rules", default="internal/gameserver/fishing_bait_rules.txt")
    ap.add_argument("--out", default="")
    ap.add_argument("--report", required=True)
    args = ap.parse_args()

    static = sqlite3.connect(f"file:{args.static}?mode=ro", uri=True)
    db = sqlite3.connect(f"file:{args.db}?mode=ro", uri=True)
    profiles = json.load(open(args.src, encoding="utf-8"))
    bottom_only, jig_cond = load_rules_bottom_only(args.rules)
    lures, lure_names = load_lures(static)
    fish_names = load_fish_names(static)
    evidence = load_evidence(db)
    rf4_layer = load_rf4records_layer(db, lures)

    report = ["# 鱼种水层重生成报告（v3 -> v4）", ""]
    unchanged = renamed = contradictions = 0
    rows_out = []
    for p in profiles:
        key = p["fish_key"]
        old = (p["layer_min"], p["layer_pref"], p["layer_max"])
        bait_rows = evidence.get(key, {})
        # 食性组先验
        try:
            diet = json.loads(p.get("diet_json") or "{}")
        except ValueError:
            diet = {}
        group, plo, phi, ppref = diet_group(diet)
        # 规则文本硬下界
        zh = fish_names.get(key, "")
        rkey = re.sub(r"[\s()（）\ufeff]", "", zh).lower()
        hard_bottom = rkey in bottom_only or rkey in jig_cond
        if hard_bottom:
            plo = max(plo, 0.50)
            ppref = max(ppref, 0.70)
        # 拟饵证据投点
        pts = []
        used = skipped_unknown = 0
        for bait_id, w in bait_rows.items():
            t = lures.get(bait_id)
            if t is None:
                skipped_unknown += 1
                continue
            depth = bait_depth(bait_id, lure_names.get(bait_id, ""), t)
            if depth is None:
                skipped_unknown += 1
                continue
            pts.append((depth, w))
            used += 1
        total = sum(w for _, w in pts)
        source = "evidence"
        if used < MIN_EVIDENCE_ROWS or total < MIN_EVIDENCE_WEIGHT:
            pts = [(plo, total * PRIOR_POINT_SHARE), (ppref, total * PRIOR_POINT_SHARE),
                   (phi, total * PRIOR_POINT_SHARE)]
            source = "prior" if used else "prior-only"
        else:
            for layer, w in ((plo, 0.12 * total), (ppref, 0.12 * total), (phi, 0.12 * total)):
                pts.append((layer, w))
        pts.sort()
        lo = wquantile(pts, Q_LOW)
        pref = wquantile(pts, Q_MID)
        hi = wquantile(pts, Q_HIGH)
        if None in (lo, pref, hi):
            lo, pref, hi = plo, ppref, phi
            source = "fallback"
        # 动作锚定：v3 的动作权重与玩家证据独立（生成自鱼名+本地规则）。
        # 底部动作组显著强于水面组（bot/surf > 1.15）的鱼是底栖取向
        # （江鳕/鲶/鲟/鳕等"底栖掠食"），companion 证据里中层拟饵占比高只是
        # 钓法分布，pref 直接采用 v3 全值：v3 pref 是"这鱼住多深"的可靠知识
        # （鳕 0.90 贴底），证据分位数只负责收窄区间。pref 不能打折——
        # layer_pref 还喂 marineTraits（海图鱼群 ambush/pit）与 spotBiteWeight，
        # 拉浅会连带拉垮海图铁板打法（lureLayer≈0.90）的狩猎 E 因子
        # （曾用 0.75/0.9 折扣，鳕鱼 env 1.16→0.67，TestMarineHunt 回归）。
        try:
            actions = json.loads(p.get("action_json") or "{}")
        except ValueError:
            actions = {}
        surf = sum(actions.get(a, 0) for a in SURFACE_ACTIONS) / len(SURFACE_ACTIONS)
        bot = sum(actions.get(a, 0) for a in BOTTOM_ACTIONS) / len(BOTTOM_ACTIONS)
        ratio = bot / surf if surf > 0 else 0.0
        anchored = False
        if ratio > 1.15:
            anchor = old[1]
            if pref < anchor:
                shift = anchor - pref
                lo, hi = lo + shift, hi + shift
                if hi > 1.0:
                    over = hi - 1.0
                    lo = max(0.0, lo - over)
                    hi = 1.0
                pref = anchor
                anchored = True
        # rf4records 二源下探：底栖锚定组"住得深"但也会到中层吃拟饵——
        # companion（长期累计）与 rf4records（官方周纪录窗口）两个独立源的
        # 拟饵水层中心逐鱼一致（江鳕 0.40/0.38、六须鲇 0.57/0.57、绵鳚 0.60/0.61），
        # 区间下沿下探到证据中心，中层拟饵通道不再被锚死在区间外；
        # pref（跳底最佳档）与水面饵压制（中心距表层仍 ≥0.22）不受影响。
        r4 = rf4_layer.get(key)
        rf4_probe = False
        if anchored and r4 and r4[1] >= 200:
            # 下探不低于 0.35：中心更浅的（浅水底栖）保留对表层的最后压制，
            # 只放行"底鱼到中层"这条有双源证据的通道。
            target_lo = max(r4[0], 0.35)
            if target_lo < lo:
                lo = target_lo
                rf4_probe = True
        contra = (ratio <= 0.85 and pref > 0.60) or (ratio >= 1.15 and not anchored and pref < 0.40)
        contradictions += contra
        lo, hi = clamp_width(lo, pref, hi, max_w=0.65 if rf4_probe else MAX_WIDTH)
        # 锚定组有意让 pref 贴近区间下沿以上的深处（底栖），豁免 25% 贴边钳制；
        # 非锚定组保持 pref 不贴边，避免分位数边缘值充当中心。
        if anchored:
            pref = clamp01(min(max(pref, lo + 0.10 * (hi - lo)), hi - 0.05 * (hi - lo)))
        else:
            pref = clamp01(min(max(pref, lo + 0.25 * (hi - lo)), hi - 0.25 * (hi - lo)))

        changed = abs(lo - old[0]) > 1e-9 or abs(pref - old[1]) > 1e-9 or abs(hi - old[2]) > 1e-9
        renamed += changed
        unchanged += not changed
        rows_out.append({
            "fish_key": key, "name": zh, "group": group, "source": source,
            "evidence_rows": used, "evidence_weight": round(total), "skipped_unknown": skipped_unknown,
            "v3": old, "v4": (round(lo, 4), round(pref, 4), round(hi, 4)),
            "hard_bottom": hard_bottom, "contradiction": contra, "anchored": anchored,
            "rf4_probe": rf4_probe,
        })
        new = (round(lo, 4), round(pref, 4), round(hi, 4))
        p["layer_min"], p["layer_pref"], p["layer_max"] = new
        p["profile_version"] = 4

    # 分辨力指标：三档代表泳层 × 三组鱼
    groups = collections.defaultdict(list)
    for r in rows_out:
        groups[r["group"]].append(r)
    report.append("## 分辨力指标（简化因子：区间内 0.75，区间外 0.75·exp(-1.5·出界)）")
    report.append("")
    report.append("| 组 | 鱼数 | 指标 | v3 区间 | v4 区间 |")
    report.append("|---|---|---|---|---|")
    targets = [("表层饵 t=0.08", 0.08), ("中层饵 t=0.45", 0.45), ("跳底饵 t=0.90", 0.90)]
    for gname, rows in sorted(groups.items()):
        for tname, t in targets:
            f3 = [layer_factor(t, *r["v3"]) for r in rows]
            f4 = [layer_factor(t, *r["v4"]) for r in rows]
            fmt = lambda fs: f"min {min(fs):.2f} / 中位 {sorted(fs)[len(fs)//2]:.2f} / max {max(fs):.2f}"
            report.append(f"| {gname} | {len(rows)} | {tname} | {fmt(f3)} | {fmt(f4)} |")
    report.append("")

    # 关键断言。掠食组混有上层掠食（狗鱼/黑鲈/鲑科）与底栖掠食（鲟/鲶/杜父鱼），
    # 断言分开：上层掠食必须吃得到水面饵（>=0.5），底栖掠食必须被水面饵压制（<=0.1）。
    bottom_rows = [r for r in rows_out if r["group"] == "bottom"]
    pred_rows = [r for r in rows_out if r["group"] == "predator"]
    pred_upper = [r for r in pred_rows if r["v4"][0] <= 0.22 and r["v4"][1] <= 0.45]
    pred_deep = [r for r in pred_rows if r["v4"][0] >= 0.45]
    a3 = max(layer_factor(0.08, *r["v3"]) for r in bottom_rows)
    a4 = max(layer_factor(0.08, *r["v4"]) for r in bottom_rows)
    b3 = min(layer_factor(0.08, *r["v3"]) for r in pred_upper) if pred_upper else float("nan")
    b4 = min(layer_factor(0.08, *r["v4"]) for r in pred_upper) if pred_upper else float("nan")
    c4 = max(layer_factor(0.08, *r["v4"]) for r in pred_deep) if pred_deep else float("nan")
    report.append("## 关键断言")
    report.append("")
    report.append(f"- 水面饵打底栖鱼的最大因子：v3 {a3:.3f} -> v4 {a4:.3f}"
                  f"（数据目标 <=0.30；收紧到 <=0.15 由 PR-2 衰减常数负责）")
    report.append(f"- 水面饵打上层掠食（lo<=0.30，{len(pred_upper)} 条）的最小因子：v3 {b3:.3f} -> v4 {b4:.3f}（目标 >=0.5）")
    report.append(f"- 水面饵打底栖掠食（lo>=0.45，{len(pred_deep)} 条）的最大因子：v4 {c4:.3f}（目标 <=0.1）")
    report.append("")

    # 对照表
    report.append("## 逐鱼对照（changed only）")
    report.append("")
    report.append("| 鱼种 | 组 | 来源 | 证据 | v3 (min/pref/max) | v4 | 校验 |")
    report.append("|---|---|---|---|---|---|---|")
    for r in sorted(rows_out, key=lambda r: r["v4"][1]):
        if r["v3"] == r["v4"]:
            continue
        marks = []
        if r["hard_bottom"]:
            marks.append("规则硬下界")
        if r["anchored"]:
            marks.append("动作锚定")
        if r.get("rf4_probe"):
            marks.append("rf4下探")
        if r["contradiction"]:
            marks.append("动作方向矛盾")
        if r["skipped_unknown"]:
            marks.append(f"未知饵{r['skipped_unknown']}行")
        report.append(
            f"| {r['name'] or r['fish_key']} | {r['group']} | {r['source']} | {r['evidence_weight']} | "
            f"{r['v3'][0]:.2f}/{r['v3'][1]:.2f}/{r['v3'][2]:.2f} | "
            f"{r['v4'][0]:.2f}/{r['v4'][1]:.2f}/{r['v4'][2]:.2f} | {';'.join(marks) or ''} |"
        )
    report.append("")
    report.append(f"## 汇总")
    report.append("")
    report.append(f"- 共 {len(rows_out)} 鱼；区间变更 {renamed}，未变 {unchanged}；动作方向矛盾 {contradictions} 条（只标记）")
    report.append(f"- 来源分布：{dict(collections.Counter(r['source'] for r in rows_out))}")

    with open(args.report, "w", encoding="utf-8") as f:
        f.write("\n".join(report))
    print("\n".join(report[-6:]))
    print(f"\n报告 -> {args.report}")

    if args.out:
        with open(args.out, "w", encoding="utf-8") as f:
            json.dump(profiles, f, ensure_ascii=False, indent=2)
            f.write("\n")
        print(f"v4 JSON -> {args.out}")


if __name__ == "__main__":
    sys.exit(main())
