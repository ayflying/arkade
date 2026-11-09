package config

import (
	"github.com/BurntSushi/toml"
)

// decodeTOML 是 TOML 解码的唯一入口。
//
// 单独包一层的原因：直接 toml.Decode 无法统一处理错误信息，
// 这里用 MetaData.Undecoded 检测拼错的键并明确报错，
// 避免用户把 auth.agent_token 写成 auth.agent_tokne 却毫无提示。
func decodeTOML(raw []byte, out *Config) error {
	meta, err := toml.Decode(string(raw), out)
	if err != nil {
		return err
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return &UnknownKeyError{Keys: undecoded}
	}
	return nil
}

// UnknownKeyError 表示配置文件里存在无法识别的键。
type UnknownKeyError struct {
	Keys []toml.Key
}

func (e *UnknownKeyError) Error() string {
	out := "配置中存在未知项: "
	for i, k := range e.Keys {
		if i > 0 {
			out += ", "
		}
		out += k.String()
	}
	return out
}
