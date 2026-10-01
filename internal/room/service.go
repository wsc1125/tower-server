package room

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"

	"github.com/tower/server/internal/signal"
)

// 业务错误。handler 按这些错误映射 HTTP 状态码。
var (
	// ErrInvalidName 房间名不合法。
	ErrInvalidName = errors.New("房间名需为 1~64 个字符")

	// ErrInvalidMaxPlayers 人数上限不合法。
	ErrInvalidMaxPlayers = errors.New("房间人数上限需为 2~4")

	// ErrInvalidCode 房间码格式不合法。
	ErrInvalidCode = errors.New("房间码格式不正确")

	// ErrAlreadyInRoom 账号已在其他房间内。
	ErrAlreadyInRoom = errors.New("你已在其他房间中，请先离开")

	// ErrRoomFull 房间已满。
	ErrRoomFull = errors.New("房间已满")

	// ErrRoomNotJoinable 房间不在可加入状态。
	ErrRoomNotJoinable = errors.New("房间当前不可加入")

	// ErrNotHost 操作者不是房主。
	ErrNotHost = errors.New("只有房主可以执行此操作")

	// ErrNotMember 操作者不在房间内。
	ErrNotMember = errors.New("你不在该房间内")
)

// 房间参数约束。
const (
	minPlayerLimit = 2
	maxPlayerLimit = 4

	// codeGenAttempts 房间码生成的最大重试次数。
	// 32^8 的空间下冲突概率极低，5 次足够。
	codeGenAttempts = 5
)

// Service 房间业务逻辑。
type Service struct {
	repo     *Repository
	registry *Registry
	logger   zerolog.Logger
}

// NewService 创建房间服务。
func NewService(repo *Repository, registry *Registry, logger zerolog.Logger) *Service {
	return &Service{repo: repo, registry: registry, logger: logger}
}

// Create 创建房间：落库 + 内存建 Room + 房主加入。
func (s *Service) Create(ctx context.Context, hostAccount int64, displayName, name string, maxPlayers int, isPublic bool) (Snapshot, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return Snapshot{}, ErrInvalidName
	}

	if maxPlayers < minPlayerLimit || maxPlayers > maxPlayerLimit {
		return Snapshot{}, ErrInvalidMaxPlayers
	}

	// 一个账号同时只能在一个房间 —— 否则会出现「同一人在两个房间」的幽灵成员。
	if existing, ok := s.registry.FindByAccount(hostAccount); ok {
		return Snapshot{}, fmt.Errorf("%w（当前在房间 %s）", ErrAlreadyInRoom, existing.Code)
	}

	rec, err := s.createRecordWithRetry(ctx, hostAccount, name, maxPlayers, isPublic)
	if err != nil {
		return Snapshot{}, err
	}

	host := &Member{
		AccountID:   hostAccount,
		DisplayName: displayName,
		JoinedAt:    time.Now(),
	}

	room := newRoom(rec.Code, host, name, maxPlayers, isPublic)
	if !s.registry.Add(room) {
		// 内存里已有同码房间（理论上不该发生，因为落库时已保证唯一）。
		// 回滚数据库记录，避免留下孤儿房间。
		_ = s.repo.Close(ctx, rec.Code)
		return Snapshot{}, fmt.Errorf("房间码冲突，请重试")
	}

	s.logger.Info().
		Str("room_code", rec.Code).
		Int64("host_account", hostAccount).
		Msg("房间已创建")

	return room.Snapshot(), nil
}

// Join 加入房间。
func (s *Service) Join(ctx context.Context, code string, accountID int64, displayName string) (Snapshot, error) {
	room, err := s.mustGetRoom(ctx, code)
	if err != nil {
		return Snapshot{}, err
	}

	// 已在房内视为幂等成功 —— 客户端重试不该报错。
	if room.HasMember(accountID) {
		return room.Snapshot(), nil
	}

	if other, ok := s.registry.FindByAccount(accountID); ok {
		return Snapshot{}, fmt.Errorf("%w（当前在房间 %s）", ErrAlreadyInRoom, other.Code)
	}

	if room.Status != StatusWaiting {
		return Snapshot{}, ErrRoomNotJoinable
	}

	member := &Member{
		AccountID:   accountID,
		DisplayName: displayName,
		JoinedAt:    time.Now(),
	}

	if !room.AddMember(member) {
		return Snapshot{}, ErrRoomFull
	}

	s.logger.Info().
		Str("room_code", code).
		Int64("account_id", accountID).
		Msg("成员加入房间")

	return room.Snapshot(), nil
}

