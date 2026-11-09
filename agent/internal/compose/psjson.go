package compose

// psjson.go 是 compose ps 输出的 JSON 解析细节。
//
// 单独成文件的原因：这里要兼容三种输出形态，都是真实存在的版本差异，
// 和"状态归并"这条业务逻辑混在一起会让 status.go 难以阅读。
//
// 三种形态：
//  1. JSON 数组：[{"Name":"x","Service":"web",...}]        —— v2.6+
//  2. 每行一个 JSON 对象：{"Name":...}\n{"Name":...}       —— 部分 2.0~2.5
//  3. 空输出（项目未启动，或服务不存在）                     —— 任何版本

import (
	"encoding/json"
	"strings"
)

// parseJSONArray 解析 JSON 数组形态。
//
// 解析失败返回 nil 而不是错误：状态探测是"尽力而为"的信息，
// 解析不出来就报 unknown，不该让整个 compose.list 接口失败——
// 用户在界面上更需要看到"这个编排存在但状态未知"，而不是一个 500。
func parseJSONArray(raw string) []psEntry {
	var entries []psEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil
	}
	return entries
}

// parseJSONLines 解析"每行一个 JSON 对象"形态。
//
// 逐行解码而不是先拼成数组：某些版本会输出不带逗号、不带方括号的连续对象，
// 逐行处理天然容忍这种格式，也容忍前后夹带非 JSON 的告警行（跳过即可）。
func parseJSONLines(raw string) []psEntry {
	var entries []psEntry
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var e psEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}
