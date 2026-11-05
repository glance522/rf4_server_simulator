package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rf4server/internal/gameserver"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

type blobEntry struct {
	File      string   `json:"file"`
	SHA256    string   `json:"sha256"`
	Size      int      `json:"size"`
	Kind      int      `json:"kind"`
	IDs       []string `json:"ids"`
	Operation []int    `json:"operation"`
}
type manifest struct {
	Schema        int    `json:"schema"`
	ClientVersion string `json:"clientVersion"`
	TargetAccount string `json:"targetAccount"`
	NamesRedacted bool   `json:"namesRedacted"`
	blobEntry
	Entries []blobEntry `json:"entries"`
}
type inventoryIndex struct {
	Schema        int                        `json:"schema"`
	ClientVersion string                     `json:"clientVersion"`
	TargetAccount string                     `json:"targetAccount"`
	SourceUserID  int64                      `json:"sourceUserID"`
	CountsByKind  map[string]json.RawMessage `json:"countsByKind"`
	IDsByKind     map[string][]string        `json:"idsByKind"`
}
type importedItem struct {
	row  *store.Item
	tree *proto.Value
	kind int
}
type character struct {
	uid         int64
	profile     *proto.Value
	items       []importedItem
	bootstrap   map[string]*proto.Value
	raw         map[string][]byte
	inventory   []byte
	fingerprint string
}
type summary struct {
	UID             int64       `json:"uid"`
	Items           int         `json:"items"`
	Containers      map[int]int `json:"containers"`
	DryRun          bool        `json:"dryRun"`
	AlreadyImported bool        `json:"alreadyImported"`
}