// Leave 离开房间。房主离开时触发房主迁移或关闭房间。
//
// 返回 (房间快照, 房间是否已关闭)。房间关闭时快照为零值。
func (s *Service) Leave(ctx context.Context, code string, accountID int64) (Snapshot, bool, error) {
	room, err := s.mustGetRoom(ctx, code)
	if err != nil {
		return Snapshot{}, false, err
	}

	if !room.HasMember(accountID) {
		return Snapshot{}, false, ErrNotMember
	}

	return s.removeAndRebalance(ctx, room, accountID)
}

// Close 房主主动关闭房间。
func (s *Service) Close(ctx context.Context, code string, accountID int64) error {
	room, err := s.mustGetRoom(ctx, code)
	if err != nil {
		return err
	}

	if room.HostID != accountID {
		return ErrNotHost
	}

	if err := s.repo.Close(ctx, code); err != nil {
		return err
	}

	room.SetStatus(StatusClosed)
	s.registry.Remove(code)

	s.logger.Info().Str("room_code", code).Int64("account_id", accountID).Msg("房间已关闭")

	return nil
}

// Get 查询房间详情。
func (s *Service) Get(ctx context.Context, code string) (Snapshot, error) {
	room, err := s.mustGetRoom(ctx, code)
	if err != nil {
		return Snapshot{}, err
	}

	return room.Snapshot(), nil
}

// ListEntry 房间列表项。
type ListEntry struct {
	Code            string `json:"room_code"`
	Name            string `json:"name"`
	HostDisplayName string `json:"host_display_name"`
	CurrentPlayers  int    `json:"current_players"`
	MaxPlayers      int    `json:"max_players"`
	Status          Status `json:"status"`
}

