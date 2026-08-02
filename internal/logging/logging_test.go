package logging

import "testing"

func TestMaskKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"正常长度", "fc-1234567890abcd", "fc-****abcd"},
		{"空串", "", "fc-****"},
		{"3 字符", "abc", "fc-****"},
		{"恰好 4 字符", "abcd", "fc-****"},
		{"非 fc- 前缀的 key 也安全", "sk-abcdef123456", "fc-****3456"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MaskKey(c.in)
			if got != c.want {
				t.Errorf("MaskKey(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
