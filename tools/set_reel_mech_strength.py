# -*- coding: utf-8 -*-
"""给渔轮定义写入官方机构强度（锁轮张力上限）。

官方客户端与抓包报文都不携带机构强度（见 specs/changes/reel-lock-official-strength），
数值逐轮来自社区官方数据站（RF4H en.rf4h.ru / RF4DB rf4db.com，两者数字一致），
存入 item_def.attrs_json['kngaaefkpka']——官方通用"拉力/强度"字段（f32 位型 u32，
与鱼竿/钓线的 MaxLoad 同键同语义）。未写入的轮子由服务端回退到旧摩擦片口径。

用法：python tools/set_reel_mech_strength.py [--db server_data/static.sqlite3]
幂等：重复执行结果一致。只改列出的 item_id，其余行不动。
"""
import argparse
import json
import sqlite3
import struct
import sys

# Turion SW 系列（RF4H: en.rf4h.ru/reels/turion_sw；7YR 来自 RF4DB/Steam 攻略）。
REEL_STRENGTH_KG = {
    "spin_5271": 110.68,  # Turion SW 8000
    "spin_5272": 114.29,  # Turion SW 10000
    "spin_5273": 120.19,  # Turion SW 20000
    "spin_5274": 127.94,  # Turion SW 30000
    "spin_6008": 128.50,  # Turion SW 30000 7-Years
}

STRENGTH_KEY = "kngaaefkpka"


def f32_bits(value: float) -> int:
    return struct.unpack("<I", struct.pack("<f", value))[0]


def bits_f32(raw) -> float:
    if not isinstance(raw, int) or raw <= 0:
        return 0.0
    return struct.unpack("<f", struct.pack("<I", raw & 0xFFFFFFFF))[0]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", default="server_data/static.sqlite3")
    args = parser.parse_args()

    db = sqlite3.connect(args.db)
    changed = 0
    for item_id, kg in REEL_STRENGTH_KG.items():
        row = db.execute(
            "select attrs_json from item_def where item_id=?", (item_id,)
        ).fetchone()
        if row is None:
            print(f"[跳过] {item_id}: item_def 无此行")
            continue
        attrs = json.loads(row[0] or "{}")
        old = bits_f32(attrs.get(STRENGTH_KEY))
        new = f32_bits(kg)
        if attrs.get(STRENGTH_KEY) == new:
            print(f"[一致] {item_id}: {old:.2f}kg（已写入）")
            continue
        attrs[STRENGTH_KEY] = new
        db.execute(
            "update item_def set attrs_json=? where item_id=?",
            (json.dumps(attrs, ensure_ascii=False), item_id),
        )
        changed += 1
        print(f"[写入] {item_id}: {old:.2f}kg -> {kg:.2f}kg")
    db.commit()

    print(f"共写入 {changed} 条")
    # 复核：渔轮类目下带机构强度的定义数
    n = 0
    for (aj,) in db.execute("select attrs_json from item_def where category=6"):
        attrs = json.loads(aj or "{}")
        if STRENGTH_KEY in attrs:
            n += 1
    print(f"复核: category=6 且带 {STRENGTH_KEY} 的定义 {n} 条")
    db.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
