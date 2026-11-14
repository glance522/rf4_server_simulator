package gameserver

import (
	"rf4server/internal/proto"
)

// broadcastChatProbe 向所有在线会话推送同一条聊天消息（跨玩家频道播报）。
//
// skip 非空时跳过该会话：调用方已经单独把同一条消息推给了本人，
// 再推一遍会让当事人看到重复的两行。
// fromUID 是这条消息在客户端显示的发送者；跨会话必须显式指定，
// 否则会显示成收件人自己发的。
//
// 一次播报要先锁内快照再锁外推送：Push 会写隧道，持 s.mu 推送可能
// 与被关闭会话的回收路径互相等待。
func (s *Server) broadcastChatProbe(skip *Session, fromUID uint64, channel, kind int, text string, kv *proto.Value) {
	if s == nil {
		return
	}
	s.mu.Lock()
	peers := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess == nil || sess == skip || sess.Closed() {
			continue
		}
		peers = append(peers, sess)
	}
	s.mu.Unlock()
	uid := uint32(fromUID)
	if s.log != nil {
		s.log.Info("频道播报", "from", uid, "text", text, "kind", kind, "peers", len(peers))
	}
	for _, sess := range peers {
		sess.pushChatProbeAs(channel, kind, uid, text, kv)
	}
}

// broadcastChatProbeIncludingSelf 把同一条聊天消息推给本人和所有其他在线会话，
// 每人恰好一条。用于"自己钓到鱼"这类场景：当事人既要看到，也不能多看一条。
// 走 self 直推而不是依赖 s.sessions 快照，避免自会话刚建立/刚重连时漏推。
func (s *Server) broadcastChatProbeIncludingSelf(self *Session, fromUID uint64, channel, kind int, text string, kv *proto.Value) {
	if self != nil && !self.Closed() {
		self.pushChatProbeAs(channel, kind, uint32(fromUID), text, kv)
	}
	s.broadcastChatProbe(self, fromUID, channel, kind, text, kv)
}
