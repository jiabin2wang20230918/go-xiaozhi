package voice

import "strings"

var defaultSentencePunctuation = []rune{'。', '？', '！', '；', '：'}

// defaultMaxRunesPerSentence 是无标点时的兜底切句长度（rune 计）。
// 中文 LLM 常先吐一长串无标点文本，若只等标点，首句迟迟凑不齐、TTS 干等，
// 推高首音延迟。积攒到此长度即先切一句合成发送，是"兜底"而非目标——
// 标点通常会更早到达（标点优先于此兜底）。
const defaultMaxRunesPerSentence = 16

type SentenceSplitter struct {
	buffer       string
	punctuations []rune
	maxRunes     int // <=0 表示禁用兜底，仅按标点切（旧行为）
}

func NewSentenceSplitter() *SentenceSplitter {
	return &SentenceSplitter{
		punctuations: defaultSentencePunctuation,
		maxRunes:     defaultMaxRunesPerSentence,
	}
}

// NewSentenceSplitterWithMax 用指定的兜底切句长度构造分句器。
// maxRunes <=0 时禁用长度兜底（仅按标点切）。
func NewSentenceSplitterWithMax(maxRunes int) *SentenceSplitter {
	return &SentenceSplitter{
		punctuations: defaultSentencePunctuation,
		maxRunes:     maxRunes,
	}
}

func (s *SentenceSplitter) Push(chunk string) []string {
	s.buffer += chunk

	// (1) 标点切分优先（保留原有"最后一个标点"的贪婪语义）。
	if end := s.lastPunctuationEnd(); end >= 0 {
		raw := s.buffer[:end]
		s.buffer = s.buffer[end:]
		if text := strings.TrimSpace(raw); text != "" {
			return []string{text}
		}
	}

	// (2) 长度兜底：仅当没有标点命中且缓冲区已达上限时，按 rune 边界提前切一句。
	// 避免无标点的长文本让首句迟迟无法合成。
	if s.maxRunes > 0 {
		runes := []rune(s.buffer)
		if len(runes) >= s.maxRunes {
			raw := string(runes[:s.maxRunes])
			s.buffer = string(runes[s.maxRunes:])
			if text := strings.TrimSpace(raw); text != "" {
				return []string{text}
			}
		}
	}
	return nil
}

func (s *SentenceSplitter) Flush() []string {
	text := strings.TrimSpace(s.buffer)
	s.buffer = ""
	if text == "" {
		return nil
	}
	return []string{text}
}

func (s *SentenceSplitter) lastPunctuationEnd() int {
	last := -1
	for i, r := range s.buffer {
		for _, punctuation := range s.punctuations {
			if r == punctuation {
				last = i + len(string(r))
				break
			}
		}
	}
	return last
}
