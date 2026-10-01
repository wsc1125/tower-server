// Package relay 中继模块：生成 coturn 的临时凭证。
//
// 采用 TURN REST API 机制（RFC 5766 附录 / draft-uberti-behave-turn-rest-00）：
//
//	username   = <过期时间戳>:<account_id>
//	password   = base64(HMAC-SHA1(shared_secret, username))
//
// coturn 侧配置 --use-auth-secret --static-auth-secret=<同一个密钥>，
// 收到请求时用同样的算法重算 password 并比对。
package relay

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

// Credentials 一组临时凭证。
type Credentials struct {
	// Username 格式为 "<过期时间戳>:<account_id>"。
	Username string

	// Password base64(HMAC-SHA1(sharedSecret, username))。
	Password string

	// ExpiresAt 过期时间点。
	ExpiresAt time.Time
}

// ICEServer ICE 服务器配置。字段名与 WebRTC 的 RTCIceServer 对齐，
// 客户端可直接反序列化使用。
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Response 凭证接口的响应体。
type Response struct {
	ICEServers []ICEServer `json:"ice_servers"`
	TTL        int64       `json:"ttl"`
}

// Issuer 临时凭证签发器。
type Issuer struct {
	sharedSecret string
	realm        string
	host         string
	port         int
	ttl          time.Duration
}

// NewIssuer 创建签发器。
func NewIssuer(sharedSecret, realm, host string, port int, ttl time.Duration) *Issuer {
	return &Issuer{
		sharedSecret: sharedSecret,
		realm:        realm,
		host:         host,
		port:         port,
		ttl:          ttl,
	}
}

// Issue 为指定账号签发临时凭证。
func (i *Issuer) Issue(accountID int64) Credentials {
	return i.IssueWithTTL(accountID, i.ttl)
}

// IssueWithTTL 与 Issue 相同，但可覆盖有效期。
//
// 单独一个方法而非改 Issue 签名：Issue 是既有调用方与测试依赖的稳定入口，
// 加参数会波及所有调用点。
func (i *Issuer) IssueWithTTL(accountID int64, ttl time.Duration) Credentials {
	expiresAt := time.Now().Add(ttl)
	username := fmt.Sprintf("%d:%d", expiresAt.Unix(), accountID)

	return Credentials{
		Username:  username,
		Password:  i.sign(username),
		ExpiresAt: expiresAt,
	}
}

// sign 计算 HMAC-SHA1 并 base64 编码。
//
// 用 SHA1 而非更现代的哈希：这是 coturn 的既定协议，
// 客户端与 coturn 都按此实现，换算法会直接不兼容。
// 此处 HMAC 的用途是「凭证校验」而非「抗碰撞」，SHA1 的弱点不构成实际风险。
func (i *Issuer) sign(username string) string {
	mac := hmac.New(sha1.New, []byte(i.sharedSecret))
	mac.Write([]byte(username))

	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// BuildResponse 构造完整的 ICE 服务器列表。
//
// 同时下发 STUN 与 TURN：
//   - STUN 用于探测公网地址（打洞的前提），不需要凭证
//   - TURN 用于打洞失败时中继，需要临时凭证
//
// 客户端应优先尝试直连，失败才回退中继 —— 中继流量是真实成本。
func (i *Issuer) BuildResponse(accountID int64) Response {
	return i.BuildResponseWithTTL(accountID, i.ttl)
}

// BuildResponseWithTTL 与 BuildResponse 相同，但可覆盖凭证有效期。
func (i *Issuer) BuildResponseWithTTL(accountID int64, ttl time.Duration) Response {
	creds := i.IssueWithTTL(accountID, ttl)
	hostPort := fmt.Sprintf("%s:%d", i.host, i.port)

	return Response{
		ICEServers: []ICEServer{
			{
				URLs: []string{"stun:" + hostPort},
			},
			{
				URLs: []string{
					"turn:" + hostPort + "?transport=udp",
					"turn:" + hostPort + "?transport=tcp",
				},
				Username:   creds.Username,
				Credential: creds.Password,
			},
		},
		TTL: int64(ttl.Seconds()),
	}
}

// Realm coturn 的 realm。
func (i *Issuer) Realm() string { return i.realm }

// Verify 校验一组凭证是否有效。仅用于测试与排查，生产路径由 coturn 自行校验。
func (i *Issuer) Verify(username, password string) bool {
	expected := i.sign(username)

	// 用 hmac.Equal 做常量时间比较，避免时序侧信道。
	return hmac.Equal([]byte(expected), []byte(password))
}

// ParseUsername 解析 username 中的过期时间戳与账号 ID。
func ParseUsername(username string) (expiresAt time.Time, accountID int64, err error) {
	sep := -1
	for idx := 0; idx < len(username); idx++ {
		if username[idx] == ':' {
			sep = idx
			break
		}
	}

	if sep < 0 {
		return time.Time{}, 0, fmt.Errorf("username 格式错误，缺少分隔符")
	}

	ts, err := strconv.ParseInt(username[:sep], 10, 64)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("username 时间戳解析失败: %w", err)
	}

	id, err := strconv.ParseInt(username[sep+1:], 10, 64)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("username 账号 ID 解析失败: %w", err)
	}

	return time.Unix(ts, 0), id, nil
}