// List 列出公开且等待中的房间。
//
// 以数据库为准（重启后内存注册表为空，但房间记录仍在），
// 人数从内存注册表补齐 —— 成员不落库。
func (s *Service) List(ctx context.Context) ([]ListEntry, error) {
	records, err := s.repo.ListPublicWaiting(ctx)
	if err != nil {
		return nil, err
	}

	entries := make([]ListEntry, 0, len(records))
	for _, rec := range records {
		entry := ListEntry{
			Code:           rec.Code,
			Name:           rec.Name,
			MaxPlayers:     rec.MaxPlayers,
			Status:         Status(rec.Status),
			CurrentPlayers: 0,
		}

		if room, ok := s.registry.Get(rec.Code); ok {
			entry.CurrentPlayers = room.MemberCount()

			// 房主昵称从内存成员取；服务重启后内存为空，回退到空串。
			for _, m := range room.Members() {
				if m.AccountID == room.HostID {
					entry.HostDisplayName = m.DisplayName
					break
				}
			}
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// CleanupStale 关闭超时未活动的房间，返回关闭数量。
//
// 由后台定时任务调用。同时清理内存注册表 —— 否则注册表会随运行时间无限增长。
func (s *Service) CleanupStale(ctx context.Context, idle time.Duration) (int64, error) {
	closed, err := s.repo.CloseStale(ctx, idle)
	if err != nil {
		return 0, err
	}

	// 内存侧：把超过 idle 未活动的 waiting 房间一并摘掉。
	// 与数据库判定条件保持一致，避免两边状态分叉。
	cutoff := time.Now().Add(-idle)
	for _, room := range s.registry.All() {
		if room.Status == StatusWaiting && room.LastActiveAt().Before(cutoff) {
			s.registry.Remove(room.Code)
		}
	}

	return closed, nil
}

// HostAccountID 返回房间当前房主；房间不存在时返回 0。
//
// 供信令模块判断「谁有权开始游戏」。
func (s *Service) HostAccountID(code string) int64 {
	room, ok := s.registry.Get(code)
	if !ok {
		return 0
	}

	return room.HostID
}

// IsMember 判断账号是否在房间内。
//
// 供信令模块做连接鉴权：非成员不得连入信令，否则凭房间码即可偷听。
func (s *Service) IsMember(code string, accountID int64) bool {
	room, ok := s.registry.Get(code)
	if !ok {
		return false
	}

	return room.HasMember(accountID)
}

// OnDisconnect 处理信令连接断开，返回新房主信息供信令模块广播。
//
// 与 Leave 的区别：Leave 是玩家主动退出（HTTP 调用），
// OnDisconnect 是连接被动断开（网络中断 / 客户端崩溃）。
// 两者最终都走 removeAndRebalance，因此房主迁移逻辑只有一份。
func (s *Service) OnDisconnect(code string, accountID int64) signal.DisconnectResult {
	room, ok := s.registry.Get(code)
	if !ok {
		return signal.DisconnectResult{Closed: true}
	}

	if !room.HasMember(accountID) {
		return signal.DisconnectResult{}
	}

	// 用独立 context：连接已断，请求 context 已取消。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wasHost := room.HostID == accountID

	snapshot, closed, err := s.removeAndRebalance(ctx, room, accountID)
	if err != nil {
		s.logger.Error().Err(err).
			Str("room_code", code).
			Int64("account_id", accountID).
			Msg("断线处理失败")

		return signal.DisconnectResult{}
	}

	s.logger.Info().
		Str("room_code", code).
		Int64("account_id", accountID).
		Bool("room_closed", closed).
		Msg("信令连接断开，已移除成员")

	if closed {
		return signal.DisconnectResult{Closed: true}
	}

	result := signal.DisconnectResult{}

	// 只有原房主离开才需要通知迁移；普通成员离开时房主未变。
	if wasHost {
		result.NewHostAccountID = snapshot.HostID

		for _, m := range snapshot.Members {
			if m.AccountID == snapshot.HostID {
				result.NewHostDisplayName = m.DisplayName
				break
			}
		}
	}

	return result
}

// removeAndRebalance 移除成员并处理房主迁移 / 房间关闭。
//
// 这是房间生命周期的核心分支，抽出来供 Leave 与后续的信令断线处理复用。
func (s *Service) removeAndRebalance(ctx context.Context, room *Room, accountID int64) (Snapshot, bool, error) {
	wasHost := room.HostID == accountID

	_, empty := room.RemoveMember(accountID)

	// 房间已空 → 关闭。
	if empty {
		if err := s.repo.Close(ctx, room.Code); err != nil {
			return Snapshot{}, false, err
		}

		room.SetStatus(StatusClosed)
		s.registry.Remove(room.Code)

		s.logger.Info().Str("room_code", room.Code).Msg("房间已空，自动关闭")

		return Snapshot{}, true, nil
	}

	// 房主离开但房内还有人 → 房主迁移给最早加入者。
	if wasHost {
		next := room.EarliestMember(accountID)
		if next == nil {
			// 理论上不可达：非空房间必有成员。兜底关闭，避免房间卡在无主状态。
			_ = s.repo.Close(ctx, room.Code)
			room.SetStatus(StatusClosed)
			s.registry.Remove(room.Code)

			return Snapshot{}, true, nil
		}

		room.SetHost(next.AccountID)

		if err := s.repo.UpdateHost(ctx, room.Code, next.AccountID); err != nil {
			// 落库失败不回滚内存迁移：内存是权威（成员本就不落库），
			// 数据库只是备份。记日志，下次房间状态变更时会再写一次。
			s.logger.Error().Err(err).
				Str("room_code", room.Code).
				Int64("new_host", next.AccountID).
				Msg("房主迁移落库失败")
		}

		s.logger.Info().
			Str("room_code", room.Code).
			Int64("old_host", accountID).
			Int64("new_host", next.AccountID).
			Msg("房主已迁移")
	}

	return room.Snapshot(), false, nil
}

// mustGetRoom 取房间：先查内存，未命中再回源数据库。
//
// 回源是为了支持「服务重启后房间仍在」—— 重启后内存注册表为空，
// 但数据库里 waiting 的房间记录还在，玩家仍应能加入。
func (s *Service) mustGetRoom(ctx context.Context, code string) (*Room, error) {
	if !IsValidCode(code) {
		return nil, ErrInvalidCode
	}

	if room, ok := s.registry.Get(code); ok {
		return room, nil
	}

	rec, err := s.repo.FindByCode(ctx, code)
	if err != nil {
		return nil, err
	}

	if rec.Status == string(StatusClosed) {
		return nil, ErrRoomNotFound
	}

	// 从数据库记录重建内存房间。成员列表为空 —— 原成员需重新加入。
	room := &Room{
		Code:       rec.Code,
		HostID:     rec.HostAccount,
		Name:       rec.Name,
		MaxPlayers: rec.MaxPlayers,
		IsPublic:   rec.IsPublic,
		Status:     Status(rec.Status),
		CreatedAt:  rec.CreatedAt,
		UpdatedAt:  rec.UpdatedAt,
		members:    make(map[int64]*Member),
	}

	s.registry.Add(room)

	return room, nil
}

// createRecordWithRetry 生成房间码并落库，冲突则重试。
func (s *Service) createRecordWithRetry(ctx context.Context, hostAccount int64, name string, maxPlayers int, isPublic bool) (*Record, error) {
	for attempt := 0; attempt < codeGenAttempts; attempt++ {
		code, err := GenerateCode()
		if err != nil {
			return nil, fmt.Errorf("生成房间码失败: %w", err)
		}

		rec, err := s.repo.Create(ctx, code, hostAccount, name, maxPlayers, isPublic)
		if err == nil {
			return rec, nil
		}

		if errors.Is(err, ErrCodeTaken) {
			continue
		}

		return nil, err
	}

	return nil, fmt.Errorf("房间码生成冲突次数过多，请重试")
}
