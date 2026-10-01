// Command sigtest 是信令模块的端到端验证客户端。
//
// 模拟两个玩家：A 建房并连信令，B 加入并连信令，
// 验证房间状态下发、信令转发、断线房主迁移。
//
// 用法：go run ./cmd/sigtest
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const baseURL = "http://localhost:8080/api/v1"

var failures int

func main() {
	a := login("hostuser", "HostPlayer")
	b := login("guestuser", "GuestPlayer")

	// A 建房。
	room := post(a, "/rooms", map[string]any{"name": "信令测试", "max_players": 4})
	code := room["room_code"].(string)
	check("建房成功", code != "", "room_code="+code)

	// B 加入。
	post(b, "/rooms/"+code+"/join", nil)
	check("B 加入房间", true, "")

	// 双方连信令。
	wsA := dial(code, a)
	defer wsA.Close()
	wsB := dial(code, b)
	defer wsB.Close()

	// A 应收到 room_state（此时只有 A 自己）。
	stateA := readUntil(wsA, "room_state", 3*time.Second)
	check("A 收到 room_state", stateA != nil, "")
	if stateA != nil {
		members := stateA["payload"].(map[string]any)["members"].([]any)
		check("A 的初始 room_state 只有自己", len(members) == 1, fmt.Sprintf("实际 %d", len(members)))
	}

	// B 连上后，A 应收到 member_joined。
	joined := readUntil(wsA, "member_joined", 3*time.Second)
	check("A 收到 member_joined", joined != nil, "")

	// B 也应收到自己的 room_state（此时房内 2 人）。
	stateB := readUntil(wsB, "room_state", 3*time.Second)
	check("B 收到 room_state", stateB != nil, "")
	if stateB != nil {
		members := stateB["payload"].(map[string]any)["members"].([]any)
		check("B 看到 2 名成员", len(members) == 2, fmt.Sprintf("实际 %d", len(members)))
	}

	// A 向 B 转发信令数据。
	send(wsA, "signal", map[string]any{
		"target_account_id": b.accountID,
		"data":              `{"sdp":"v=0 fake offer","type":"offer"}`,
	})

	fwd := readUntil(wsB, "signal", 3*time.Second)
	check("B 收到转发的 signal", fwd != nil, "")
	if fwd != nil {
		p := fwd["payload"].(map[string]any)
		check("转发来源是 A", int64(p["from_account_id"].(float64)) == a.accountID,
			fmt.Sprintf("from=%v", p["from_account_id"]))
		check("data 原样透传", strings.Contains(p["data"].(string), "fake offer"), "")
	}

	// 转发给不存在的目标 → 应收到 error。
	send(wsA, "signal", map[string]any{"target_account_id": 999999, "data": "x"})
	errMsg := readUntil(wsA, "error", 3*time.Second)
	check("目标不存在时返回 error", errMsg != nil, "")

	// ping/pong 保活。
	send(wsA, "ping", map[string]any{})
	pong := readUntil(wsA, "pong", 3*time.Second)
	check("ping 得到 pong", pong != nil, "")

	// B 尝试 start_game（非房主）→ error。
	send(wsB, "start_game", map[string]any{})
	notHost := readUntil(wsB, "error", 3*time.Second)
	check("非房主 start_game 被拒", notHost != nil, "")

	// A（房主）start_game → 双方都收到 game_starting。
	send(wsA, "start_game", map[string]any{})
	gsA := readUntil(wsA, "game_starting", 3*time.Second)
	gsB := readUntil(wsB, "game_starting", 3*time.Second)
	check("A 收到 game_starting", gsA != nil, "")
	check("B 收到 game_starting", gsB != nil, "")

	// 关键：A 断开 → B 应收到 host_changed。
	wsA.Close()
	hc := readUntil(wsB, "host_changed", 5*time.Second)
	check("A 断线后 B 收到 host_changed", hc != nil, "")
	if hc != nil {
		p := hc["payload"].(map[string]any)
		check("新房主是 B", int64(p["new_host_account_id"].(float64)) == b.accountID,
			fmt.Sprintf("new_host=%v", p["new_host_account_id"]))
	}

	// 清理：B 关闭房间。
	wsB.Close()
	time.Sleep(300 * time.Millisecond)

	fmt.Printf("\n===== 结果：%d 项失败 =====\n", failures)
	if failures > 0 {
		os.Exit(1)
	}
}

// ---- 测试辅助 ----

type account struct {
	token     string
	accountID int64
}

func login(username, displayName string) account {
	body := map[string]any{"username": username, "password": "passw0rd123", "display_name": displayName}
	resp, err := http.Post(baseURL+"/auth/register", "application/json", jsonBody(body))
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)

	if resp.StatusCode != http.StatusCreated {
		// 已存在则登录。
		resp2, err := http.Post(baseURL+"/auth/login", "application/json", jsonBody(map[string]any{
			"username": username, "password": "passw0rd123",
		}))
		if err != nil {
			panic(err)
		}
		defer resp2.Body.Close()
		out = map[string]any{}
		_ = json.NewDecoder(resp2.Body).Decode(&out)
	}

	acc := out["account"].(map[string]any)
	tokens := out["tokens"].(map[string]any)

	return account{
		token:     tokens["access_token"].(string),
		accountID: int64(acc["account_id"].(float64)),
	}
}

func post(a account, path string, body map[string]any) map[string]any {
	req, _ := http.NewRequest(http.MethodPost, baseURL+path, jsonBody(body))
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)

	return out
}

func dial(roomCode string, a account) *websocket.Conn {
	url := fmt.Sprintf("ws://localhost:8080/api/v1/signal?room_code=%s&token=%s", roomCode, a.token)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		panic(fmt.Sprintf("连接信令失败: %v", err))
	}

	return conn
}

func send(conn *websocket.Conn, msgType string, payload map[string]any) {
	env := map[string]any{"type": msgType}
	if payload != nil {
		env["payload"] = payload
	}

	_ = conn.WriteJSON(env)
}

// readUntil 读取消息直到遇到指定类型，超时返回 nil。
func readUntil(conn *websocket.Conn, msgType string, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)

		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return nil
		}

		if msg["type"] == msgType {
			return msg
		}
	}

	return nil
}

func check(name string, ok bool, detail string) {
	if ok {
		fmt.Printf("  PASS  %s\n", name)
		return
	}

	failures++
	if detail != "" {
		fmt.Printf("  FAIL  %s  (%s)\n", name, detail)
	} else {
		fmt.Printf("  FAIL  %s\n", name)
	}
}

func jsonBody(v any) *strings.Reader {
	raw, _ := json.Marshal(v)

	return strings.NewReader(string(raw))
}
