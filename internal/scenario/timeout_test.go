package scenario

import (
	"testing"
	"time"
)

func TestParseStepTimeout(t *testing.T) {
	customDefault := 42 * time.Second

	tests := []struct {
		name    string
		input   string
		def     time.Duration
		want    time.Duration
		wantErr bool
	}{
		{name: "empty returns default", input: "", def: customDefault, want: customDefault},
		{name: "valid seconds", input: "5s", def: customDefault, want: 5 * time.Second},
		{name: "valid milliseconds", input: "250ms", def: customDefault, want: 250 * time.Millisecond},
		{name: "valid minutes", input: "2m", def: customDefault, want: 2 * time.Minute},
		{name: "invalid duration", input: "not-a-duration", def: customDefault, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStepTimeout(tt.input, tt.def)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
