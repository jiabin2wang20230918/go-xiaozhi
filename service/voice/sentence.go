package voice

import "strings"

var defaultSentencePunctuation = []rune{'。', '？', '！', '；', '：'}

type SentenceSplitter struct {
	buffer       string
	punctuations []rune
}

func NewSentenceSplitter() *SentenceSplitter {
	return &SentenceSplitter{punctuations: defaultSentencePunctuation}
}

func (s *SentenceSplitter) Push(chunk string) []string {
	s.buffer += chunk
	end := s.lastPunctuationEnd()
	if end < 0 {
		return nil
	}

	raw := s.buffer[:end]
	s.buffer = s.buffer[end:]
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	return []string{text}
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
