package voice

func trimEdgePunctuationAndEmoji(text string) string {
	runes := []rune(text)
	start := 0
	for start < len(runes) && isTrimmedEdgeRune(runes[start]) {
		start++
	}
	end := len(runes) - 1
	for end >= start && isTrimmedEdgeRune(runes[end]) {
		end--
	}
	if start > end {
		return ""
	}
	return string(runes[start : end+1])
}

func isTrimmedEdgeRune(r rune) bool {
	if r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '，' || r == ',' || r == '。' || r == '.' || r == '！' || r == '!' || r == '-' || r == '－' || r == '、' {
		return true
	}
	return (r >= 0x1F600 && r <= 0x1F64F) ||
		(r >= 0x1F300 && r <= 0x1F5FF) ||
		(r >= 0x1F680 && r <= 0x1F6FF) ||
		(r >= 0x1F900 && r <= 0x1F9FF) ||
		(r >= 0x1FA70 && r <= 0x1FAFF) ||
		(r >= 0x2600 && r <= 0x26FF) ||
		(r >= 0x2700 && r <= 0x27BF)
}
