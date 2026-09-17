package kiro

import (
	"encoding/json"
	"testing"
)

func TestStripAssistantSentinels(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "trailing marker",
			in:   "El proyecto usa Go 1.22+.\n\n<｜DSML｜function_calls",
			want: "El proyecto usa Go 1.22+.",
		},
		{
			name: "marker keeps following text",
			in:   "respuesta\n\n<｜DSML｜function_calls\n\nunderstood",
			want: "respuesta\n\nunderstood",
		},
		{
			name: "ascii variant",
			in:   "hola\n\n<|DSML|function_calls",
			want: "hola",
		},
		{
			name: "no marker",
			in:   "solo texto",
			want: "solo texto",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripAssistantSentinels(tc.in); got != tc.want {
				t.Fatalf("stripAssistantSentinels(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestKiroContentTextStripsSentinels(t *testing.T) {
	got := kiroContentText(json.RawMessage(`"usá Go 1.22\n\n<｜DSML｜function_calls"`))
	if got != "usá Go 1.22" {
		t.Fatalf("kiroContentText = %q, want the marker stripped", got)
	}
}
