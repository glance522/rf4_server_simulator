-- 玩家饱食度：使用食物/饮料/酒水时增加，达到上限后拒绝继续进食。
-- 0~100，默认 0；饱食度会随时间回落（由 gameserver 在进食与档案下发时折算）。
ALTER TABLE profile ADD COLUMN satiety REAL NOT NULL DEFAULT 0;
