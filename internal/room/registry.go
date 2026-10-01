package room

import (
	"sort"
	"sync"
	"time"
)

// Status 房间状态。
type Status string

const (
	// StatusWaiting 等待中，可加入。
	StatusWaiting Status = "waiting"

	// StatusPlaying 游戏中，不再接受新成员。
	StatusPlaying Status = "playing"

	// StatusClosed 已关闭。
	StatusClosed Status = "closed"
)

// Member 房间成员。运行时状态，不落库。
//
// Conn 字段留给信令模块（步骤 6）挂 WebSocket 连接；
// 本期房间模块只用 AccountID / DisplayName / JoinedAt。
type Member struct {
	AccountID   int64
	DisplayName string
	AvatarURL   string
	JoinedAt    time.Time
}

// Room 内存中的房间。
//
// 成员列表存内存不落库：成员的生命周期与 WebSocket 连接绑定，
// 连接断了成员就该消失。落库反而要额外处理「僵尸成员」清理。
type Room struct {
	Code       string
	HostID     int64
	Name       string
	MaxPlayers int
	IsPublic   bool
	Status     Status
	CreatedAt  time.Time
	UpdatedAt  time.Time

	mu      sync.RWMutex
	members map[int64]*Member
}

// newRoom 创建内存房间，并把房主作为第一个成员加入。
func newRoom(code string, host *Member, name string, maxPlayers int, isPublic bool) *Room {
	now := time.Now()

	return &Room{
		Code:       code,
		HostID:     host.AccountID,
		Name:       name,
		MaxPlayers: maxPlayers,
		IsPublic:   isPublic,
		Status:     StatusWaiting,
		CreatedAt:  now,
		UpdatedAt:  now,
		members:    map[int64]*Member{host.AccountID: host},
	}
}

// Members 返回成员快照，按加入时间升序。
//
// 返回副本而非内部 map：调用方（如广播）遍历时不应持有锁，
// 否则一个慢客户端会阻塞整个房间的进出操作。
func (r *Room) Members() []*Member {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]*Member, 0, len(r.members))
	for _, m := range r.members {
		list = append(list, m)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].JoinedAt.Before(list[j].JoinedAt)
	})

	return list
}

// MemberCount 当前成员数。
func (r *Room) MemberCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.members)
}

// HasMember 判断账号是否在房间内。
func (r *Room) HasMember(accountID int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.members[accountID]

	return ok
}

// AddMember 加入成员。房间已满或已在房内时返回 false。
func (r *Room) AddMember(m *Member) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.members[m.AccountID]; exists {
		return false
	}

	if len(r.members) >= r.MaxPlayers {
		return false
	}

	r.members[m.AccountID] = m
	r.UpdatedAt = time.Now()

	return true
}

// RemoveMember 移除成员，返回该成员与「移除后房间是否已空」。
func (r *Room) RemoveMember(accountID int64) (*Member, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, exists := r.members[accountID]
	if !exists {
		return nil, len(r.members) == 0
	}

	delete(r.members, accountID)
	r.UpdatedAt = time.Now()

	return m, len(r.members) == 0
}

// EarliestMember 返回除 exclude 外最早加入的成员，用于房主迁移。
//
// 选最早加入者：加入越早对当前局状态越熟悉，且顺序确定 ——
// 无需多端协商，避免竞争。
func (r *Room) EarliestMember(exclude int64) *Member {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var earliest *Member
	for id, m := range r.members {
		if id == exclude {
			continue
		}

		if earliest == nil || m.JoinedAt.Before(earliest.JoinedAt) {
			earliest = m
		}
	}

	return earliest
}

// SetHost 移交房主。
func (r *Room) SetHost(accountID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.HostID = accountID
	r.UpdatedAt = time.Now()
}

// SetStatus 更新房间状态。
func (r *Room) SetStatus(status Status) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.Status = status
	r.UpdatedAt = time.Now()
}

// Snapshot 返回房间的只读快照，供 HTTP 响应序列化。
//
// 单独定义快照类型而非直接序列化 Room：Room 带 mutex 与内部 map，
// 直接序列化既会暴露内部结构，也会在序列化时与写操作竞争。
type Snapshot struct {
	Code       string        `json:"room_code"`
	Name       string        `json:"name"`
	HostID     int64         `json:"host_account"`
	MaxPlayers int           `json:"max_players"`
	IsPublic   bool          `json:"is_public"`
	Status     Status        `json:"status"`
	Members    []MemberBrief `json:"members"`
	CreatedAt  time.Time     `json:"created_at"`
}

// MemberBrief 成员在响应中的精简表示。
type MemberBrief struct {
	AccountID   int64     `json:"account_id"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url"`
	IsHost      bool      `json:"is_host"`
	JoinedAt    time.Time `json:"joined_at"`
}

// Snapshot 生成房间快照。
func (r *Room) Snapshot() Snapshot {
	r.mu.RLock()
	hostID := r.HostID
	status := r.Status
	r.mu.RUnlock()

	members := r.Members()
	briefs := make([]MemberBrief, 0, len(members))
	for _, m := range members {
		briefs = append(briefs, MemberBrief{
			AccountID:   m.AccountID,
			DisplayName: m.DisplayName,
			AvatarURL:   m.AvatarURL,
			IsHost:      m.AccountID == hostID,
			JoinedAt:    m.JoinedAt,
		})
	}

	return Snapshot{
		Code:       r.Code,
		Name:       r.Name,
		HostID:     hostID,
		MaxPlayers: r.MaxPlayers,
		IsPublic:   r.IsPublic,
		Status:     status,
		Members:    briefs,
		CreatedAt:  r.CreatedAt,
	}
}

// LastActiveAt 房间最后一次活动时间，用于超时清理判定。
func (r *Room) LastActiveAt() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.UpdatedAt
}

// Registry 内存房间注册表。
//
// 只保存「活跃房间」（waiting / playing）。关闭的房间从表中移除，
// 历史记录留在数据库里。
type Registry struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{rooms: make(map[string]*Room)}
}

// Add 登记房间。房间码已存在时返回 false。
func (g *Registry) Add(r *Room) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.rooms[r.Code]; exists {
		return false
	}

	g.rooms[r.Code] = r

	return true
}

// Get 按房间码取房间。
func (g *Registry) Get(code string) (*Room, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	r, ok := g.rooms[code]

	return r, ok
}

// Remove 从注册表移除房间。
func (g *Registry) Remove(code string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.rooms, code)
}

// All 返回全部活跃房间的快照。
func (g *Registry) All() []*Room {
	g.mu.RLock()
	defer g.mu.RUnlock()

	list := make([]*Room, 0, len(g.rooms))
	for _, r := range g.rooms {
		list = append(list, r)
	}

	return list
}

// FindByAccount 查找账号当前所在的房间。
//
// 用于「一个账号同时只能在一个房间」的约束校验。
func (g *Registry) FindByAccount(accountID int64) (*Room, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for _, r := range g.rooms {
		if r.HasMember(accountID) {
			return r, true
		}
	}

	return nil, false
}
