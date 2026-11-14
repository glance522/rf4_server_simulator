-- 海图区号集合：一个区域可覆盖多个声呐区号（图上框选范围自动转集合）。
-- sea_zones_json 存 [N,...]；空串=旧单区号语义（回退读 sea_zone 列）。
ALTER TABLE fish_zone ADD COLUMN sea_zones_json TEXT NOT NULL DEFAULT '';
