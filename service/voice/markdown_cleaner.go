package voice

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	codeBlockPattern        = regexp.MustCompile("(?s)```.*?```")
	headingPattern          = regexp.MustCompile(`(?m)^#+\s*`)
	boldAsteriskPattern     = regexp.MustCompile(`\*\*(.*?)\*\*`)
	boldUnderscorePattern   = regexp.MustCompile(`__(.*?)__`)
	italicAsteriskPattern   = regexp.MustCompile(`\*(\S.*?)\*`)
	italicUnderscorePattern = regexp.MustCompile(`_(\S.*?)_`)
	imagePattern            = regexp.MustCompile(`!\[.*?\]\(.*?\)`)
	linkPattern             = regexp.MustCompile(`\[(.*?)\]\(.*?\)`)
	quotePattern            = regexp.MustCompile(`(?m)^\s*>+\s*`)
	tableBlockPattern       = regexp.MustCompile(`(?m)(?:^[^\n]*\|[^\n]*\n)+`)
	listPattern             = regexp.MustCompile(`(?m)^\s*[*+-]\s*`)
	blockFormulaPattern     = regexp.MustCompile(`(?s)\$\$.*?\$\$`)
	inlineDollarPattern     = regexp.MustCompile(`(?m)(^|[^A-Za-z0-9])\$([^\n$]+)\$([^A-Za-z0-9]|$)`)
	normalFormulaChars      = regexp.MustCompile(`[a-zA-Z\\^_{}\+\-\(\)\[\]=]`)
	multipleNewlinePattern  = regexp.MustCompile(`\n{2,}`)
	tableSeparatorPattern   = regexp.MustCompile(`^\|\s*[-:]+\s*(\|\s*[-:]+\s*)+\|?$`)
)

func cleanMarkdownForTTS(text string) string {
	text = codeBlockPattern.ReplaceAllString(text, "")
	text = headingPattern.ReplaceAllString(text, "")
	text = boldAsteriskPattern.ReplaceAllString(text, "$1")
	text = boldUnderscorePattern.ReplaceAllString(text, "$1")
	text = italicAsteriskPattern.ReplaceAllString(text, "$1")
	text = italicUnderscorePattern.ReplaceAllString(text, "$1")
	text = imagePattern.ReplaceAllString(text, "")
	text = linkPattern.ReplaceAllString(text, "$1")
	text = quotePattern.ReplaceAllString(text, "")
	text = tableBlockPattern.ReplaceAllStringFunc(text, replaceMarkdownTableBlock)
	text = listPattern.ReplaceAllString(text, "- ")
	text = blockFormulaPattern.ReplaceAllString(text, "")
	text = inlineDollarPattern.ReplaceAllStringFunc(text, replaceInlineDollarFormula)
	text = multipleNewlinePattern.ReplaceAllString(text, "\n")
	return strings.TrimSpace(text)
}

func replaceInlineDollarFormula(match string) string {
	parts := inlineDollarPattern.FindStringSubmatch(match)
	if len(parts) != 4 {
		return match
	}
	if normalFormulaChars.MatchString(parts[2]) {
		return parts[1] + parts[2] + parts[3]
	}
	return match
}

func replaceMarkdownTableBlock(block string) string {
	lines := strings.Split(strings.Trim(block, "\n"), "\n")
	parsed := make([][]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if tableSeparatorPattern.MatchString(line) {
			continue
		}
		columns := splitMarkdownTableLine(line)
		if len(columns) > 0 {
			parsed = append(parsed, columns)
		}
	}
	if len(parsed) == 0 {
		return ""
	}
	if len(parsed) == 1 {
		return "单行表格：" + strings.Join(parsed[0], ", ") + "\n"
	}
	headers := parsed[0]
	out := []string{"表头是：" + strings.Join(headers, ", ")}
	for rowIndex, row := range parsed[1:] {
		values := make([]string, 0, len(row))
		for colIndex, cell := range row {
			if colIndex < len(headers) {
				values = append(values, fmt.Sprintf("%s = %s", headers[colIndex], cell))
			} else {
				values = append(values, cell)
			}
		}
		out = append(out, fmt.Sprintf("第 %d 行：%s", rowIndex+1, strings.Join(values, ", ")))
	}
	return strings.Join(out, "\n") + "\n"
}

func splitMarkdownTableLine(line string) []string {
	rawColumns := strings.Split(line, "|")
	columns := make([]string, 0, len(rawColumns))
	for _, column := range rawColumns {
		column = strings.TrimSpace(column)
		if column != "" {
			columns = append(columns, column)
		}
	}
	return columns
}
