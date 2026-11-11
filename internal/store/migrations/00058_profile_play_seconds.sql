-- 玩家累计在线秒数。
--
-- 由 gameserver 在会话期间每约 30 秒累加一次、关闭会话时再补最后一笔，
-- 用于玩家简介「N 天 游戏中」——客户端按 f1.f0 / 1440 显示天数，
-- 因此这里存的是「真实在线时长」，不是账号注册时长。
--
-- 老玩家从 0 开始累计：历史在线时长没有留痕，无法反推。
ALTER TABLE profile ADD COLUMN play_seconds INTEGER NOT NULL DEFAULT 0;
