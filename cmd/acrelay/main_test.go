package main

import "testing"

func TestParseOptionalFormalRoundBound(t *testing.T) {
	tests := []struct {
		raw  string
		want int
		ok   bool
	}{
		{raw: "", want: 0, ok: true},
		{raw: "1", want: 1, ok: true},
		{raw: "3", want: 3, ok: true},
		{raw: "5", want: 5, ok: true},
		{raw: "0", ok: false},
		{raw: "6", ok: false},
		{raw: "nope", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseOptionalFormalRoundBound(tt.raw)
			if tt.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("expected validation error")
			}
			if tt.ok && got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}
