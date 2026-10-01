// Package avatar 头像文件存储。
//
// 存本地目录而非对象存储：本期单机部署，本地盘够用，
// 也省掉一套云服务凭证管理。上多实例前需换成对象存储或共享卷 ——
// 否则 A 实例上传的头像在 B 实例上取不到。
package avatar

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MaxBytes 头像文件大小上限。
//
// 客户端已把图压到 512×512，正常在 200KB 以内；
// 留 2MB 余量给未压缩的 PNG。
const MaxBytes = 2 << 20

// ErrTooLarge 文件超过 MaxBytes。
var ErrTooLarge = errors.New("头像文件过大")

// ErrUnsupportedType 不是受支持的图片格式。
var ErrUnsupportedType = errors.New("头像仅支持 JPEG / PNG / WebP")

// Store 头像存储。
type Store struct {
	root string
}

// NewStore 创建存储并确保根目录存在。
func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("创建头像目录失败: %w", err)
	}

	return &Store{root: root}, nil
}

// Root 头像根目录的绝对路径。静态文件服务需要它。
func (s *Store) Root() string { return s.root }

// Save 保存头像，返回相对路径（如 "42/9f3c1a2b.jpg"）。
//
// 文件名用时间戳 + 随机后缀而非账号 ID：同一账号换头像时不会覆盖旧文件，
// 浏览器与 Unity 的图片缓存也不会拿到旧图。
func (s *Store) Save(accountID int64, filename string, data []byte) (string, error) {
	if len(data) > MaxBytes {
		return "", ErrTooLarge
	}

	ext, err := detectExt(data)
	if err != nil {
		return "", err
	}

	dir := strconv.FormatInt(accountID, 10)
	if err := os.MkdirAll(filepath.Join(s.root, dir), 0o755); err != nil {
		return "", fmt.Errorf("创建账号头像目录失败: %w", err)
	}

	name := fmt.Sprintf("%d_%d%s", time.Now().UnixNano(), os.Getpid(), ext)
	rel := path.Join(dir, name)

	if err := os.WriteFile(filepath.Join(s.root, rel), data, 0o644); err != nil {
		return "", fmt.Errorf("写入头像文件失败: %w", err)
	}

	return rel, nil
}

// Remove 删除头像文件。文件不存在不算错误 —— 调用方只关心「删干净了」。
func (s *Store) Remove(rel string) error {
	if rel == "" {
		return nil
	}

	// 只接受存储自己生成的相对路径，避免 "../" 之类的路径穿越。
	if !isSafeRel(rel) {
		return fmt.Errorf("非法的头像路径: %s", rel)
	}

	if err := os.Remove(filepath.Join(s.root, rel)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除头像文件失败: %w", err)
	}

	return nil
}

// detectExt 按文件头判断图片格式并返回扩展名。
//
// 不信任客户端给的文件名与 Content-Type：两者都能随便伪造，
// 而文件头是内容本身。这样也顺带挡掉了「上传 .php 伪装成图片」这类攻击。
func detectExt(data []byte) (string, error) {
	if len(data) == 0 {
		return "", ErrUnsupportedType
	}

	switch http.DetectContentType(data) {
	case "image/jpeg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	case "image/webp":
		return ".webp", nil
	default:
		return "", ErrUnsupportedType
	}
}

// isSafeRel 判断相对路径是否安全：不含 ".."、不以分隔符开头。
func isSafeRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return false
	}

	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return false
		}
	}

	return true
}

// ReadAll 读取上传流并限制大小。超出上限时返回 ErrTooLarge。
func ReadAll(r io.Reader) ([]byte, error) {
	// 多读 1 字节：能区分「刚好等于上限」与「超过上限」。
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取上传内容失败: %w", err)
	}

	if len(data) > MaxBytes {
		return nil, ErrTooLarge
	}

	return data, nil
}
