package gameserver

// 技能学习与重置命令（5/2、5/3、5/4）。
// 三个命令成功后都返回 311 全量技能树，供客户端刷新面板。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strconv"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// fishingSkillBonus 保存钓鱼判定使用的三类技能增益。
type fishingSkillBonus struct {
	BiteSpeed      float64 // 咬钩等待倍率，<1 更快
	TrophyLuck     float64 // 额外抽一次体重取较重者的概率
	FightTolerance float64 // 默认鱼池体重上限的放宽比例
}

func neutralFishingSkillBonus() fishingSkillBonus {
	return fishingSkillBonus{BiteSpeed: 1.0}
}

// fishingSkillCatForRig 根据钓组类型返回对应技能大类；未知类型归入浮钓。
func fishingSkillCatForRig(rigType int64) int64 {
	if bottomRigTypes[rigType] {
		return 3
	}
	if rigTypeLureFamily[rigType] {
		return 2
	}
	if rigType >= 50 && rigType <= 59 {
		return 11
	}
	return 1
}

const (
	// 熟练度增长概率按 p = 0.9 * 25 / (25 + 当前熟练度) 计算。
	// 熟练度越高，命中增长的概率越低；100% 时直接返回 0。
	fishingProficiencyChanceScale = 25.0
	fishingProficiencyMaxChance   = 0.9
)

// fishingProficiencyGainChance 返回一次钓获触发熟练度增长的概率。
func fishingProficiencyGainChance(current float64) float64 {
	if current < 0 {
		current = 0
	}
	if current >= profCap {
		return 0
	}
	return fishingProficiencyMaxChance * fishingProficiencyChanceScale /
		(fishingProficiencyChanceScale + current)
}

// fishingProficiencyLargeGainChance 返回增长命中后获得 0.2%（而非 0.1%）的概率。
// 低熟练度更容易获得较大增量，高熟练度更多获得 0.1%。
func fishingProficiencyLargeGainChance(current float64) float64 {
	if current < 0 {
		current = 0
	}
	if current > profCap {
		current = profCap
	}
	return 0.8 - 0.6*(current/profCap)
}

// fishingProficiencyGainForRoll 是可测试的纯函数。gainRoll 决定本次是否增长，
// amountRoll 决定命中后增加 0.2 还是 0.1。
func fishingProficiencyGainForRoll(current, gainRoll, amountRoll float64) float64 {
	if current >= profCap || gainRoll >= fishingProficiencyGainChance(current) {
		return 0
	}
	if amountRoll < fishingProficiencyLargeGainChance(current) {
		return 0.2
	}
	return 0.1
}

// awardFishingProficiency 按实际钓组大类增加熟练度。
func (s *Server) awardFishingProficiency(ctx context.Context, uid, rigType int64) (int64, float64) {
	cat := fishingSkillCatForRig(rigType)
	return cat, s.awardSkillProficiency(ctx, uid, cat)
}

// awardSkillProficiency 按指定技能大类增加熟练度。满熟练度不写库；
// 接近上限时只增加剩余可容纳的数值。
func (s *Server) awardSkillProficiency(ctx context.Context, uid, cat int64) float64 {
	return s.awardSkillProficiencyWithRolls(ctx, uid, cat, rand.Float64(), rand.Float64())
}

// awardSkillProficiencyWithRolls 允许测试注入随机数，实际写库仍复用同一概率公式。
func (s *Server) awardSkillProficiencyWithRolls(ctx context.Context, uid, cat int64, gainRoll, amountRoll float64) float64 {
	if s == nil || s.DB == nil {
		return 0
	}
	current, err := s.DB.GetSkillPoints(ctx, uid, cat)
	if err != nil {
		return 0
	}
	gain := fishingProficiencyGainForRoll(current, gainRoll, amountRoll)
	if gain <= 0 {
		return 0
	}
	if current+gain > profCap {
		gain = profCap - current
	}
	if gain <= 1e-9 {
		return 0
	}
	if err := s.DB.AddSkillPoints(ctx, uid, cat, gain); err != nil {
		return 0
	}
	return gain
}

// fishingBonus 按当前钓法合成永久与当前竿熟练度，不给未知节点补通用加成。
func (s *Server) fishingBonus(ctx context.Context, uid, rigType int64, gears ...string) fishingSkillBonus {
	b := neutralFishingSkillBonus()
	if s == nil || s.DB == nil {
		return b
	}
	cat := fishingSkillCatForRig(rigType)
	effects, err := s.effectiveFishingSkills(ctx, uid, gears...)
	if err != nil {
		return b
	}
	effect := effects.mastery(cat) * 0.15
	b.BiteSpeed = 1.0 - effect/10.0*0.03
	if b.BiteSpeed < 0.5 {
		b.BiteSpeed = 0.5
	}
	b.TrophyLuck = effect / 10.0 * 0.012
	b.FightTolerance = effect / 10.0 * 0.06
	return b
}

