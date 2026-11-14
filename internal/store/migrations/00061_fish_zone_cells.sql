-- 区域刷选：一个区域可以是任意 40m 网格组合（在地图上涂格子），不再限于矩形。
-- cells_json 存 [[gx,gz],...]；空串 = 不是 grid_cells 区域（grid_rect/sea_zone 不受影响）。
ALTER TABLE fish_zone ADD COLUMN cells_json TEXT NOT NULL DEFAULT '';
