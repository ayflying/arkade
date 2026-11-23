package mcp

import "unicode/utf8"

const maxMCPContainerLogBytes = 32 << 10

// Keep the newest output within the MCP budget without changing the Web API.
// Count a newline per line so even empty lines consume part of the budget.
func limitContainerLogs(result map[string]any) {
	lines, ok := result["lines"].([]string)
	if !ok {
		return
	}
	remaining := maxMCPContainerLogBytes
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if len(line) < remaining {
			remaining -= len(line) + 1
			continue
		}
		start := i + 1
		if remaining > 1 {
			offset := len(line) - remaining + 1
			// A partial line must begin at a UTF-8 character boundary.
			for offset < len(line) && !utf8.RuneStart(line[offset]) {
				offset++
			}
			if offset < len(line) {
				lines[i] = line[offset:]
				start = i
			}
		}
		result["lines"] = lines[start:]
		result["truncated"] = true
		return
	}
}