func main() {
	source := flag.String("character-dir", "", "Sanitized character snapshot directory")
	target := flag.String("database", "", "Separate Go player database")
	static := flag.String("static-database", "server_data/static.sqlite3", "Read-only static database")
	dry := flag.Bool("dry-run", false, "Validate snapshots without writing player data")
	flag.Parse()
	result, err := run(*source, *target, *static, *dry)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func checkedBlob(root string, entry blobEntry) ([]byte, *proto.Value, error) {
	if entry.File == "" || filepath.Base(entry.File) != entry.File || entry.Size < 1 || entry.Size > 16<<20 {
		return nil, nil, errors.New("invalid snapshot file")
	}
	blob, err := os.ReadFile(filepath.Join(root, entry.File))
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(blob)
	if len(blob) != entry.Size || hex.EncodeToString(sum[:]) != entry.SHA256 {
		return nil, nil, errors.New("snapshot checksum mismatch")
	}
	v, pos, err := proto.DecodeValue(blob, 0, wiregen.Table())
	if err != nil || pos != len(blob) {
		return nil, nil, fmt.Errorf("snapshot decoding failed: %s at %d: %v", entry.File, pos, err)
	}
	encoded, err := proto.EncodeValue(v, wiregen.Table())
	if err != nil || !bytes.Equal(blob, encoded) {
		return nil, nil, fmt.Errorf("snapshot wire roundtrip failed: %s", entry.File)
	}
	return blob, v, nil
}

func validTargetAccount(account string) bool {
	if len(account) < 3 || len(account) > 32 {
		return false
	}
	for _, ch := range account {
		if ch != '_' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func loadCharacter(root string) (*character, error) {
	account := ""
	readManifest := func(name string) (manifest, error) {
		var m manifest
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return m, err
		}
		if err = json.Unmarshal(b, &m); err != nil {
			return m, err
		}
		if m.Schema != 1 || m.ClientVersion != "4.0.25034" || !validTargetAccount(m.TargetAccount) || !m.NamesRedacted {
			return m, errors.New("unsupported or unredacted snapshot")
		}
		if account != "" && !strings.EqualFold(m.TargetAccount, account) {
			return m, errors.New("snapshot account mismatch")
		}
		account = m.TargetAccount
		return m, nil
	}
	profile, err := readManifest("profile.json")
	if err != nil {
		return nil, err
	}
	blob, tree, err := checkedBlob(root, profile.blobEntry)
	if err != nil {
		return nil, err
	}
	if tree.TypeID != 307 || len(tree.Fields) != 26 {
		return nil, errors.New("invalid player profile")
	}
	c := &character{uid: int64(number(tree, 0)), profile: tree, bootstrap: map[string]*proto.Value{}, raw: map[string][]byte{"profile": blob}}
	c.inventory, err = os.ReadFile(filepath.Join(root, "inventory.json"))
	if err != nil {
		return nil, err
	}
	var inventory inventoryIndex
	if err = json.Unmarshal(c.inventory, &inventory); err != nil {
		return nil, err
	}
	if inventory.Schema != 2 || inventory.ClientVersion != "4.0.25034" || !strings.EqualFold(inventory.TargetAccount, account) || inventory.SourceUserID != c.uid || c.uid <= 0 {
		return nil, errors.New("inventory account mismatch")
	}
	knownByKind := map[int]map[string]bool{}
	for kind, ids := range inventory.IDsByKind {
		var number int
		if _, err := fmt.Sscan(kind, &number); err != nil {
			return nil, errors.New("invalid inventory kind")
		}
		knownByKind[number] = map[string]bool{}
		for _, id := range ids {
			guid, err := wireGUID(id)
			if err != nil || knownByKind[number][guid] {
				return nil, errors.New("invalid inventory identifiers")
			}
			knownByKind[number][guid] = true
		}
	}
	details, err := readManifest("details.json")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, entry := range details.Entries {
		blob, list, err := checkedBlob(root, entry)
		if err != nil {
			return nil, err
		}
		if list.Kind != proto.KindArgs || list.ArgCodeStr() != "61" || len(list.Items) != len(entry.IDs) {
			return nil, errors.New("invalid inventory detail array")
		}
		c.raw[entry.File] = blob
		for i, tree := range list.Items {
			guid, err := wireGUID(entry.IDs[i])
			if err != nil {
				return nil, err
			}
			if seen[guid] || !knownByKind[entry.Kind][guid] || tree == nil || tree.Kind != proto.KindObj || named(tree, "cahhdnicmbn").Hex != guid {
				return nil, errors.New("inventory identity mismatch")
			}
			seen[guid] = true
			fields, err := gameserver.FieldsToJSON(tree.Fields)
			if err != nil {
				return nil, err
			}
			stored, err := gameserver.FieldsFromJSON(fields)
			if err != nil {
				return nil, err
			}
			a, err := proto.EncodeValue(tree, wiregen.Table())
			if err != nil {
				return nil, err
			}
			b, err := proto.EncodeValue(proto.Obj(tree.Class, tree.TypeID, stored...), wiregen.Table())
			if err != nil || !bytes.Equal(a, b) {
				return nil, fmt.Errorf("item JSON roundtrip failed for type %d", tree.TypeID)
			}
			container := fmt.Sprintf("native:%d", entry.Kind)
			if entry.Kind == 0 {
				container = "backpack"
			}
			if entry.Kind == 5 {
				container = "keepnet"
			}
			qty := namedNumber(tree, "ljnnpokapig", 1)
			if qty > math.MaxInt64 {
				return nil, errors.New("item quantity exceeds database range")
			}
			row := &store.Item{GUID: guid, UID: c.uid, NumericID: int64(namedNumber(tree, "iofbjlaadfg", 0)), WireClass: tree.Class, TypeID: int64(tree.TypeID), Container: container, Slot: int64(len(c.items)), Qty: int64(qty), FieldsJSON: fields, Durability: namedFloat(tree, "nmidlgejlcp", 1), Workmanship: namedFloat(tree, "hjilekjlgbk", 1)}
			if !finite(row.Durability) || !finite(row.Workmanship) {
				return nil, errors.New("nonfinite item state")
			}
			c.items = append(c.items, importedItem{row, tree, entry.Kind})
		}
	}
	count := 0
	for _, ids := range inventory.IDsByKind {
		for _, id := range ids {
			guid, err := wireGUID(id)
			if err != nil || !seen[guid] {
				return nil, errors.New("missing inventory detail")
			}
			count++
		}
	}
	if count != len(c.items) {
		return nil, errors.New("inventory detail count mismatch")
	}
	bootstrap, err := readManifest(filepath.Join("bootstrap", "index.json"))
	if err != nil {
		return nil, err
	}
	for _, entry := range bootstrap.Entries {
		if len(entry.Operation) != 2 {
			return nil, errors.New("invalid bootstrap operation")
		}
		key := fmt.Sprintf("%d/%d", entry.Operation[0], entry.Operation[1])
		blob, value, err := checkedBlob(filepath.Join(root, "bootstrap"), entry)
		if err != nil {
			return nil, err
		}
		c.raw["bootstrap/"+entry.File] = blob
		c.bootstrap[key] = value
	}
	hash := sha256.New()
	_, _ = hash.Write(c.inventory)
	keys := make([]string, 0, len(c.raw))
	for key := range c.raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, _ = hash.Write([]byte(key))
		_, _ = hash.Write(c.raw[key])
	}
	c.fingerprint = hex.EncodeToString(hash.Sum(nil))
	return c, nil
}

func named(v *proto.Value, name string) proto.Field {
	if v == nil {
		return proto.Field{}
	}
	for _, meta := range wiregen.ClassMeta[v.Class].Fields {
		if meta.Name == name {
			return v.Field(meta.Idx)
		}
	}
	return proto.Field{}
}
func number(v *proto.Value, index int) uint64 {
	f := v.Field(index)
	if f.Num == nil {
		return 0
	}
	return *f.Num
}
func text(v *proto.Value, index int) string {
	f := v.Field(index)
	if f.Str == nil {
		return ""
	}
	return *f.Str
}
func namedNumber(v *proto.Value, name string, fallback uint64) uint64 {
	f := named(v, name)
	if f.Num == nil {
		return fallback
	}
	return *f.Num
}
func namedFloat(v *proto.Value, name string, fallback float64) float64 {
	f := named(v, name)
	if f.Op == "FLOAT" || f.Op == "DOUBLE" {
		return f.Float
	}
	if f.Num != nil {
		return float64(math.Float32frombits(uint32(*f.Num)))
	}
	return fallback
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func wireGUID(value string) (string, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", errors.New("invalid inventory GUID")
	}
	b, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil {
		return "", err
	}
	if bytes.Equal(b, make([]byte, 16)) {
		return "", errors.New("zero inventory GUID")
	}
	for _, pair := range [][2]int{{0, 3}, {1, 2}, {4, 5}, {6, 7}} {
		b[pair[0]], b[pair[1]] = b[pair[1]], b[pair[0]]
	}
	return hex.EncodeToString(b), nil
}

func run(source, target, static string, dry bool) (summary, error) {
	c, err := loadCharacter(source)
	if err != nil {
		return summary{}, err
	}
	result := summary{UID: c.uid, Items: len(c.items), Containers: map[int]int{}, DryRun: dry}
	for _, item := range c.items {
		result.Containers[item.kind]++
	}
	if dry {
		target = ":memory:"
	} else if target == "" || filepath.Clean(target) == ":memory:" {
		return result, errors.New("a separate player database path is required")
	}
	if !dry {
		if err := checkTarget(target); err != nil {
			return result, err
		}
	}
	db, err := store.OpenWithStatic(target, static)
	if err != nil {
		return result, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for i := range c.items {
		it := c.items[i].row
		if c.items[i].kind == 5 && it.TypeID == 25 {
			it.ItemID = text(c.items[i].tree, 0)
			continue
		}
		definition, err := db.GetItemDef(ctx, it.NumericID)
		if err != nil {
			return result, fmt.Errorf("missing static item definition %d", it.NumericID)
		}
		it.ItemID = definition.ItemID
	}
	if dry {
		return result, nil
	}
	if err := db.Migrate(); err != nil {
		return result, err
	}
	result.AlreadyImported, err = writeCharacter(ctx, db, c)
	return result, err
}

func checkTarget(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(abs)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return err
	}
	defer rows.Close()
	allowed := store.TableOwnership()
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return err
		}
		if _, ok := allowed[table]; !ok {
			return fmt.Errorf("target is not an RF4 Go player database: %s", table)
		}
	}
	return rows.Err()
}

