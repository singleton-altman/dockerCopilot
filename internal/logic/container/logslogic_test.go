package container

import (
	"testing"

	"github.com/onlyLTY/dockerCopilot/internal/types"
)

func TestCountLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{name: "empty", in: "", want: 0},
		{name: "single line without newline", in: "hello", want: 1},
		{name: "single line with newline", in: "hello\n", want: 1},
		{name: "two lines", in: "a\nb", want: 2},
		{name: "trailing newline", in: "a\nb\n", want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countLines(tt.in); got != tt.want {
				t.Fatalf("countLines(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestLogsRejectsEmptyID(t *testing.T) {
	logic := NewLogsLogic(nil, nil)
	resp, err := logic.Logs(&types.ContainerLogsReq{})
	if err == nil {
		t.Fatal("expected error for empty container id")
	}
	if resp.Code != 400 {
		t.Fatalf("expected 400 for empty container id, got %d", resp.Code)
	}
}
