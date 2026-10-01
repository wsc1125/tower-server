package account

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tower/server/internal/auth"
	"github.com/tower/server/internal/avatar"
)

// 用户名规则：3~20 位，字母 / 数字 / 下划线。
// 限制字符集是为了避免「看起来一样但实际不同」的用户名（如全角字符、零宽字符）被用来冒充他人。
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)

// ErrInvalidUsername 用户名格式不合法。
var ErrInvalidUsername = errors.New("用户名需为 3~20 位字母、数字或下划线")

// ErrInvalidPassword 密码格式不合法。
var ErrInvalidPassword = errors.New("密码长度需为 8~72 位")

// ErrInvalidDisplayName 昵称格式不合法。
var ErrInvalidDisplayName = errors.New("昵称不能为空，且不能超过 32 个字符")

// ErrInvalidAvatar 头像文件不合法。
var ErrInvalidAvatar = errors.New("头像仅支持 2MB 以内的 JPEG / PNG / WebP 图片")

// ErrInvalidCredentials 用户名或密码错误。
//
// 登录失败统一返回这一个错误，不区分「用户不存在」与「密码错误」——
// 否则接口会变成用户名枚举器。
var ErrInvalidCredentials = errors.New("用户名或密码错误")

// Service 账号业务逻辑。
type Service struct {
	repo    *Repository
	hasher  *auth.PasswordHasher
	issuer  *auth.TokenIssuer
	avatars *avatar.Store
	baseURL string
}

// NewService 创建账号服务。
//
// avatars 与 baseURL 用于头像：前者落盘，后者把相对路径拼成客户端可直接加载的 URL。
func NewService(repo *Repository, hasher *auth.PasswordHasher, issuer *auth.TokenIssuer,
	avatars *avatar.Store, baseURL string) *Service {
	return &Service{
		repo:    repo,
		hasher:  hasher,
		issuer:  issuer,
		avatars: avatars,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// Register 注册新账号并直接签发令牌（注册即登录，省一次往返）。
//
// avatarData 为 nil 表示本次注册未上传头像。
func (s *Service) Register(ctx context.Context, username, password, displayName string, avatarData []byte) (*Account, auth.TokenPair, error) {
	username = strings.TrimSpace(username)
	displayName = strings.TrimSpace(displayName)

	if !usernamePattern.MatchString(username) {
		return nil, auth.TokenPair{}, ErrInvalidUsername
	}

	if err := validatePassword(password); err != nil {
		return nil, auth.TokenPair{}, err
	}

	// 昵称必填：不再回退到用户名，避免玩家在游戏里看到一串账号 ID。
	if displayName == "" || utf8.RuneCountInString(displayName) > 32 {
		return nil, auth.TokenPair{}, ErrInvalidDisplayName
	}

	hash, err := s.hasher.Hash(password)
	if err != nil {
		return nil, auth.TokenPair{}, err
	}

	acc, err := s.repo.Create(ctx, username, hash, displayName, "")
	if err != nil {
		return nil, auth.TokenPair{}, err
	}

	// 头像在账号创建之后落盘：文件名要带账号 ID，且此时才确定账号真的建成了。
	// 落盘失败不回滚账号 —— 头像缺失是小事，让玩家因为一张图注册失败是大事。
	if len(avatarData) > 0 {
		if err := s.attachAvatar(ctx, acc, avatarData); err != nil {
			return nil, auth.TokenPair{}, err
		}
	}

	tokens, err := s.issuer.Issue(acc.ID)
	if err != nil {
		return nil, auth.TokenPair{}, err
	}

	return acc, tokens, nil
}

// attachAvatar 保存头像文件并把相对路径写回账号。
func (s *Service) attachAvatar(ctx context.Context, acc *Account, data []byte) error {
	if s.avatars == nil {
		return ErrInvalidAvatar
	}

	rel, err := s.avatars.Save(acc.ID, "", data)
	if err != nil {
		if errors.Is(err, avatar.ErrTooLarge) || errors.Is(err, avatar.ErrUnsupportedType) {
			return ErrInvalidAvatar
		}

		return err
	}

	if err := s.repo.UpdateAvatar(ctx, acc.ID, rel); err != nil {
		// 数据库没记上，文件就是孤儿，顺手删掉，避免磁盘越攒越多。
		_ = s.avatars.Remove(rel)
		return err
	}

	acc.AvatarPath = rel
	acc.AvatarURL = s.avatarURL(rel)

	return nil
}

// avatarURL 把相对路径拼成客户端可加载的完整 URL。
func (s *Service) avatarURL(rel string) string {
	if rel == "" || s.baseURL == "" {
		return ""
	}

	return s.baseURL + "/avatars/" + rel
}

// Decorate 补齐账号的派生字段（当前只有头像 URL）。
//
// 从数据库读出来的账号只有 avatar_path，URL 依赖部署时的 baseURL，
// 因此统一在出站前补一次，而不是存进库里。
func (s *Service) Decorate(acc *Account) {
	if acc == nil {
		return
	}

	acc.AvatarURL = s.avatarURL(acc.AvatarPath)
}

// Login 校验凭证并签发令牌。
func (s *Service) Login(ctx context.Context, username, password string) (*Account, auth.TokenPair, error) {
	username = strings.TrimSpace(username)

	acc, err := s.repo.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// 用户不存在时也跑一次 bcrypt，让两条路径耗时相近，避免时序枚举。
			_ = s.hasher.VerifyDummy(password)
			return nil, auth.TokenPair{}, ErrInvalidCredentials
		}

		return nil, auth.TokenPair{}, err
	}

	if err := s.hasher.Verify(acc.PasswordHash, password); err != nil {
		if errors.Is(err, auth.ErrPasswordMismatch) {
			return nil, auth.TokenPair{}, ErrInvalidCredentials
		}

		return nil, auth.TokenPair{}, err
	}

	tokens, err := s.issuer.Issue(acc.ID)
	if err != nil {
		return nil, auth.TokenPair{}, err
	}

	// 登录时间写失败不影响登录本身，忽略即可。
	_ = s.repo.TouchLastLogin(ctx, acc.ID)

	return acc, tokens, nil
}

// Refresh 用 refresh token 换取新的令牌对。
//
// 这里不做 refresh token 轮转（rotation）与吊销 —— 本期无状态化优先，
// 代价是 refresh token 在有效期内无法提前失效。上真实服务器前需补一张
// refresh_tokens 表来支持吊销。
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*Account, auth.TokenPair, error) {
	claims, err := s.issuer.Parse(refreshToken, auth.TokenTypeRefresh)
	if err != nil {
		return nil, auth.TokenPair{}, err
	}

	acc, err := s.repo.FindByID(ctx, claims.AccountID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// 账号已注销，令牌随之作废。
			return nil, auth.TokenPair{}, auth.ErrTokenInvalid
		}

		return nil, auth.TokenPair{}, err
	}

	tokens, err := s.issuer.Issue(acc.ID)
	if err != nil {
		return nil, auth.TokenPair{}, err
	}

	return acc, tokens, nil
}

// Profile 按 ID 取账号资料，供 /me 使用。
func (s *Service) Profile(ctx context.Context, accountID int64) (*Account, error) {
	return s.repo.FindByID(ctx, accountID)
}

// validatePassword 校验密码长度。
//
// 上限 72 是 bcrypt 的硬限制（超出部分会被静默截断），
// 下限 8 是弱密码的基本门槛。
func validatePassword(password string) error {
	length := len(password)
	if length < 8 || length > 72 {
		return ErrInvalidPassword
	}

	return nil
}
