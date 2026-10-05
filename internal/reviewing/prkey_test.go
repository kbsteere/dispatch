package reviewing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePRRef(t *testing.T) {
	tests := []struct {
		in      string
		want    PRKey
		wantErr bool
	}{
		{in: "chainguard-dev/mono#62436", want: PRKey{"chainguard-dev", "mono", 62436}},
		{
			in:   "https://github.com/chainguard-dev/stereo/pull/1234",
			want: PRKey{"chainguard-dev", "stereo", 1234},
		},
		{
			in:   "https://github.com/chainguard-dev/omnibump/pull/88/files",
			want: PRKey{"chainguard-dev", "omnibump", 88},
		},
		{in: "  chainguard-dev/mono#7 ", want: PRKey{"chainguard-dev", "mono", 7}},
		{in: "chainguard-dev/mono", wantErr: true},
		{in: "chainguard-dev/mono#0", wantErr: true},
		{in: "chainguard-dev/mono#-3", wantErr: true},
		{in: "../etc/passwd#1", wantErr: true},
		{in: "chainguard-dev/..#1", wantErr: true},
		{in: "https://evil.example/chainguard-dev/mono/pull/1", wantErr: true},
		{in: "chainguard-dev/mo no#1", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParsePRRef(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPRKeyFormats(t *testing.T) {
	k := PRKey{"chainguard-dev", "mono", 62436}
	assert.Equal(t, "chainguard-dev/mono#62436", k.String())
	assert.Equal(t, "chainguard-dev/mono", k.NameWithOwner())
	assert.Equal(t, "chainguard-dev__mono__62436", k.FileName())
	assert.Equal(t, "https://github.com/chainguard-dev/mono/pull/62436", k.URL())
}

func TestValidSHA(t *testing.T) {
	assert.True(t, ValidSHA("acd7e58b2f1c4d9e8a7b6c5d4e3f2a1b0c9d8e7f"))
	assert.False(t, ValidSHA("acd7e58"))
	assert.False(t, ValidSHA("--upload-pack=evil-aaaaaaaaaaaaaaaaaaaaaaa"))
	assert.False(t, ValidSHA("ACD7E58B2F1C4D9E8A7B6C5D4E3F2A1B0C9D8E7F"))
}

func TestSanitizeTerminal(t *testing.T) {
	in := "fix\x1b]52;c;ZXZpbA==\x07 clipboard\x1b[2J\r\nline2\tok\u009b31m"
	assert.Equal(t, "fix]52;c;ZXZpbA== clipboard[2J\nline2\tok31m", SanitizeTerminal(in))
}
