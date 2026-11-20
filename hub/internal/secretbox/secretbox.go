// Package secretbox 提供 Hub 落库前的对称加密。
//
// 为什么要有这一层：有些凭据必须在服务端"可再次读出"（例如尚未接入的
// 服务器密钥要能反复展示给管理员），而数据库只存摘要就无法满足。
// 这类值用主密钥加密后再落库，主密钥放在独立的密钥文件里，
// 单独拖走数据库拿不到明文。
//
// 与 settings / monitor 里各自的 AES-GCM 实现同源；这里抽成通用小工具，
// 新代码统一用它，避免第三份几乎相同的加解密代码各自漂移。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/gogf/gf/v2/errors/gerror"
)

// prefix 是密文格式版本号。换算法或换 AAD 约定时递增，
// 读到旧前缀可以给出可理解的错误，而不是解密失败的一团乱码。
const prefix = "v1:"

// ErrInvalid 表示密文格式不对或无法解密。
//
// 故意不把原始密文或底层错误暴露给调用方：解密失败最常见的原因是
// 主密钥被换过，这时需要的是"恢复原主密钥"这种可操作提示，
// 而不是一段 base64。
var ErrInvalid = errors.New("密文无效或与当前主密钥不匹配")

// Box 是加解密器。零值不可用，必须用 New 构造。
type Box struct{ aead cipher.AEAD }

// New 用 32 字节主密钥构造加密器。
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, gerror.New("加密主密钥必须为32字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, gerror.Wrap(err, "初始化加密算法失败")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, gerror.Wrap(err, "初始化加密模式失败")
	}
	return &Box{aead: aead}, nil
}

// Seal 加密明文。
//
// purpose 是用途标签，会作为附加认证数据参与校验：同一把主密钥下，
// 为 A 用途加密的密文不能被当成 B 用途解开，避免实现者把不同字段的
// 密文互相搬运。
func (b *Box) Seal(purpose, plain string) (string, error) {
	if b == nil {
		return "", gerror.New("加密器未初始化")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", gerror.Wrap(err, "生成随机数失败")
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plain), []byte(purpose))
	return prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Open 解密 Seal 产生的密文。
func (b *Box) Open(purpose, sealed string) (string, error) {
	if b == nil {
		return "", gerror.New("加密器未初始化")
	}
	if len(sealed) <= len(prefix) || sealed[:len(prefix)] != prefix {
		return "", ErrInvalid
	}
	raw, err := base64.RawStdEncoding.DecodeString(sealed[len(prefix):])
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", ErrInvalid
	}
	n := b.aead.NonceSize()
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(purpose))
	if err != nil {
		return "", ErrInvalid
	}
	return string(plain), nil
}
