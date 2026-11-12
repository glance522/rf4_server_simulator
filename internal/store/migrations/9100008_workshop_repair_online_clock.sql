-- 工坊修理改按"在线时长"计时：下线冻结。
--
-- 官服表现（玩家 2026-11-11 口述）：物件进工坊后带一把锁和倒计时，**只有在线时
-- 倒计时才走**，下线就冻住。原来的实现只有一个绝对到期时间 due_ts，离线期间照走，
-- 与官服相反。
--
-- 现在拆成两列：
--   remaining_s  还欠多少秒工时，只有在线期间被扣减；
--   tick_ts      上一次扣减的时刻，用来算"这一拍该扣多少"；
--                登录时重置成 now，把离线那段时间排除掉。
-- due_ts 保留，作为"按当前 remaining_s 推算的到期时刻"给人看和建索引，不再参与判定。
--
-- 存量行没有剩余秒数可查，只能按原 due_ts 折算，等价于"从这一刻开始冻结"。
-- 再按新上限（满磨损 6 小时在线，见 workshop_silver_repair_hours）封顶：
-- 老行是按"24 小时绝对时间"建的，不封顶的话切到在线计时会变成"在线 24 小时"，
-- 比旧口径更难验。封顶后最坏等于新规则，玩家不会觉得被加价。
ALTER TABLE workshop_repair_job ADD COLUMN remaining_s REAL NOT NULL DEFAULT 0;
ALTER TABLE workshop_repair_job ADD COLUMN tick_ts REAL NOT NULL DEFAULT 0;
UPDATE workshop_repair_job
   SET remaining_s = MIN(21600, MAX(0, due_ts - CAST(strftime('%s','now') AS REAL))),
       tick_ts = CAST(strftime('%s','now') AS REAL);
UPDATE workshop_repair_job SET due_ts = tick_ts + remaining_s;
