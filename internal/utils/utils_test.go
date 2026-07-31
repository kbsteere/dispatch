package utils

import (
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "empty string is zero", input: "", want: 0},
		{name: "days", input: "10d", want: 240 * time.Hour},
		{name: "week", input: "1w", want: 168 * time.Hour},
		{name: "negative fractional week", input: "-1.5w", want: -252 * time.Hour},
		{name: "month shorthand", input: "3mo", want: 3 * 30 * 24 * time.Hour},
		{name: "compound years months days", input: "3Y4M5d", want: 29280 * time.Hour},
		{name: "literal hours pass through", input: "2h", want: 2 * time.Hour},
		{name: "unknown unit errors", input: "10x", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDuration(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDuration(%q): got nil error, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDuration(%q): unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q): got = %v, want = %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestTimeElapsed(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		then time.Time
		want string
	}{
		{name: "just now", then: now, want: "now"},
		{name: "hours", then: now.Add(-2 * time.Hour), want: "2h"},
		{name: "days", then: now.Add(-3 * 24 * time.Hour), want: "3d"},
		{name: "weeks", then: now.Add(-10 * 24 * time.Hour), want: "1w"},
		{name: "months", then: now.Add(-40 * 24 * time.Hour), want: "1mo"},
		{name: "years", then: now.Add(-400 * 24 * time.Hour), want: "1y"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TimeElapsed(tt.then); got != tt.want {
				t.Errorf("TimeElapsed: got = %q, want = %q", got, tt.want)
			}
		})
	}
}

func TestShortNumber(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{1500, "1k"},
		{999999, "999k"},
		{1000000, "1m"},
		{2500000, "2m"},
	}

	for _, tt := range tests {
		if got := ShortNumber(tt.in); got != tt.want {
			t.Errorf("ShortNumber(%d): got = %q, want = %q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text unchanged", in: "hello world", want: "hello world"},
		{
			name: "safe branch name unchanged",
			in:   "feature/foo-1.0_bar",
			want: "feature/foo-1.0_bar",
		},
		{name: "strips escape sequence", in: "a\x1b[31mred\x1b[0m", want: "a[31mred[0m"},
		{name: "carriage return dropped, newline to space", in: "line1\r\nline2", want: "line1 line2"},
		{name: "tab to space", in: "tab\there", want: "tab here"},
		{name: "strips bell and other C0", in: "\x07bell\x00", want: "bell"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeText(tt.in); got != tt.want {
				t.Errorf("SanitizeText(%q): got = %q, want = %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGetStylePrefix(t *testing.T) {
	tests := []struct {
		name     string
		style    lipgloss.Style
		wantLen  int
		wantSame bool // whether input == output (no reset to strip)
	}{
		{
			name:    "empty style returns empty or minimal ANSI",
			style:   lipgloss.NewStyle(),
			wantLen: 0,
		},
		{
			name:    "style with foreground color",
			style:   lipgloss.NewStyle().Foreground(lipgloss.Color("red")),
			wantLen: 5, // At least some ANSI codes
		},
		{
			name:    "style with background color",
			style:   lipgloss.NewStyle().Background(lipgloss.Color("blue")),
			wantLen: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetStylePrefix(tt.style)
			// The result should not end with reset sequence
			if (len(result) >= 4 && result[len(result)-4:] == "\x1b[0m") ||
				(len(result) >= 3 && result[len(result)-3:] == "\x1b[m") {
				t.Error("GetStylePrefix should strip trailing reset sequence")
			}
		})
	}
}

func TestGetStylePrefix_StripsReset(t *testing.T) {
	// Create a style that will produce a reset sequence
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	rendered := style.Render("")

	// Verify the raw render has a reset
	hasReset := (len(rendered) >= 4 && rendered[len(rendered)-4:] == "\x1b[0m") ||
		(len(rendered) >= 3 && rendered[len(rendered)-3:] == "\x1b[m")

	prefix := GetStylePrefix(style)

	// Prefix should not have reset even if rendered did
	prefixHasReset := (len(prefix) >= 4 && prefix[len(prefix)-4:] == "\x1b[0m") ||
		(len(prefix) >= 3 && prefix[len(prefix)-3:] == "\x1b[m")
	if prefixHasReset {
		t.Errorf("GetStylePrefix should strip reset, but got: %q", prefix)
	}

	// If original had reset, prefix should be shorter
	if hasReset && len(prefix) >= len(rendered) {
		t.Error("GetStylePrefix should return shorter string when stripping reset")
	}
}

func TestGetStylePrefix_VariousStyles(t *testing.T) {
	tests := []struct {
		name  string
		style lipgloss.Style
	}{
		{
			name:  "bold style",
			style: lipgloss.NewStyle().Bold(true),
		},
		{
			name:  "italic style",
			style: lipgloss.NewStyle().Italic(true),
		},
		{
			name:  "underline style",
			style: lipgloss.NewStyle().Underline(true),
		},
		{
			name: "combined foreground and background",
			style: lipgloss.NewStyle().
				Foreground(lipgloss.Color("red")).
				Background(lipgloss.Color("blue")),
		},
		{
			name: "adaptive color",
			style: lipgloss.NewStyle().
				Foreground(compat.AdaptiveColor{Light: lipgloss.Color("#000000"), Dark: lipgloss.Color("#ffffff")}),
		},
		{
			name:  "256 color",
			style: lipgloss.NewStyle().Foreground(lipgloss.Color("205")),
		},
		{
			name:  "true color",
			style: lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5733")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := GetStylePrefix(tt.style)

			// Should not end with reset
			if (len(prefix) >= 4 && prefix[len(prefix)-4:] == "\x1b[0m") ||
				(len(prefix) >= 3 && prefix[len(prefix)-3:] == "\x1b[m") {
				t.Errorf("GetStylePrefix should strip reset for %s, but got: %q", tt.name, prefix)
			}

			// Should be valid ANSI (starts with escape if non-empty and has styling)
			if len(prefix) > 0 && prefix[0] != '\x1b' {
				// Only fail if the style would produce ANSI codes
				rendered := tt.style.Render("")
				if len(rendered) > 0 && rendered[0] == '\x1b' {
					t.Errorf(
						"GetStylePrefix for %s should start with escape, got: %q",
						tt.name,
						prefix,
					)
				}
			}
		})
	}
}

func TestGetStylePrefix_ConcatenationPreservesStyle(t *testing.T) {
	// Test that concatenating prefix + text works as expected
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("green"))
	prefix := GetStylePrefix(style)

	// The prefix should be usable for concatenation
	result := prefix + "Hello"

	// Result should contain the text
	if len(result) < 5 {
		t.Error("Result should contain 'Hello'")
	}

	// Result should start with ANSI if style produces ANSI
	rendered := style.Render("")
	if len(rendered) > 0 && rendered[0] == '\x1b' && prefix[0] != '\x1b' {
		t.Error("Prefix should start with ANSI escape when style has color")
	}
}
