package cli

import "testing"

func TestParsePayloadSize(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"5MB", 5 * 1024 * 1024},
		{"5mb", 5 * 1024 * 1024},
		{"5 MB", 5 * 1024 * 1024},
		{"5M", 5 * 1024 * 1024},
		{"512KB", 512 * 1024},
		{"512K", 512 * 1024},
		{"2.5MB", int(2.5 * 1024 * 1024)},
		{"1GB", 1024 * 1024 * 1024},
		{"1G", 1024 * 1024 * 1024},
		{"1024B", 1024},
		{"5", 5 * 1024 * 1024}, // bare number → MB
		{"0", 0},
		{"", 0},
		{"garbage", 0},
		{"-5", 0},
	}
	for _, tc := range cases {
		got := ParsePayloadSize(tc.in)
		if got != tc.want {
			t.Errorf("ParsePayloadSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFormatPayloadSize(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{512, "512 B"},
		{2048, "2.0 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{int(2.5 * 1024 * 1024), "2.5 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}
	for _, tc := range cases {
		got := FormatPayloadSize(tc.in)
		if got != tc.want {
			t.Errorf("FormatPayloadSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
