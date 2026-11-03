package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"rf4server/internal/store"
)

type response struct {
	OK bool `json:"ok"`
	*store.MembershipStatus
	Purchase *store.MembershipPurchase `json:"purchase,omitempty"`
	Error    *store.MembershipError    `json:"error,omitempty"`
}

func run(args []string, out io.Writer) int {
	fail := func(err error) int {
		var business *store.MembershipError
		if !errors.As(err, &business) {
			business = &store.MembershipError{Code: "database_error", Message: "本地会员数据暂时不可用"}
		}
		_ = json.NewEncoder(out).Encode(response{Error: business})
		return 1
	}
	invalid := func() int {
		return fail(&store.MembershipError{Code: "invalid_request", Message: "本地会员参数无效"})
	}
	flags := flag.NewFlagSet("local-membership", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("database", "", "本地角色数据库")
	uid := flags.Int64("uid", 0, "本地角色ID")
	if flags.Parse(args) != nil || *database == "" || *uid <= 0 || len(flags.Args()) == 0 {
		return invalid()
	}
	action, rest := flags.Args()[0], flags.Args()[1:]
	var plan, request string
	if action == "purchase" {
		purchaseFlags := flag.NewFlagSet("purchase", flag.ContinueOnError)
		purchaseFlags.SetOutput(io.Discard)
		purchaseFlags.StringVar(&plan, "plan", "", "套餐")
		purchaseFlags.StringVar(&request, "request-id", "", "幂等请求编号")
		if purchaseFlags.Parse(rest) != nil || len(purchaseFlags.Args()) != 0 {
			return invalid()
		}
	} else if action != "status" || len(rest) != 0 {
		return invalid()
	}
	info, err := os.Stat(*database)
	if err != nil || info.IsDir() {
		return fail(errors.New("missing database"))
	}
	db, err := store.Open(*database)
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().Unix()
	var receipt *store.MembershipPurchase
	if action == "purchase" {
		receipt, err = db.PurchaseMembership(ctx, *uid, plan, request, now)
		if err != nil {
			return fail(err)
		}
	}
	status, err := db.GetMembershipStatus(ctx, *uid, now)
	if err != nil {
		return fail(err)
	}
	if json.NewEncoder(out).Encode(response{OK: true, MembershipStatus: status, Purchase: receipt}) != nil {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }
