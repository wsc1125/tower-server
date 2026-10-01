package room

import (
	"context"
	"errors"
)

// ErrSessionNotFound 本局会话不存在。
var ErrSessionNotFound = errors.New("本局会话不存在")

// CreateSession 房主为本房间创建一局游戏会话。
//
// 只有房主能创建：会话里的监听地址由房主提供，其他人创建出来的会话
// 指向的地址不可信，成员连过去只会失败。
func (s *Service) CreateSession(ctx context.Context, code string, accountID int64) (*GameSession, error) {
	room, err := s.mustGetRoom(ctx, code)
	if err != nil {
		return nil, err
	}

	if room.HostID != accountID {
		return nil, ErrNotHost
	}

	session := s.sessions.Create(code, accountID)

	s.logger.Info().
		Str("room_code", code).
		Str("session_id", session.ID).
		Int64("host_account", accountID).
		Msg("游戏会话已创建")

	return session, nil
}

// GetSession 查询房间当前会话。
//
// 返回 bool 而非 error：会话不存在是正常状态（还没出发），
// 调用方据此决定是「等待房主」还是「报错」。
func (s *Service) GetSession(code string) (*GameSession, bool) {
	return s.sessions.Get(code)
}

// MarkSessionReady 房主上报监听地址，会话转为 ready。
func (s *Service) MarkSessionReady(code string, accountID int64, address string, port int) (*GameSession, error) {
	room, ok := s.registry.Get(code)
	if !ok {
		return nil, ErrRoomNotFound
	}

	if room.HostID != accountID {
		return nil, ErrNotHost
	}

	if !s.sessions.SetReady(code, address, port) {
		return nil, ErrSessionNotFound
	}

	session, _ := s.sessions.Get(code)

	s.logger.Info().
		Str("room_code", code).
		Str("session_id", session.ID).
		Str("host_address", address).
		Int("host_port", port).
		Msg("游戏会话已就绪")

	return session, nil
}

// CloseSession 房主结束本局会话。
//
// 只删内存会话、不动房间：本局结束不代表大厅解散，
// 成员应能留在房间里再开一局。
func (s *Service) CloseSession(code string, accountID int64) error {
	room, ok := s.registry.Get(code)
	if !ok {
		return ErrRoomNotFound
	}

	if room.HostID != accountID {
		return ErrNotHost
	}

	s.sessions.Remove(code)

	s.logger.Info().
		Str("room_code", code).
		Int64("host_account", accountID).
		Msg("游戏会话已关闭")

	return nil
}
