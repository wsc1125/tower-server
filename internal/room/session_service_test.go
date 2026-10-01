package room

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// newTestService 构造只依赖内存的 Service。
//
// repo 留空：会话相关逻辑不落库，测试不该被数据库拖累。
func newTestService() *Service {
	return &Service{
		registry: NewRegistry(),
		sessions: NewSessionStore(),
		logger:   zerolog.Nop(),
	}
}

// seedRoom 在注册表里放一个房间，房主为 hostID 且已在成员表中。
//
// 直接构造 Room 而非走 Create：Create 需要数据库，而这里只关心
// 「注册表里有一个房主是谁的房间」这一前提。
func seedRoom(svc *Service, code string, hostID int64) *Room {
	host := &Member{
		AccountID:   hostID,
		DisplayName: "房主",
		JoinedAt:    time.Now(),
	}

	room := newRoom(code, host, "测试房间", maxPlayerLimit, true)
	svc.registry.Add(room)

	return room
}

func TestCreateSessionOnlyHost(t *testing.T) {
	svc := newTestService()
	seedRoom(svc, "ABCD2345", 42)

	if _, err := svc.CreateSession(context.Background(), "ABCD2345", 99); !errors.Is(err, ErrNotHost) {
		t.Fatalf("非房主创建应返回 ErrNotHost，实际 %v", err)
	}

	session, err := svc.CreateSession(context.Background(), "ABCD2345", 42)
	if err != nil {
		t.Fatalf("房主创建会话失败: %v", err)
	}

	if session.Status != SessionStatusConnecting {
		t.Fatalf("初始状态应为 connecting，实际 %q", session.Status)
	}

	if session.RoomCode != "ABCD2345" {
		t.Fatalf("RoomCode 应为 ABCD2345，实际 %q", session.RoomCode)
	}

	if session.HostAccountID != 42 {
		t.Fatalf("HostAccountID 应为 42，实际 %d", session.HostAccountID)
	}
}

func TestMarkSessionReadyOnlyHost(t *testing.T) {
	svc := newTestService()
	seedRoom(svc, "ABCD2345", 42)

	if _, err := svc.CreateSession(context.Background(), "ABCD2345", 42); err != nil {
		t.Fatalf("房主创建会话失败: %v", err)
	}

	if _, err := svc.MarkSessionReady("ABCD2345", 99, "192.168.1.10", 7777); !errors.Is(err, ErrNotHost) {
		t.Fatalf("非房主标记应返回 ErrNotHost，实际 %v", err)
	}

	session, err := svc.MarkSessionReady("ABCD2345", 42, "192.168.1.10", 7777)
	if err != nil {
		t.Fatalf("房主标记会话失败: %v", err)
	}

	if session.Status != SessionStatusReady {
		t.Fatalf("状态应为 ready，实际 %q", session.Status)
	}

	if session.HostAddress != "192.168.1.10" {
		t.Fatalf("HostAddress 应为 192.168.1.10，实际 %q", session.HostAddress)
	}

	if session.HostPort != 7777 {
		t.Fatalf("HostPort 应为 7777，实际 %d", session.HostPort)
	}
}

func TestGetSessionMissing(t *testing.T) {
	svc := newTestService()
	seedRoom(svc, "ABCD2345", 42)

	if _, ok := svc.GetSession("ABCD2345"); ok {
		t.Fatal("未创建会话时 GetSession 应返回 false")
	}
}

func TestCloseSessionOnlyHost(t *testing.T) {
	svc := newTestService()
	seedRoom(svc, "ABCD2345", 42)

	if _, err := svc.CreateSession(context.Background(), "ABCD2345", 42); err != nil {
		t.Fatalf("房主创建会话失败: %v", err)
	}

	if err := svc.CloseSession("ABCD2345", 99); !errors.Is(err, ErrNotHost) {
		t.Fatalf("非房主关闭应返回 ErrNotHost，实际 %v", err)
	}

	if err := svc.CloseSession("ABCD2345", 42); err != nil {
		t.Fatalf("房主关闭会话失败: %v", err)
	}

	if _, ok := svc.GetSession("ABCD2345"); ok {
		t.Fatal("关闭后 GetSession 应返回 false")
	}
}
