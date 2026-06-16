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
