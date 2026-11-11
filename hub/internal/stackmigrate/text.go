package stackmigrate

import (
	"strconv"
	"strings"
)

func itoa(n int) string               { return strconv.Itoa(n) }
func joinComma(parts []string) string { return strings.Join(parts, "、") }
