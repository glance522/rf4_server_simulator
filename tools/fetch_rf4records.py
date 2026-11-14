#!/usr/bin/env python3
"""抓取 rf4records.com 官方周纪录捕获数据，入库 rf4records_catch 表。

数据源：https://rf4records.com/api/records/filtered?fish=<英文名>（官方周纪录聚合，
免费公开站；robots.txt 通用 UA Allow: / 且 Content-Signal use=reference，允许参考使用；
禁抓的只有 AI 训练爬虫。仍礼貌限速，仅少量请求，UA 自报身份）。

每条纪录 = 玩家用某饵钓到某鱼（重量/水域/钓法/日期），正是"周纪录饵效"口径——
与 companion_bait_fish（预置快照，provenance 缺失）同源互补，为食性判定提供
可溯源的外部证据。首次运行抓 252 种鱼全量（分批、约 15 分钟）；之后重跑按
PRIMARY KEY 去重，只增量入库新纪录。

用法：
  python -I tools/fetch_rf4records.py --db data/rf4server.db \
      --static server_data/static.sqlite3 --batch 40 --sleep 2
  # 试跑（只抓 3 条鱼）：--limit 3
"""

import argparse
import json
import sqlite3
import sys
import time
import re
import urllib.request

sys.stdout.reconfigure(encoding="utf-8")

API = "https://rf4records.com/api/records/filtered?fish="
UA = "rf4s-simulator-evidence/0.1 (local single-player research; robots complied)"

MAP_PATH = "tools/rf4records_fish_map.py"


def load_fish_map():
    """鱼名映射表与脚本同目录，tools/ 白名单只进 .py。"""
    sys.path.insert(0, "tools")
    from rf4records_fish_map import FISH_MAP
    return {k: v for k, v in FISH_MAP.items() if v}


def norm_bait(s):
    return " ".join(s.split()).lower()


# 英文口味/形态词 -> item_catalog.name_zh 的中文机翻词。
# 组合饵（"Blackcurrant 15 Pop-Up; Blackcurrant Flavor Purple"）的组件 1 按词表
# 翻译+数字提取，与中文名做字符集匹配（机翻词序不稳定，用无序比对）。
WORD_ZH = {
    "Blackcurrant": "黑加仑", "Salmon": "鲑鱼", "Strawberry": "大草莓", "Vanilla": "香草",
    "Honey": "蜂蜜", "Garlic": "大蒜", "Crab & Mussels": "螃蟹和贻贝", "Crab & Fish": "螃蟹和鱼",
    "Crab": "螃蟹", "Mussels": "贻贝", "Mussel": "贻贝", "Cranberry": "蔓越莓",
    "Fresh Cranberry": "新鲜蔓越莓", "Spice Mix": "七种香料", "Sweet Cream": "甜奶油",
    "Cream": "奶油", "Caramel": "焦糖", "Cocos": "椰子", "Coconut": "椰子",
    "Squid": "鱿鱼", "Octopus": "章鱼", "Krill": "磷虾", "Pepper": "胡椒",
    "Tuna": "吞拿鱼", "Halibut": "庸鲽", "Pineapple": "菠萝", "Tropical": "热带的",
    "Fruit": "水果", "Fish": "鱼", "Herbs": "香草", "Plum": "李子", "Peach": "桃",
    "Hemp": "麻", "Scallop": "扇贝", "Shrimp": "虾", "Mint": "薄荷", "Bee": "蜜蜂",
    "Pop-Up": "PopUp", "Pop Up": "PopUp", "Pop-Ups": "PopUp", "Soluble": "可溶", "Pellets": "",
    "Flavor": "", "Scent": "", "Worm": "蚯蚓", "Bloodworm": "红虫", "Maggot": "蛆虫",
    "Tutti Frutti": "什锦水果", "Garlic & Mussels": "大蒜似的贻贝", "Banana": "甜香蕉", "Foam-rubber fish": "泡沫橡胶鱼",
}
# 长词优先替换，避免 "Mussels" 被 "Mussel" 截断
WORD_ORDER = sorted(WORD_ZH, key=len, reverse=True)