func writeCharacter(ctx context.Context, db *store.DB, c *character) (bool, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	marker := fmt.Sprintf("native-import:%d:fingerprint", c.uid)
	var previous string
	err = tx.QueryRowContext(ctx, "SELECT v FROM kv WHERE k=?", marker).Scan(&previous)
	if err == nil {
		if previous == c.fingerprint {
			return true, nil
		}
		return false, errors.New("different snapshot already imported; existing progress preserved")
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM profile WHERE uid=?", c.uid).Scan(&existing); err != nil {
		return false, err
	}
	if existing != 0 {
		return false, errors.New("target character already exists; no data overwritten")
	}
	p := c.profile
	for _, index := range []int{0, 2, 3, 7, 8, 10, 11, 19} {
		if number(p, index) > math.MaxInt64 {
			return false, errors.New("profile number exceeds database range")
		}
	}
	membershipExpiresAt := int64(0)
	if number(p, 11) != 0 {
		membershipExpiresAt = max(int64(0), proto.FiletimeToUnix(int64(number(p, 11))))
	}
	now := float64(time.Now().Unix())
	if _, err = tx.ExecContext(ctx, `INSERT INTO profile(uid,name,level,xp,silver,gold,membership,membership_exp,bank,region,created_ts,updated_ts) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, c.uid, text(p, 1), number(p, 2), number(p, 3), number(p, 7), number(p, 8), number(p, 10), membershipExpiresAt, number(p, 19), "CN", now, now); err != nil {
		return false, err
	}
	if spawn := c.bootstrap["1/13"]; spawn != nil {
		mapTree := spawn.Field(0).Val
		if mapTree != nil && mapTree.TypeID == 205 && text(mapTree, 0) != "" {
			if _, err = tx.ExecContext(ctx, "UPDATE profile SET last_level_key=?,last_scene=?,location_updated_ts=? WHERE uid=?", text(mapTree, 0), text(spawn, 1), now, c.uid); err != nil {
				return false, err
			}
		}
	}
	rods := map[string]bool{}
	for _, item := range c.items {
		if item.row.TypeID == 157 {
			rods[item.row.GUID] = true
		}
	}
	for _, item := range c.items {
		it := item.row
		parent := named(item.tree, "odflendafcg").Hex
		if rods[parent] {
			it.RigGUID = parent
			if item.kind == 0 {
				it.Container = "rig"
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO item(guid,uid,item_id,numeric_id,wire_class,type_id,container,slot,rig_guid,qty,buy_price,fields_json,created_ts,durability,workmanship) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, it.GUID, it.UID, it.ItemID, it.NumericID, it.WireClass, it.TypeID, it.Container, it.Slot, it.RigGUID, it.Qty, it.BuyPrice, it.FieldsJSON, it.CreatedTS, it.Durability, it.Workmanship); err != nil {
			return false, err
		}
		if item.kind == 5 {
			if err = insertFish(ctx, tx, c.uid, item.tree); err != nil {
				return false, err
			}
		}
		if item.row.TypeID == 157 {
			if err = insertRig(ctx, tx, c.uid, item.tree); err != nil {
				return false, err
			}
		}
	}
	if skills := c.bootstrap["5/1"]; skills != nil {
		cats := skills.Field(1).Val
		if cats != nil {
			for _, cat := range cats.Items {
				if _, err = tx.ExecContext(ctx, "INSERT INTO skill(uid,skill_id,points) VALUES(?,?,?)", c.uid, number(cat, 0), math.Float32frombits(uint32(number(cat, 1)))); err != nil {
					return false, err
				}
				nodes := cat.Field(3).Val
				if nodes != nil {
					for _, node := range nodes.Items {
						if _, err = tx.ExecContext(ctx, "INSERT INTO skill_node(uid,node_id,level) VALUES(?,?,?)", c.uid, number(node, 0), number(node, 3)); err != nil {
							return false, err
						}
					}
				}
			}
		}
	}
	if slots := c.bootstrap["11/1"]; slots != nil {
		bindings := map[int64]string{}
		for _, slot := range slots.Items {
			guid := slot.Field(1).Hex
			if guid != "" && guid != strings.Repeat("0", 32) {
				key := int64(number(slot, 0))
				if _, exists := bindings[key]; exists {
					return false, errors.New("duplicate shortcut slot")
				}
				bindings[key] = guid
			}
		}
		if err = store.SaveShortcutsTx(ctx, tx, c.uid, bindings); err != nil {
			return false, err
		}
	}
	for key, raw := range c.raw {
		if _, err = tx.ExecContext(ctx, "INSERT INTO kv(k,v) VALUES(?,?)", fmt.Sprintf("native-import:%d:wire:%s", c.uid, key), hex.EncodeToString(raw)); err != nil {
			return false, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO kv(k,v) VALUES(?,?)", fmt.Sprintf("native-import:%d:inventory", c.uid), string(c.inventory)); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO kv(k,v) VALUES(?,?)", marker, c.fingerprint); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

func insertFish(ctx context.Context, tx *sql.Tx, uid int64, tree *proto.Value) error {
	if tree.TypeID != 25 {
		return errors.New("unsupported keepnet item type")
	}
	ts := float64(number(tree, 5))/10000000 - 11644473600
	_, err := tx.ExecContext(ctx, `INSERT INTO keepnet_items(echo_guid,uid,species,name_zh,weight_g,length_m,cat,keep_loc,keep_spot,keep_bait,keep_level,ts) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, named(tree, "cahhdnicmbn").Hex, uid, text(tree, 0), text(tree, 11), number(tree, 1), math.Float32frombits(uint32(number(tree, 2))), number(tree, 3), text(tree, 10), 0, text(tree, 8), text(tree, 10), ts)
	return err
}
func insertRig(ctx context.Context, tx *sql.Tx, uid int64, tree *proto.Value) error {
	guid := named(tree, "cahhdnicmbn").Hex
	rig := tree.Field(5).Val
	if rig == nil || rig.Kind == proto.KindNull {
		return nil
	}
	if rig.TypeID != 153 {
		return errors.New("unsupported rod rig type")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO rig(guid,uid,rig_type,name,fields_json,created_ts) VALUES(?,?,?,?,?,?)", guid, uid, number(rig, 1), text(rig, 0), "{}", float64(time.Now().Unix())); err != nil {
		return err
	}
	parts := rig.Field(2).Val
	ordinals := map[uint64]int{}
	insert := func(slot uint64, child string, extra uint64) error {
		if child == "" || child == strings.Repeat("0", 32) {
			return nil
		}
		ordinal := ordinals[slot]
		ordinals[slot]++
		_, err := tx.ExecContext(ctx, "INSERT INTO rig_component(rig_guid,slot,ordinal,guid,extra) VALUES(?,?,?,?,?)", guid, slot, ordinal, child, extra)
		return err
	}
	if parts != nil {
		for _, part := range parts.Items {
			f := part.Field(1)
			if err := insert(number(part, 0), f.Hex, number(part, 2)); err != nil {
				return err
			}
		}
	}
	for index, slot := range map[int]uint64{3: 11, 4: 6} {
		f := tree.Field(index)
		if err := insert(slot, f.Hex, 0); err != nil {
			return err
		}
	}
	return nil
}
