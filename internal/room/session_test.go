package room

import "testing"

func TestSessionStoreCreateAndGet(t *testing.T) {
	store := NewSessionStore()

	s := store.Create("ABC123", 42)
	if s == nil {
		t.Fatal("Create 返回 nil")
	}

	if s.ID == "" {
		t.Fatal("session ID 为空")
	}

	if s.Status != SessionStatusConnecting {
		t.Fatalf("初始状态应为 connecting，实际 %q", s.Status)
	}

	got, ok := store.Get("ABC123")
	if !ok {
		t.Fatal("Get 未查到刚创建的会话")
	}

	if got.ID != s.ID {
		t.Fatalf("Get 返回的 ID 不一致：期望 %q，实际 %q", s.ID, got.ID)
	}
}

func TestSessionStoreSetReady(t *testing.T) {
	store := NewSessionStore()
	store.Create("ABC123", 42)

	if !store.SetReady("ABC123", "192.168.1.10", 7777) {
		t.Fatal("SetReady 应返回 true")
	}

	got, ok := store.Get("ABC123")
	if !ok {
		t.Fatal("Get 未查到会话")
	}

	if got.Status != SessionStatusReady {
		t.Fatalf("状态应为 ready，实际 %q", got.Status)
	}

	if got.HostAddress != "192.168.1.10" {
		t.Fatalf("HostAddress 应为 192.168.1.10，实际 %q", got.HostAddress)
	}

	if got.HostPort != 7777 {
		t.Fatalf("HostPort 应为 7777，实际 %d", got.HostPort)
	}
}

func TestSessionStoreSetReadyMissing(t *testing.T) {
	store := NewSessionStore()

	if store.SetReady("NOPE", "192.168.1.10", 7777) {
		t.Fatal("会话不存在时 SetReady 应返回 false")
	}
}

func TestSessionStoreRemove(t *testing.T) {
	store := NewSessionStore()
	store.Create("ABC123", 42)

	store.Remove("ABC123")

	if _, ok := store.Get("ABC123"); ok {
		t.Fatal("Remove 后 Get 应返回 false")
	}
}

func TestSessionStoreCreateReplaces(t *testing.T) {
	store := NewSessionStore()

	first := store.Create("ABC123", 42)
	second := store.Create("ABC123", 42)

	if first.ID == second.ID {
		t.Fatalf("同房间重复 Create 应生成不同 ID，实际都是 %q", first.ID)
	}

	got, ok := store.Get("ABC123")
	if !ok {
		t.Fatal("Get 未查到会话")
	}

	if got.ID != second.ID {
		t.Fatalf("Get 应返回最新会话 %q，实际 %q", second.ID, got.ID)
	}
}
