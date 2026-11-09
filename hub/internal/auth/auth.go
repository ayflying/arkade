// Package auth 实现登录、会话与鉴权。
//
// 职责边界：
//   - 只做认证（你是谁）与会话生命周期，权限判定交给 user 包；
//   - 对外暴露 Service，HTTP 层通过它拿当前用户，不直接读 Cookie；
//   - 令牌哈希、Cookie 名称、时效都在本包内统一，避免散落各处。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// 会话 Cookie 名称。使用 __Host- 前缀要求：Secure + Path=/ + 无 Domain，
// 这是浏览器给的强保证，可防止子域覆盖 Cookie。（本地 HTTP 开发时不带该前缀）
const (
	CookieName = "arkade_session"
	CookiePath = "/"
	// 会话有效期由配置注入，这里只定义默认值。
	defaultSessionTTL = 72 * time.Hour
)

// ErrInvalidCredentials 表示用户名或密码错误。
// 故意不区分「用户不存在」「密码错误」「账号禁用」，避免账号枚举。
var ErrInvalidCredentials = errors.New("用户名或密码错误")

// ErrSessionExpired 表示会话不存在或已过期。
var ErrSessionExpired = errors.New("登录已失效")

// Service 是认证服务。
type Service struct {
	db         *store.DB
	sessionTTL time.Duration
}

// NewService 构造认证服务。ttl 为 0 时使用默认值。
func NewService(db *store.DB, ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	return &Service{db: db, sessionTTL: ttl}
}

// LoginResult 是登录成功后的结果，token 需要由 HTTP 层写入 Cookie。
type LoginResult struct {
	User      user.User
	Token     string    // 明文令牌，只在这里出现一次，不再落库
	ExpiresAt time.Time // Cookie 过期时间
}

// Login 校验用户名密码并创建会话。
func (s *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	u, err := s.db.GetUserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		// 用户不存在时也执行一次哈希比对，让耗时与密码错误分支接近，
		// 减少通过响应时间探测「用户是否存在」的可能。
		_ = user.VerifyPassword(dummyHash, password)
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("查询用户失败: %w", err)
	}
	if u.Disabled {
		return LoginResult{}, ErrInvalidCredentials
	}
	if err := user.VerifyPassword(u.PasswordHash, password); err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}

	token, err := newToken()
	if err != nil {
		return LoginResult{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(s.sessionTTL)
	if err := s.db.CreateSession(ctx, hashToken(token), u.ID, now.Unix(), expires.Unix()); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{User: u, Token: token, ExpiresAt: expires}, nil
}

// Authenticate 用令牌换取当前用户。令牌无效或过期返回 ErrSessionExpired。
func (s *Service) Authenticate(ctx context.Context, token string) (user.User, error) {
	if token == "" {
		return user.User{}, ErrSessionExpired
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	uid, err := s.db.SessionUserID(ctx, hashToken(token), time.Now().Unix())
	if errors.Is(err, store.ErrNotFound) {
		return user.User{}, ErrSessionExpired
	}
	if err != nil {
		return user.User{}, fmt.Errorf("校验会话失败: %w", err)
	}

	u, err := s.db.GetUserByID(ctx, uid)
	if errors.Is(err, store.ErrNotFound) {
		return user.User{}, ErrSessionExpired
	}
	if err != nil {
		return user.User{}, fmt.Errorf("查询会话用户失败: %w", err)
	}
	// 账号在会话有效期内被禁用时，立即失效（不等待会话自然过期）。
	if u.Disabled {
		return user.User{}, ErrSessionExpired
	}
	return u, nil
}

// Logout 删除会话。令牌无效时不报错：退出登录应始终"成功"。
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.db.DeleteSession(ctx, hashToken(token))
}

// SessionTTL 返回会话有效期，供 HTTP 层设置 Cookie MaxAge。
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

// newToken 生成 32 字节随机令牌并做 base64url 编码。
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成会话令牌失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken 计算令牌的 SHA-256 十六进制摘要。
//
// 为什么用 SHA-256 而不是 bcrypt：令牌本身已是 256 位高熵随机值，
// 不存在被字典爆破的前提，bcrypt 的慢速特性在这里只会拖慢每个请求。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// dummyHash 是用于恒定耗时的假哈希（明文 "arkade-dummy-password" 的 bcrypt 结果占位）。
// 内容不重要，只要格式合法即可让 CompareHashAndPassword 走完整计算路径。
var dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
