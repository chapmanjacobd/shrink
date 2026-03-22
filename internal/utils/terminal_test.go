package utils

import "testing"

func TestTruncateMiddle(t *testing.T) {
	if TruncateMiddle("hello world", 10) != "hell…orld" {
		t.Errorf("got %s", TruncateMiddle("hello world", 10))
	}
	if TruncateMiddle("hello", 10) != "hello" {
		t.Errorf("got %s", TruncateMiddle("hello", 10))
	}
}

func TestGetTerminalWidth(t *testing.T) {
	w := GetTerminalWidth()
	if w <= 0 {
		t.Errorf("expected positive width")
	}
}
