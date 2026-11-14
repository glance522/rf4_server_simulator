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


def load_bait_map(static):
    """item_catalog.name（英文原名）规范化 -> item_id。"""
    conn = sqlite3.connect(f"file:{static}?mode=ro", uri=True)
    m = {}
    for item_id, name in conn.execute(
        "SELECT item_id, name FROM item_catalog WHERE name IS NOT NULL AND name<>''"
    ):
        m.setdefault(norm_bait(name), item_id)
    conn.close()
    return m


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
    args = ap.parse_args()

    fish_map = load_fish_map()
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
