package httpx

// paramsOf extracts registered parameter names, not request path matching.
// GoFrame owns matching and supplies the values through GetRouter.
func paramsOf(pattern string) []string {
	var names []string
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '{' {
			continue
		}
		j := i + 1
		for j < len(pattern) && pattern[j] != '}' {
			j++
		}
		if j < len(pattern) {
			names = append(names, pattern[i+1:j])
			i = j
		}
	}
	return names
}