def squash(s):
    """去空白/全角空格/连字符，供无序字符集比对。"""
    return re.sub(r"popups", "popup", re.sub(r"[\s \-]+", "", s)).lower()


def translate_component(part):
    """英文组件 -> 中文候选串（词表替换，数字保留）。"""
    s = part
    for en in WORD_ORDER:
        s = re.sub(re.escape(en), lambda mm: "" + WORD_ZH[en] + "" if WORD_ZH[en] else "", s, flags=re.I)
    # 提取翻译词与数字
    words = [w.strip() for w in s.split("") if w.strip() and not w.strip().isdigit()]
    nums = re.findall(r"\d+", part)
    return words, nums


def load_zh_index(static):
    """item_catalog.name_zh 规范化 -> item_id（组合饵翻译匹配用）。"""
    conn = sqlite3.connect(f"file:{static}?mode=ro", uri=True)
    idx = {}
    for item_id, name_zh in conn.execute(
        "SELECT c.item_id, c.name_zh FROM item_def d LEFT JOIN item_catalog c ON c.item_id=d.item_id"
        " WHERE d.category IN (17,27) AND c.name_zh<>''"
    ):
        idx.setdefault(squash(name_zh), item_id)
    conn.close()
    return idx


def match_by_translate(bait_display, zh_idx):
    """组合饵翻译匹配：拆组件，跳过 Flavor 味剂，词表翻译后与中文名做
    字符集（无序）匹配——机翻词序与英文词序不一致仍可命中。"""
    for part in bait_display.split(";"):
        part = part.strip()
        if not part or "Flavor" in part:
            continue  # 味剂不是独立商品
        words, nums = translate_component(part)
        if not words or not nums:
            continue
        want = set(squash("".join(words) + "".join(nums)))
        for zh_norm, item_id in zh_idx.items():
            if set(zh_norm) == want and abs(len(zh_norm) - len(squash("".join(words) + "".join(nums)))) <= 2:
                return item_id
    return ""


def load_bait_map(static):
    """合并两源：item_catalog.name（拟饵英文原名）+ 手工补充表（天然饵/活饵）。
    组合饵（分号连接）在 reparse 时逐成分映射，此处只建单名索引。"""
    conn = sqlite3.connect(f"file:{static}?mode=ro", uri=True)
    m = {}
    for item_id, name in conn.execute(
        "SELECT item_id, name FROM item_catalog WHERE name IS NOT NULL AND name<>''"
    ):
        m.setdefault(norm_bait(name), item_id)
    conn.close()
    sys.path.insert(0, "tools")
    from rf4records_bait_map import BAIT_MAP
    for en, item_id in BAIT_MAP.items():
        m.setdefault(norm_bait(en), item_id)
    return m


def bait_id_for(bait_display, bait_map, zh_idx=None):
    """按分号拆分组合饵，逐成分映射；组合 Pop-Up 走词表翻译匹配；全不中留空。"""
    for part in bait_display.split(";"):
        hit = bait_map.get(norm_bait(part))
        if hit:
            return hit
    if zh_idx:
        return match_by_translate(bait_display, zh_idx)
    return ""


