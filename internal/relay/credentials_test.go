package relay

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"testing"
	"time"
)

// TestIssue_UsernameFormat 验证 username 格式为 "<过期时间戳>:<account_id>"。
func TestIssue_UsernameFormat(t *testing.T) {
	issuer := NewIssuer("test-secret", "tower.local", "127.0.0.1", 3478, 24*time.Hour)

	creds := issuer.Issue(1001)

	expiresAt, accountID, err := ParseUsername(creds.Username)
	if err != nil {
		t.Fatalf("解析 username 失败: %v", err)
	}

	if accountID != 1001 {
		t.Errorf("账号 ID 应为 1001，实际 %d", accountID)
	}

	// 过期时间应约等于 now + 24h（允许 5 秒误差）。
	expected := time.Now().Add(24 * time.Hour)
	diff := expected.Sub(expiresAt)
	if diff < -5*time.Second || diff > 5*time.Second {
		t.Errorf("过期时间偏差过大: %v", diff)
	}
}

// TestIssue_PasswordMatchesRFC5766 用独立实现重算 HMAC，验证与 coturn 的算法一致。
//
// 这是本模块最关键的测试：算法写错的话，coturn 会拒绝所有凭证，
// 而客户端只会看到「中继连不上」，很难定位到是签名问题。
func TestIssue_PasswordMatchesRFC5766(t *testing.T) {
	const secret = "dev-turn-shared-secret-change-me"

	issuer := NewIssuer(secret, "tower.local", "127.0.0.1", 3478, time.Hour)
	creds := issuer.Issue(42)

	// 独立重算：base64(HMAC-SHA1(secret, username))。
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(creds.Username))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if creds.Password != expected {
		t.Errorf("密码不匹配 RFC 5766 算法\n期望: %s\n实际: %s", expected, creds.Password)
	}
}

// TestVerify 验证自校验逻辑。
func TestVerify(t *testing.T) {
	issuer := NewIssuer("test-secret", "tower.local", "127.0.0.1", 3478, time.Hour)
	creds := issuer.Issue(7)

	if !issuer.Verify(creds.Username, creds.Password) {
		t.Error("正确凭证应校验通过")
	}

	if issuer.Verify(creds.Username, "wrong-password") {
		t.Error("错误密码不应通过")
	}

	if issuer.Verify("9999999999:7", creds.Password) {
		t.Error("篡改 username 后不应通过")
	}
}

// TestBuildResponse 验证响应结构。
func TestBuildResponse(t *testing.T) {
	issuer := NewIssuer("test-secret", "tower.local", "turn.example.com", 3478, 24*time.Hour)

	resp := issuer.BuildResponse(1001)

	if len(resp.ICEServers) != 2 {
		t.Fatalf("应有 2 个 ICE 服务器（STUN + TURN），实际 %d", len(resp.ICEServers))
	}

	// STUN 不需要凭证。
	stun := resp.ICEServers[0]
	if stun.URLs[0] != "stun:turn.example.com:3478" {
		t.Errorf("STUN 地址错误: %s", stun.URLs[0])
	}

	if stun.Username != "" || stun.Credential != "" {
		t.Error("STUN 不应携带凭证")
	}

	// TURN 需要凭证，且同时提供 UDP 与 TCP。
	turn := resp.ICEServers[1]
	if len(turn.URLs) != 2 {
		t.Fatalf("TURN 应提供 UDP 与 TCP 两个地址，实际 %d", len(turn.URLs))
	}

	if turn.URLs[0] != "turn:turn.example.com:3478?transport=udp" {
		t.Errorf("TURN UDP 地址错误: %s", turn.URLs[0])
	}

	if turn.URLs[1] != "turn:turn.example.com:3478?transport=tcp" {
		t.Errorf("TURN TCP 地址错误: %s", turn.URLs[1])
	}

	if turn.Username == "" || turn.Credential == "" {
		t.Error("TURN 必须携带凭证")
	}

	if resp.TTL != 86400 {
		t.Errorf("TTL 应为 86400，实际 %d", resp.TTL)
	}
}

// TestParseUsername_Invalid 验证非法输入。
func TestParseUsername_Invalid(t *testing.T) {
	cases := []string{"", "no-colon", "abc:123", "123:abc"}

	for _, c := range cases {
		if _, _, err := ParseUsername(c); err == nil {
			t.Errorf("输入 %q 应返回错误", c)
		}
	}
}

// TestIssue_DifferentAccountsDifferentCredentials 验证不同账号拿到不同凭证。
func TestIssue_DifferentAccountsDifferentCredentials(t *testing.T) {
	issuer := NewIssuer("test-secret", "tower.local", "127.0.0.1", 3478, time.Hour)

	a := issuer.Issue(1)
	b := issuer.Issue(2)

	if a.Username == b.Username {
		t.Error("不同账号的 username 不应相同")
	}

	if a.Password == b.Password {
		t.Error("不同账号的 password 不应相同")
	}
}
