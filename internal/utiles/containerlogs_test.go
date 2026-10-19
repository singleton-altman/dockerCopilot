package utiles

import "testing"

func TestParseLogTail(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "empty falls back to default", raw: "", want: defaultLogTail},
		{name: "non numeric falls back to default", raw: "abc", want: defaultLogTail},
		{name: "zero falls back to default", raw: "0", want: defaultLogTail},
		{name: "negative falls back to default", raw: "-1", want: defaultLogTail},
		{name: "valid value", raw: "500", want: 500},
		{name: "all is capped", raw: "all", want: maxLogTail},
		{name: "over max is capped", raw: "999999", want: maxLogTail},
		{name: "whitespace is trimmed", raw: " 42 ", want: 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseLogTail(tt.raw); got != tt.want {
				t.Fatalf("ParseLogTail(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeLogOutput(t *testing.T) {
	if got, want := NormalizeLogOutput("a\r\nb\r\n"), "a\nb\n"; got != want {
		t.Fatalf("NormalizeLogOutput() = %q, want %q", got, want)
	}
}

func TestContainerLogsRejectsEmptyID(t *testing.T) {
	if _, err := ContainerLogs(nil, "", 100, false); err == nil {
		t.Fatal("expected error for empty container id")
	}
}
