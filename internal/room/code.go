// Package room 房间模块：创建 / 加入 / 离开 / 列表 / 房主迁移。
//
// 职责边界：只做「房间层面」的事（成员进出、房主归属、状态流转）。
// 不碰任何游戏逻辑 —— 游戏层面的权威移交由客户端 NGO 完成。
package room

import (
	"crypto/rand"
	"math/big"
)

// codeAlphabet 房间码字符集：32 个字符，去掉了易混淆的 0/O/1/I/L。
//
// 玩家需要口头或手打分享房间码，混淆字符会显著提高输错率。
// 32 是 2 的幂，配合 crypto/rand 取模不会引入偏置。
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// codeLength 房间码长度。32^8 ≈ 1.1×10^12，空间足够大，实际几乎不会冲突。
const codeLength = 8

// GenerateCode 生成一个随机房间码。
//
// 用 crypto/rand 而非 math/rand：房间码是访问凭证（知道码就能进房），
// 可预测的伪随机序列会让攻击者能枚举出所有活跃房间。
func GenerateCode() (string, error) {
	code := make([]byte, codeLength)
	max := big.NewInt(int64(len(codeAlphabet)))

	for i := range code {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}

		code[i] = codeAlphabet[n.Int64()]
	}

	return string(code), nil
}

// IsValidCode 校验房间码格式：长度 8 且字符全在字符集内。
//
// 先做格式校验再查库，可以挡掉大量无意义的数据库查询。
func IsValidCode(code string) bool {
	if len(code) != codeLength {
		return false
	}

	for i := 0; i < len(code); i++ {
		if !isAlphabetChar(code[i]) {
			return false
		}
	}

	return true
}

func isAlphabetChar(c byte) bool {
	for i := 0; i < len(codeAlphabet); i++ {
		if codeAlphabet[i] == c {
			return true
		}
	}

	return false
}
