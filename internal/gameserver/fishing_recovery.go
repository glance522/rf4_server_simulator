package gameserver

import (
	"errors"
	"rf4server/internal/store"
)

func completedCatchPending(st *FishState) bool {
	return st.Pending != nil && st.Hooked && !st.CastActive && !st.Resolved
}

func (s *Server) pendingCatchAllowsCast(ctx *Ctx, st *FishState) bool {
	if err := s.recoverCompletedCatch(ctx.Sess.Userid, ctx.Sess); err != nil {
		message := "待处理渔获尚未入护，请稍后重试"
		if errors.Is(err, store.ErrKeepnetFull) {
			message = "护网已满，请先腾出位置，待处理渔获会自动入护"
		}
		ctx.ReplyFailure("System.InvalidOperationException", message)
		return false
	}
	st.mu.Lock()
	blocked := completedCatchPending(st)
	st.mu.Unlock()
	if blocked {
		ctx.ReplyFailure("System.InvalidOperationException", "请先将本次渔获入护或放生")
	}
	return !blocked
}