def ensure_table(db):
    """复用迁移文件里的 CREATE TABLE（去注释），脚本与迁移保持同一 schema。"""
    import re
    sql = open("internal/store/migrations/00060_rf4records_catch.sql", encoding="utf-8").read()
    ddl = re.search(r"CREATE TABLE IF NOT EXISTS.*?\);", sql, re.S)
    if not ddl:
        raise SystemExit("00060 迁移里找不到 CREATE TABLE")
    db.execute(ddl.group(0))
    db.commit()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", default="data/rf4server.db")
    ap.add_argument("--static", default="server_data/static.sqlite3")
    ap.add_argument("--map", default=MAP_PATH, help="鱼名映射模块（tools/*.py）")
    ap.add_argument("--batch", type=int, default=40, help="每批鱼数，批间提示进度")
    ap.add_argument("--sleep", type=float, default=2.0, help="请求间隔秒")
    ap.add_argument("--limit", type=int, default=0, help="只抓前 N 条鱼（试跑）")
    ap.add_argument("--fish", default="", help="只抓指定英文名（逗号分隔），补抓失败鱼用")
    ap.add_argument("--reparse", action="store_true", help="不抓网，只按最新映射刷新库内 fish_key")
    args = ap.parse_args()

    fish_map = load_fish_map()
    zh_idx = load_zh_index(args.static)
    fv = json.load(urllib.request.urlopen(
        urllib.request.Request("https://rf4records.com/api/records/filter-values",
                               headers={"User-Agent": UA}), timeout=60))
    fishes = fv["fish"]
    if args.limit:
        fishes = fishes[:args.limit]
    if args.fish:
        wanted = [s.strip() for s in args.fish.split(",") if s.strip()]
        fishes = [f for f in fishes if f in wanted]
    bait_map = load_bait_map(args.static)

    db = sqlite3.connect(args.db)
    ensure_table(db)

    now = time.time()
    if args.reparse:
        total = 0
        for en, fk in fish_map.items():
            cur = db.execute("UPDATE rf4records_catch SET fish_key=? WHERE fish_name_en=? AND fish_key<>?", (fk, en, fk))
            total += cur.rowcount
        hit = miss = 0
        rows = db.execute("SELECT rowid, bait_name FROM rf4records_catch WHERE bait_id=''").fetchall()
        for rid, bait in rows:
            bid = bait_id_for(bait, bait_map, zh_idx)
            if bid:
                db.execute("UPDATE rf4records_catch SET bait_id=? WHERE rowid=?", (bid, rid))
                hit += 1
            else:
                miss += 1
        db.commit()
        db.close()
        print(f"reparse 完成：fish_key 刷新 {total} 行；bait_id 补映射命中 {hit}，仍缺 {miss}")
        return
    total_new = total_seen = total_bait_hit = total_bait_rows = 0
    for i, fish in enumerate(fishes, 1):
        url = API + urllib.parse.quote(fish)
        try:
            req = urllib.request.Request(url, headers={"User-Agent": UA})
            data = json.load(urllib.request.urlopen(req, timeout=120))
        except Exception as e:
            print(f"[{i}/{len(fishes)}] {fish}: 抓取失败 {e}")
            time.sleep(args.sleep)
            continue
        recs = data.get("records") or []
        new = seen = bh = br = 0
        for r in recs:
            bait = (r.get("bait_display") or "").strip()
            if not bait:
                continue
            br += 1
            bid = bait_map.get(norm_bait(bait), "")
            if bid:
                bh += 1
            cur = db.execute(
                "INSERT OR IGNORE INTO rf4records_catch(fish_name_en, fish_key, bait_name,"
                " bait_id, weight_g, waterbody, region, categories, trophy_class,"
                " catch_date, fetched_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)",
                (fish, fish_map.get(fish, ""), bait, bid, r.get("weight"),
                 r.get("waterbody") or "", r.get("region") or "",
                 ";".join(r.get("categories") or []), r.get("trophy_class") or "",
                 r.get("date") or "", now))
            if cur.rowcount:
                new += 1
            else:
                seen += 1
        total_new += new
        total_seen += seen
        total_bait_hit += bh
        total_bait_rows += br
        if i % args.batch == 0 or i == len(fishes):
            print(f"[{i}/{len(fishes)}] {fish}: 纪录 {len(recs)}，新增 {new} 已存在 {seen}"
                  f" 饵名匹配 {bh}/{br}")
        time.sleep(args.sleep)
    db.commit()
    db.close()
    n = total_bait_rows or 1
    print(f"\n完成：新增 {total_new}，已存在 {total_seen}，饵名命中率 {total_bait_hit}/{total_bait_rows}"
          f" = {total_bait_hit / n:.1%}")


import urllib.parse  # noqa: E402

if __name__ == "__main__":
    main()
