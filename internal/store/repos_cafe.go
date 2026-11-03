package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrCafeOrderInvalid      = errors.New("咖啡厅订单参数无效")
	ErrCafeOrderNotFound     = errors.New("咖啡厅订单不存在")
	ErrCafeOrderExpired      = errors.New("咖啡厅订单已过期")
	ErrCafeOrderFishMismatch = errors.New("交付的鱼不符合咖啡厅订单")
	ErrCafeOrderFishLocked   = errors.New("交付的鱼已被其他挂单占用")
)

type CafeOrderTemplate struct {
	Species        string
	MinWeightG     int64
	Quantity       int64
	RewardCents    int64
	OfferSize      int64
	PeriodSeconds  int64
	PhaseSeconds   int64
	PricingVersion int64
}

type CafeOrder struct {
	ID          string
	Scene       string
	Level       string
	Species     string
	MinWeightG  int64
	Quantity    int64
	RewardCents int64
	ExpiresAt   int64
	Completed   bool
	OfferSize   int64
}

type CafeOrderSubmitResult struct {
	RewardCents  int64
	RemovedGUIDs []string
	Replayed     bool
}

// GetCafeOrders 按各订单的现实时间窗口保存需求，刷新和重登不重置倒计时。
func (d *DB) GetCafeOrders(ctx context.Context, uid int64, scene, level string, now, periodSeconds int64, templates []CafeOrderTemplate) ([]*CafeOrder, error) {
	scene, level = strings.TrimSpace(scene), strings.TrimSpace(level)
	if uid <= 0 || scene == "" || level == "" || now < 0 || periodSeconds <= 0 || now > math.MaxInt64-periodSeconds {
		return nil, ErrCafeOrderInvalid
	}
	if len(templates) == 0 || len(templates) > 100 {
		return nil, ErrCafeOrderInvalid
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	orders := make([]*CafeOrder, 0, len(templates))
	for ordinal, template := range templates {
		if template.PricingVersion == 0 {
			template.PricingVersion = 1
		}
		period := template.PeriodSeconds
		if period == 0 {
			period = periodSeconds
		}
		phase := template.PhaseSeconds
		if template.OfferSize == 0 {
			template.OfferSize = 1
		}
		if strings.TrimSpace(template.Species) == "" || template.MinWeightG <= 0 || template.Quantity <= 0 || template.Quantity > 1000 || template.RewardCents <= 0 || template.OfferSize < 1 || template.OfferSize > 4 || period <= 0 || period > math.MaxInt64-now || phase < 0 || phase >= period || template.PricingVersion < 1 {
			return nil, ErrCafeOrderInvalid
		}
		start, expires := int64(0), phase
		if now >= phase {
			start = phase + (now-phase)/period*period
			expires = start + period
		}
		order := &CafeOrder{}
		err := tx.QueryRowContext(ctx, `SELECT o.order_id,o.scene,o.level_key,o.species,o.min_weight_g,o.quantity,o.reward_cents,o.expires_at,o.offer_size,
            EXISTS(SELECT 1 FROM cafe_order_completion c WHERE c.uid=? AND c.order_id=o.order_id)
            FROM cafe_order o WHERE o.scene=? AND o.batch_start=? AND o.period_seconds=? AND o.ordinal=? AND o.pricing_version=?`, uid, scene, start, period, ordinal, template.PricingVersion).
			Scan(&order.ID, &order.Scene, &order.Level, &order.Species, &order.MinWeightG, &order.Quantity, &order.RewardCents, &order.ExpiresAt, &order.OfferSize, &order.Completed)
		if errors.Is(err, sql.ErrNoRows) {
			id, err := newMarketOfferID()
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO cafe_order(order_id,scene,level_key,batch_start,period_seconds,ordinal,species,min_weight_g,quantity,reward_cents,expires_at,offer_size,pricing_version)
                VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, scene, level, start, period, ordinal, template.Species, template.MinWeightG, template.Quantity, template.RewardCents, expires, template.OfferSize, template.PricingVersion); err != nil {
				return nil, err
			}
			order = &CafeOrder{ID: id, Scene: scene, Level: level, Species: template.Species, MinWeightG: template.MinWeightG, Quantity: template.Quantity, RewardCents: template.RewardCents, ExpiresAt: expires, OfferSize: template.OfferSize}
		} else if err != nil {
			return nil, err
		}
		if order.Level != level {
			return nil, ErrCafeOrderInvalid
		}
		orders = append(orders, order)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return orders, nil
}

// SubmitCafeOrder 原子扣鱼、支付银币并保存交付回执；同一玩家同单只支付一次。
func (d *DB) SubmitCafeOrder(ctx context.Context, uid int64, scene, orderID string, fishGUIDs []string, now int64) (*CafeOrderSubmitResult, error) {
	scene, orderID = strings.TrimSpace(scene), normalizeMarketOfferGUID(orderID)
	if uid <= 0 || scene == "" || orderID == "" || now < 0 {
		return nil, ErrCafeOrderInvalid
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order := &CafeOrder{}
	err = tx.QueryRowContext(ctx, `SELECT order_id,scene,level_key,species,min_weight_g,quantity,reward_cents,expires_at FROM cafe_order WHERE order_id=? AND scene=?`, orderID, scene).
		Scan(&order.ID, &order.Scene, &order.Level, &order.Species, &order.MinWeightG, &order.Quantity, &order.RewardCents, &order.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCafeOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	var reward int64
	err = tx.QueryRowContext(ctx, `SELECT reward_cents FROM cafe_order_completion WHERE uid=? AND order_id=?`, uid, order.ID).Scan(&reward)
	if err == nil {
		return &CafeOrderSubmitResult{RewardCents: reward, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if order.ExpiresAt <= now {
		return nil, ErrCafeOrderExpired
	}
	if int64(len(fishGUIDs)) != order.Quantity {
		return nil, ErrCafeOrderFishMismatch
	}
	seen := make(map[string]bool, len(fishGUIDs))
	removed := make([]string, 0, len(fishGUIDs))
	for _, raw := range fishGUIDs {
		guid := normalizeMarketOfferGUID(raw)
		if guid == "" || seen[guid] {
			return nil, ErrCafeOrderFishMismatch
		}
		seen[guid] = true
		var owner, weight int64
		var species, level, loc string
		err := tx.QueryRowContext(ctx, `SELECT uid,species,weight_g,COALESCE(keep_level,''),COALESCE(keep_loc,'') FROM keepnet_items WHERE echo_guid=?`, guid).Scan(&owner, &species, &weight, &level, &loc)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCafeOrderFishMismatch
		}
		if err != nil {
			return nil, err
		}
		if level == "" {
			level, err = cafeFishLevelTx(ctx, tx, loc)
			if err != nil {
				return nil, err
			}
		}
		if owner != uid || species != order.Species || weight < order.MinWeightG || level != order.Level {
			return nil, ErrCafeOrderFishMismatch
		}
		var locked bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM market_offer WHERE fish_guid=? AND status='open')`, guid).Scan(&locked); err != nil {
			return nil, err
		}
		if locked {
			return nil, ErrCafeOrderFishLocked
		}
		removed = append(removed, guid)
	}
	var silver int64
	if err := tx.QueryRowContext(ctx, `SELECT silver FROM profile WHERE uid=?`, uid).Scan(&silver); err != nil {
		return nil, err
	}
	if silver > math.MaxInt64-order.RewardCents {
		return nil, ErrCafeOrderInvalid
	}
	for _, guid := range removed {
		deleted, err := d.DeleteKeepnetTx(ctx, tx, uid, guid)
		if err != nil {
			return nil, err
		}
		if !deleted {
			return nil, ErrCafeOrderFishMismatch
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE profile SET silver=silver+?,updated_ts=? WHERE uid=?`, order.RewardCents, float64(now), uid); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO txn(uid,ts,kind,guid,silver_delta,gold_delta,note) VALUES(?,?,'cafe_order',?,?,0,?)`, uid, float64(now), order.ID, order.RewardCents, fmt.Sprintf("cafe %s", scene)); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(removed)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cafe_order_completion(uid,order_id,reward_cents,fish_guids_json,completed_at) VALUES(?,?,?,?,?)`, uid, order.ID, order.RewardCents, string(raw), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &CafeOrderSubmitResult{RewardCents: order.RewardCents, RemovedGUIDs: removed}, nil
}

func cafeFishLevelTx(ctx context.Context, tx *sql.Tx, loc string) (string, error) {
	loc = strings.TrimSpace(loc)
	if strings.HasPrefix(loc, "level_") {
		return loc, nil
	}
	prefix := sublocPrefix(loc)
	if prefix == "" {
		return "", nil
	}
	var level string
	err := tx.QueryRowContext(ctx, `SELECT alias FROM loc_alias WHERE alias LIKE ? ORDER BY alias LIMIT 1`, prefix+"%").Scan(&level)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return level, err
}
