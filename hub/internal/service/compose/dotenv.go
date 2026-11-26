package compose

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gogf/gf/v2/errors/gerror"
)

// MergeEnv replaces dotenv values with literals and appends missing keys. Untouched
// statements are copied verbatim; a replacement keeps its assignment prefix and
// trailing whitespace/comment. Duplicate keys and unsupported statement syntax
// are rejected rather than guessing where to write. On error no partial output
// or changed keys are returned.
func MergeEnv(existing string, updates map[string]string) (string, []string, error) {
	if len(updates) == 0 {
		return existing, nil, nil
	}
	if err := ValidateEnv(existing); err != nil {
		return "", nil, err
	}
	if !utf8.ValidString(existing) {
		return "", nil, gerror.New("环境文件不是有效的 UTF-8 文本")
	}
	keys := make([]string, 0, len(updates))
	for key, value := range updates {
		if !mergeEnvKey(key) {
			return "", nil, gerror.New("环境变量名不合法：仅允许字母、数字和下划线，且不能以数字开头")
		}
		if err := ValidateEnv(value); err != nil {
			return "", nil, err
		}
		if !utf8.ValidString(value) {
			return "", nil, gerror.New("环境变量值不是有效的 UTF-8 文本")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	const bom = "\ufeff"
	start := 0
	if strings.HasPrefix(existing, bom) {
		start = len(bom)
	}
	var (
		merged  strings.Builder
		changed []string
		seen    = make(map[string]bool)
		copied  = 0
		line    = 1
	)
	for pos := start; pos < len(existing); {
		record, ok := scanMergeEnvRecord(existing, pos)
		if !ok {
			return "", nil, gerror.Newf("环境文件第 %d 行不是受支持的 dotenv 语法", line)
		}
		if record.key != "" {
			if seen[record.key] {
				return "", nil, gerror.Newf("环境文件第 %d 行重复定义环境变量", line)
			}
			seen[record.key] = true
			if update, requested := updates[record.key]; requested && !record.equals(existing, update) {
				merged.WriteString(existing[copied:record.valueStart])
				if record.inherited {
					merged.WriteByte('=')
				}
				merged.WriteString(quoteMergeEnvValue(update))
				if err := ValidateEnv(merged.String()); err != nil {
					return "", nil, err
				}
				copied = record.valueEnd
				changed = append(changed, record.key)
			}
		}
		line += strings.Count(existing[pos:record.next], "\n")
		pos = record.next
	}
	merged.WriteString(existing[copied:])

	newline := "\n"
	if first := strings.IndexByte(existing, '\n'); first > 0 && existing[first-1] == '\r' {
		newline = "\r\n"
	}
	needsSeparator := len(existing) > start && !strings.HasSuffix(existing, "\n")
	for _, key := range keys {
		if seen[key] {
			continue
		}
		if needsSeparator {
			merged.WriteString(newline)
			needsSeparator = false
		}
		merged.WriteString(key)
		merged.WriteByte('=')
		merged.WriteString(quoteMergeEnvValue(updates[key]))
		merged.WriteString(newline)
		if err := ValidateEnv(merged.String()); err != nil {
			return "", nil, err
		}
		changed = append(changed, key)
	}
	if len(changed) == 0 {
		return existing, nil, nil
	}
	if err := ValidateEnv(merged.String()); err != nil {
		return "", nil, err
	}
	sort.Strings(changed)
	return merged.String(), changed, nil
}

func mergeEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

type mergeEnvRecord struct {
	key                  string
	valueStart, valueEnd int
	next                 int
	inherited            bool
	quote                byte
}

// scanMergeEnvRecord finds raw byte spans, not resolved values. In particular,
// newlines inside either kind of quote never start a new assignment.
func scanMergeEnvRecord(src string, pos int) (mergeEnvRecord, bool) {
	lineEnd, next := mergeEnvLineEnd(src, pos)
	line := strings.TrimLeftFunc(src[pos:lineEnd], unicode.IsSpace)
	record := mergeEnvRecord{next: next}
	if line == "" || line[0] == '#' {
		return record, true
	}
	if len(line) > len("export") && strings.HasPrefix(line, "export") && strings.ContainsRune(" \t\r\f", rune(line[len("export")])) {
		line = strings.TrimLeftFunc(line[len("export"):], mergeEnvSpace)
	}
	key, _, assigned := strings.Cut(line, "=")
	if colon := strings.IndexByte(key, ':'); colon >= 0 {
		key, assigned = key[:colon], true
	}
	record.key = strings.TrimRightFunc(key, unicode.IsSpace)
	if !mergeEnvKey(record.key) {
		return mergeEnvRecord{}, false
	}
	start := lineEnd - len(line)
	if !assigned {
		// compose-go recognizes bare inherited keys at a newline delimiter;
		// a bare final word at EOF is not interpreted the same way.
		if next == lineEnd || record.key == "export" {
			return mergeEnvRecord{}, false
		}
		record.inherited = true
		record.valueStart = start + len(record.key)
		record.valueEnd = record.valueStart
		return record, true
	}
	value := strings.TrimLeftFunc(line[len(key)+1:], mergeEnvSpace)
	record.valueStart = lineEnd - len(value)
	if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
		record.quote = value[0]
		for i := record.valueStart + 1; i < len(src); i++ {
			switch src[i] {
			case '\\':
				i++
			case record.quote:
				record.valueEnd = i + 1
				end, next := mergeEnvLineEnd(src, i+1)
				tail := strings.TrimLeftFunc(src[i+1:end], unicode.IsSpace)
				if tail != "" && tail[0] != '#' {
					return mergeEnvRecord{}, false
				}
				record.next = next
				return record, true
			}
		}
		return mergeEnvRecord{}, false
	}
	// Match Compose's unquoted inline comment delimiter: a literal space then #,
	// after skipping the whitespace immediately following the assignment separator.
	value, _, _ = strings.Cut(value, " #")
	record.valueEnd = record.valueStart + len(strings.TrimRightFunc(value, unicode.IsSpace))
	return record, true
}

func mergeEnvLineEnd(src string, start int) (end, next int) {
	if i := strings.IndexByte(src[start:], '\n'); i >= 0 {
		end = start + i
		next = end + 1
		if end > start && src[end-1] == '\r' {
			end--
		}
		return end, next
	}
	return len(src), len(src)
}

// Horizontal whitespace accepted by compose-go's dotenv parser after separators.
func mergeEnvSpace(r rune) bool {
	switch r {
	case '\t', '\v', '\f', '\r', ' ', '\u0085', '\u00a0':
		return true
	}
	return false
}

// Merge comparison deliberately does not resolve dynamic values from the host or
// the existing file. An update replaces an expression with a literal even if it
// might currently happen to resolve to that literal.
func (r mergeEnvRecord) equals(src, update string) bool {
	if r.inherited {
		return false
	}
	value := src[r.valueStart:r.valueEnd]
	if r.quote != 0 {
		value = unquoteMergeEnvValue(value, r.quote)
		if r.quote == '\'' {
			return value == update
		}
		value = mergeEnvDoubleEscape.ReplaceAllStringFunc(value, func(escape string) string {
			if escape == `\$` {
				return "$$"
			}
			encoded := escape
			if strings.HasPrefix(escape, `\0`) {
				encoded = `\` + escape[2:]
			}
			decoded, _, _, err := strconv.UnquoteChar(encoded, '"')
			if err == nil {
				return string(decoded)
			}
			return escape
		})
	}
	var literal strings.Builder
	for {
		prefix, rest, found := strings.Cut(value, "$")
		literal.WriteString(prefix)
		if !found {
			return literal.String() == update
		}
		literal.WriteByte('$')
		if strings.HasPrefix(rest, "$") {
			rest = rest[1:]
		} else if len(rest) > 0 && (rest[0] == '{' || mergeEnvKey(rest[:1])) {
			return false
		}
		value = rest
	}
}

var mergeEnvDoubleEscape = regexp.MustCompile(`\\(?:[abfncrtv$"\\]|0\d{0,3})`)

func unquoteMergeEnvValue(quoted string, quote byte) string {
	raw := quoted[1 : len(quoted)-1]
	var value strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) {
			if raw[i+1] == quote {
				i++
			} else {
				value.WriteByte(raw[i])
				i++
			}
		}
		value.WriteByte(raw[i])
	}
	return value.String()
}

// Compose expands variables in double quotes, so escape every '$' as '$$'.
// Double quotes also let newline-containing and trailing-backslash values be
// encoded without creating additional physical dotenv statements.
func quoteMergeEnvValue(value string) string {
	return `"` + strings.NewReplacer(
		`\`, `\\`, `"`, `\"`, `$`, `$$`, "\n", `\n`, "\r", `\r`, "\t", `\t`,
		"\a", `\a`, "\b", `\b`, "\f", `\f`, "\v", `\v`,
	).Replace(value) + `"`
}
