package httpx

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gogf/gf/v2/errors/gerror"
)

// loadSettingsKey persists a separate random encryption key, never deriving it
// from an Agent token or the database. Backups must preserve both DB and key.
func loadSettingsKey(databasePath, configuredPath string) ([]byte, error) {
	path := configuredPath
	if path == "" {
		path = databasePath + ".settings.key"
	}
	read := func() ([]byte, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, gerror.New("系统设置密钥必须是普通文件，不能使用符号链接")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0177 != 0 {
			return nil, gerror.New("系统设置密钥权限必须为0600或更严格")
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, gerror.Wrap(err, "读取系统设置密钥失败")
		}
		if len(key) != 32 {
			return nil, gerror.New("系统设置密钥必须为32字节，不会自动替换已有密钥")
		}
		return key, nil
	}
	key, err := read()
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, gerror.Wrap(err, "创建系统设置密钥目录失败")
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, gerror.Wrap(err, "生成系统设置密钥失败")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return read()
	}
	if err != nil {
		return nil, gerror.Wrap(err, "创建系统设置密钥失败")
	}
	if _, err = file.Write(key); err != nil {
		file.Close()
		return nil, gerror.Wrap(err, "保存系统设置密钥失败")
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return nil, gerror.Wrap(err, "同步系统设置密钥失败")
	}
	if err = file.Close(); err != nil {
		return nil, gerror.Wrap(err, "关闭系统设置密钥失败")
	}
	return key, nil
}