func init() {
	RegisterExtra(func(d *Dispatch) {
		d.Register(5, 2, hSkillLearn)
		d.Register(5, 3, hSkillReset)
		d.Register(5, 4, hSkillResetAll)
	})
}

// hSkillLearn 处理 5/2 技能学习或升级。
// 请求需包含大类 ID 和节点 ID；缺少参数时交给兜底链。
// 无论成功与否都返回 311 全量技能树，失败原因只写日志。
func hSkillLearn(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	ints := argsIntsDeep(ctx.Tree)
	if len(ints) < 2 {
		return nil, false // cat_id / node_id 缺失 → PASS
	}
	catID, nodeID := int64(ints[0]), int64(ints[1])
	ok, reason := srv.skillLearn(ctx.Sess.pushCtx(), ctx.Sess.Userid, catID, nodeID)
	if ok {
		ctx.Log.Info("5/2 学习 OK", "cat", catID, "node", nodeID)
	} else {
		ctx.Log.Info("5/2 学习拒绝", "cat", catID, "node", nodeID, "reason", reason)
	}
	return srv.replySkillTree(ctx)
}

// hSkillReset 处理 5/3 单节点重置并返还技能点。
// 有两个以上整数时取第二个作为节点 ID，否则取第一个。
func hSkillReset(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	ints := argsIntsDeep(ctx.Tree)
	if len(ints) > 0 {
		nodeID := int64(ints[0])
		if len(ints) >= 2 {
			nodeID = int64(ints[1])
		}
		// skills.reset_node：重置一个技能节点（返还技能点）
		if err := srv.resetSkillNodes(ctx.Sess.pushCtx(), ctx.Sess.Userid, &nodeID); err != nil {
			ctx.Log.Warn("5/3 重置写库失败", "node", nodeID, "err", err)
		} else {
			ctx.Log.Info("5/3 重置节点", "node", nodeID)
		}
	}
	return srv.replySkillTree(ctx)
}

// hSkillResetAll 处理 5/4 全部重置，清空 skill_node 后返回全量技能树。
func hSkillResetAll(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	if err := srv.resetSkillNodes(ctx.Sess.pushCtx(), ctx.Sess.Userid, nil); err != nil {
		ctx.Log.Warn("5/4 全部重置写库失败", "err", err)
	} else {
		ctx.Log.Info("5/4 已重置全部技能")
	}
	return srv.replySkillTree(ctx)
}

// replySkillTree 构造并编码 311 全量技能树；f0 是当前自然可用技能点。
func (s *Server) replySkillTree(ctx *Ctx) ([]byte, bool) {
	tree, err := s.BuildSkillTree311(ctx.Sess.pushCtx(), ctx.Sess.Userid, ^uint64(0))
	if err != nil {
		ctx.Log.Warn("5/x 技能树构造失败", "err", err)
		return nil, false // 树构造失败，交给兜底链
	}
	body, err := proto.EncodeValue(tree, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Warn("5/x 技能树编码失败", "err", err)
		return nil, false
	}
	return body, true
}

