package voice

import "testing"

func TestSentenceSplitterAcrossChunks(t *testing.T) {
	s := NewSentenceSplitter()
	if got := s.Push("你好"); len(got) != 0 {
		t.Fatalf("unexpected sentence before punctuation: %v", got)
	}
	got := s.Push("世界。下一")
	if len(got) != 1 || got[0] != "你好世界。" {
		t.Fatalf("unexpected sentence: %v", got)
	}
	if got := s.Flush(); len(got) != 1 || got[0] != "下一" {
		t.Fatalf("unexpected flush: %v", got)
	}
}

func TestSentenceSplitterUsesLastPunctuationInBuffer(t *testing.T) {
	s := NewSentenceSplitter()
	got := s.Push("第一句。第二句！剩余")
	if len(got) != 1 || got[0] != "第一句。第二句！" {
		t.Fatalf("unexpected sentence: %v", got)
	}
	if got := s.Flush(); len(got) != 1 || got[0] != "剩余" {
		t.Fatalf("unexpected flush: %v", got)
	}
}

// 无标点时，积攒到 maxRunes 即兜底切一句（降低首音延迟）。
func TestSentenceSplitterMaxRunesFallback(t *testing.T) {
	s := NewSentenceSplitterWithMax(4)
	// 不足上限：不切。
	if got := s.Push("你好世"); len(got) != 0 {
		t.Fatalf("should not split before maxRunes: %v", got)
	}
	// 达到上限：按 rune 边界切前 4 个，余下留 buffer。
	got := s.Push("界继续延伸")
	if len(got) != 1 || got[0] != "你好世界" {
		t.Fatalf("expected maxRunes fallback cut to \"你好世界\", got %v", got)
	}
	// 剩余由 Flush 释放。
	if got := s.Flush(); len(got) != 1 || got[0] != "继续延伸" {
		t.Fatalf("unexpected flush: %v", got)
	}
}

// maxRunes 更小时，标点仍优先于长度兜底（标点先命中即整句返回）。
func TestSentenceSplitterPunctuationBeatsMaxRunes(t *testing.T) {
	s := NewSentenceSplitterWithMax(2)
	got := s.Push("短。")
	if len(got) != 1 || got[0] != "短。" {
		t.Fatalf("punctuation should win over maxRunes, got %v", got)
	}
}

// maxRunes <=0 时禁用兜底，仅按标点切（保留旧行为）。
func TestSentenceSplitterDisabledMaxRunesKeepsPunctuationOnly(t *testing.T) {
	s := NewSentenceSplitterWithMax(0)
	long := "这是一段没有标点的很长文本"
	if got := s.Push(long); len(got) != 0 {
		t.Fatalf("disabled fallback should not cut long no-punctuation text: %v", got)
	}
	if got := s.Flush(); len(got) != 1 || got[0] != long {
		t.Fatalf("flush should release whole buffer when fallback disabled: %v", got)
	}
}
