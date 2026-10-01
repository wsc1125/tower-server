package room

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// 会话状态。
const (
	// SessionStatusConnecting 已创建会话，等待房主上报监听地址。
	SessionStatusConnecting = "connecting"

	// SessionStatusReady 房主已上报地址，成员可以据此发起连接。
	SessionStatusReady = "ready"

	// SessionStatusInGame 本局已开始。
	SessionStatusInGame = "in_game"

	// SessionStatusClosed 本局已结束。
	SessionStatusClosed = "closed"
)

// GameSession 一局联机游戏的连接描述。
//
// 与 Room 的区别：Room 是「大厅」，可长期存在、成员可自由进出；
// GameSession 是「本局」，一次出发对应一个会话，连接信息在其中流转。
//
// 之所以把连接信息做成可查询状态而非一次性信令：成员可能错过推送
// （断线重连、推送丢失），必须能主动补齐，否则会永久卡在等待。
type GameSession struct {
	ID            string    `json:"session_id"`
	RoomCode      string    `json:"room_code"`
	TransportMode string    `json:"transport_mode"`
	HostAccountID int64     `json:"host_account_id"`
	HostAddress   string    `json:"host_address"`
	HostPort      int       `json:"host_port"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

// SessionStore 内存会话存储，按房间码索引。
//
// 一个房间同时只有一个活跃会话：重复 Create 直接覆盖，
// 避免旧会话的连接信息残留导致成员连到已废弃的地址。
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*GameSession
}

// NewSessionStore 创建会话存储。
func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]*GameSession)}
}

// Create 为房间创建新会话，覆盖同房间的旧会话。
func (s *SessionStore) Create(roomCode string, hostAccountID int64) *GameSession {
	session := &GameSession{
		ID:            newSessionID(),
		RoomCode:      roomCode,
		TransportMode: "lan",
		HostAccountID: hostAccountID,
		Status:        SessionStatusConnecting,
		CreatedAt:     time.Now(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[roomCode] = session

	return session
}

// Get 按房间码取会话。
func (s *SessionStore) Get(roomCode string) (*GameSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[roomCode]

	return session, ok
}

// SetReady 写入房主监听地址并置为 ready。会话不存在时返回 false。
func (s *SessionStore) SetReady(roomCode, address string, port int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[roomCode]
	if !ok {
		return false
	}

	session.HostAddress = address
	session.HostPort = port
	session.Status = SessionStatusReady

	return true
}

// SetStatus 更新会话状态。会话不存在时返回 false。
func (s *SessionStore) SetStatus(roomCode, status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[roomCode]
	if !ok {
		return false
	}

	session.Status = status

	return true
}

// Remove 删除会话。
func (s *SessionStore) Remove(roomCode string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, roomCode)
}

// newSessionID 生成 16 位十六进制会话 ID。
//
// 随机源不可用时退化为时间戳：ID 只用于标识本局，
// 生成失败就中断整个出发流程的代价远大于 ID 可预测。
func newSessionID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}

	return hex.EncodeToString(buf)
}