// skillBonusPoints 返回管理员通过后台追加/扣减的未分配技能点（kv 偏移量）。
func (s *Server) skillBonusPoints(ctx context.Context, uid int64) int64 {
	if s.DB == nil {
		return 0
	}
	v, ok, err := s.DB.KVGet(ctx, fmt.Sprintf("skill_bonus:%d", uid))
	if err != nil || !ok {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// skillAvailablePoints 返回等级累计技能点减去已投入点数后的余额（含管理员偏移）。
func (s *Server) skillAvailablePoints(ctx context.Context, uid int64) int64 {
	p, err := s.DB.EnsureProfile(ctx, uid)
	if err != nil {
		return 0
	}
	total := skillPointsTotal(p.Level) + s.skillBonusPoints(ctx, uid)
	spent, err := s.DB.SkillNodeLevelSum(ctx, uid)
	if err != nil {
		return 0
	}
	avail := total - spent
	if avail < 0 {
		return 0
	}
	return avail
}

// skillLearn 学习或升级一个技能节点，返回是否成功及原因。
// 校验顺序（与客户端预期一致）：
//  1. 节点存在（skill_def）
//  2. 已确认共享组共用等级，其余组沿用现有约束
//  3. 可学（max_level > 0）
//  4. 未达最大等级
//  5. 大类熟练度 ≥ 节点解锁门槛
//  6. 有可用技能点
//
// 通过则 skill_node.level += 1。
func (s *Server) skillLearn(ctx context.Context, uid, catID, nodeID int64) (bool, string) {
	def, err := s.DB.GetSkillDef(ctx, catID, nodeID)
	if err != nil || def == nil {
		return false, "unknown-node"
	}
	if def.MaxLevel <= 0 {
		return false, "not-learnable"
	}
	defs := s.skillDefsOfCat(catID)
	shared := store.SharedSkillNodeIDs(catID, nodeID)
	reason := "ok"
	denied := errors.New("skill-denied")
	effects, err := s.effectiveFishingSkills(ctx, uid)
	if err != nil {
		return false, "db-error"
	}
	err = s.writeSkillNodes(ctx, uid, func(conn *sql.Conn) error {
		levels := map[int64]int64{}
		rows, err := conn.QueryContext(ctx, `SELECT node_id,level FROM skill_node WHERE uid=?`, uid)
		if err != nil {
			return err
		}
		for rows.Next() {
			var node, rank int64
			if err := rows.Scan(&node, &rank); err != nil {
				rows.Close()
				return err
			}
			levels[node] = rank
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if def.GroupID != 0 && len(shared) == 1 {
			for _, other := range defs {
				if other.NodeID == nodeID || other.GroupID != def.GroupID {
					continue
				}
				if levels[other.NodeID] > 0 {
					reason = "group-conflict"
					return denied
				}
			}
		}
		var cur int64
		for _, id := range shared {
			cur = max(cur, levels[id])
		}
		if cur >= def.MaxLevel {
			reason = "max-level"
			return denied
		}
		var prof float64
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE((SELECT points FROM skill WHERE uid=? AND skill_id=?),0)`, uid, catID).Scan(&prof); err != nil {
			return err
		}
		unlock := skillNodeDef{UnlockPct: float32(def.UnlockPct), Enabled: def.Enabled != 0}
		if !nodeUnlocked(unlock, prof, effects.GearMastery[catID]) {
			reason = "locked"
			return denied
		}
		var level int64
		var bonusRaw string
		if err := conn.QueryRowContext(ctx, `SELECT level,
			COALESCE((SELECT v FROM kv WHERE k=?),'0') FROM profile WHERE uid=?`, fmt.Sprintf("skill_bonus:%d", uid), uid).Scan(&level, &bonusRaw); err != nil {
			return err
		}
		bonus, _ := strconv.ParseInt(bonusRaw, 10, 64)
		if skillPointsTotal(level)+bonus-store.CountSkillNodePoints(levels) <= 0 {
			reason = "no-points"
			return denied
		}
		for _, id := range shared {
			if _, err := conn.ExecContext(ctx, `INSERT INTO skill_node(uid,node_id,level) VALUES(?,?,?)
				ON CONFLICT(uid,node_id) DO UPDATE SET level=excluded.level`, uid, id, cur+1); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, denied) {
		return false, reason
	}
	if err != nil {

		s.log.Warn("5/2 节点写库失败", "uid", uid, "node", nodeID, "err", err)
		return false, "db-error"
	}
	return true, "ok"
}

// writeSkillNodes 串行检查点数与节点写入，避免并发学习透支或丢级。
func (s *Server) writeSkillNodes(ctx context.Context, uid int64, mutate func(*sql.Conn) error) error {
	if _, err := s.DB.EnsureProfile(ctx, uid); err != nil {
		return err
	}
	conn, err := s.DB.SQL.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	if err := mutate(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func (s *Server) resetSkillNodes(ctx context.Context, uid int64, nodeID *int64) error {
	return s.writeSkillNodes(ctx, uid, func(conn *sql.Conn) error {
		if nodeID == nil {
			_, err := conn.ExecContext(ctx, `DELETE FROM skill_node WHERE uid=?`, uid)
			return err
		}
		var catID int64
		err := conn.QueryRowContext(ctx, `SELECT cat_id FROM skill_def WHERE node_id=? LIMIT 1`, *nodeID).Scan(&catID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, id := range store.SharedSkillNodeIDs(catID, *nodeID) {
			if _, err := conn.ExecContext(ctx, `DELETE FROM skill_node WHERE uid=? AND node_id=?`, uid, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// argsIntsDeep 按深度优先顺序收集整数。
// 只匹配 U32、U64、DATETIME 线型；对象字段只读取 U32/U64，VAL 内嵌树不递归。
func argsIntsDeep(v *proto.Value) []uint64 {
	var out []uint64
	var walk func(x *proto.Value)
	walk = func(x *proto.Value) {
		if x == nil {
			return
		}
		switch x.Kind {
		case proto.KindU32, proto.KindU64, proto.KindDateTime:
			out = append(out, x.Num)
		case proto.KindArgs, proto.KindValList, proto.KindArray,
			proto.KindU32Arr, proto.KindGuidArr, proto.KindTypedU32Arr:
			for _, it := range x.Items {
				walk(it)
			}
		case proto.KindObj:
			for i := range x.Fields {
				switch x.Fields[i].Op {
				case "U32", "U64":
					if x.Fields[i].Num != nil {
						out = append(out, u64Of(x.Fields[i].Num))
					}
				}
			}
		}
	}
	walk(v)
	return out
}
