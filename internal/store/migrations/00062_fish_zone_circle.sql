-- 圆形钓点区：中心（世界米）+ 半径。坑、湾口窄部等小面积区域的米级精度划分，
-- 与 40m 网格（分布/查鱼口径）互不影响；grid_rect/grid_cells 不使用这三列。
ALTER TABLE fish_zone ADD COLUMN cx REAL;
ALTER TABLE fish_zone ADD COLUMN cz REAL;
ALTER TABLE fish_zone ADD COLUMN radius REAL;
