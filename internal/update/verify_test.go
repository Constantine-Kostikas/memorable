package update

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifySignature(t *testing.T) {
	t.Parallel()
	release := newSigner(t)
	other := newSigner(t)
	data := []byte("abc  memorable_Linux_x86_64.tar.gz\n")

	t.Run("good signature", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, VerifySignature(release.public, data, release.sign(t, data)))
	})
	t.Run("signed by another key", func(t *testing.T) {
		t.Parallel()
		require.ErrorIs(t, VerifySignature(release.public, data, other.sign(t, data)), ErrVerification)
	})
	t.Run("tampered data", func(t *testing.T) {
		t.Parallel()
		sig := release.sign(t, data)
		require.ErrorIs(t, VerifySignature(release.public, []byte("tampered"), sig), ErrVerification)
	})
	t.Run("garbage signature", func(t *testing.T) {
		t.Parallel()
		require.ErrorIs(t, VerifySignature(release.public, data, []byte("not a signature")), ErrVerification)
	})
	t.Run("empty signature", func(t *testing.T) {
		t.Parallel()
		require.ErrorIs(t, VerifySignature(release.public, data, nil), ErrVerification)
	})
	t.Run("bad key", func(t *testing.T) {
		t.Parallel()
		require.ErrorIs(t, VerifySignature([]byte("nope"), data, release.sign(t, data)), ErrVerification)
	})
}

func TestReleaseKey_IsTheRealPublicKey(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	is.Contains(string(ReleaseKey), "BEGIN PGP PUBLIC KEY BLOCK")
	is.NotContains(string(ReleaseKey), "PRIVATE")
	fp, err := keyFingerprint(ReleaseKey)
	require.NoError(t, err)
	is.Equal(ReleaseKeyFingerprint, fp)
}

func TestChecksumFor(t *testing.T) {
	t.Parallel()
	sum := strings.Repeat("ab", 32)
	file := sum + "  memorable_Linux_x86_64.tar.gz\n" + strings.Repeat("cd", 32) + "  memorable_Darwin_arm64.tar.gz\n"

	got, err := ChecksumFor([]byte(file), "memorable_Linux_x86_64.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, sum, got)

	_, err = ChecksumFor([]byte(file), "memorable_Linux_aarch64.tar.gz")
	require.ErrorIs(t, err, ErrVerification, "missing entry")

	dup := file + strings.Repeat("ef", 32) + "  memorable_Linux_x86_64.tar.gz\n"
	_, err = ChecksumFor([]byte(dup), "memorable_Linux_x86_64.tar.gz")
	require.ErrorIs(t, err, ErrVerification, "duplicate entry")

	_, err = ChecksumFor([]byte("xyz  memorable_Linux_x86_64.tar.gz\n"), "memorable_Linux_x86_64.tar.gz")
	require.ErrorIs(t, err, ErrVerification, "malformed hash")
}

func TestVerifyChecksum(t *testing.T) {
	t.Parallel()
	data := []byte("archive")
	require.NoError(t, VerifyChecksum(data, sha256Hex(data)))
	require.NoError(t, VerifyChecksum(data, strings.ToUpper(sha256Hex(data))))
	require.ErrorIs(t, VerifyChecksum([]byte("other"), sha256Hex(data)), ErrVerification)
}

func TestExtractBinary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entries []tarEntry
		limit   int64
		want    string
		wantErr bool
	}{
		{name: "binary next to readme", entries: []tarEntry{{name: "README.md", body: "docs"}, {name: "memorable", body: "BIN"}}, want: "BIN"},
		{name: "dot-slash prefix", entries: []tarEntry{{name: "./memorable", body: "BIN"}}, want: "BIN"},
		{name: "missing binary", entries: []tarEntry{{name: "README.md", body: "docs"}}, wantErr: true},
		{name: "traversal entry", entries: []tarEntry{{name: "../memorable", body: "EVIL"}, {name: "memorable", body: "BIN"}}, wantErr: true},
		{name: "absolute entry", entries: []tarEntry{{name: "/usr/local/bin/memorable", body: "EVIL"}}, wantErr: true},
		{name: "nested binary ignored", entries: []tarEntry{{name: "sub/memorable", body: "EVIL"}}, wantErr: true},
		{name: "symlink named memorable", entries: []tarEntry{{name: "memorable", typeflag: '2'}}, wantErr: true},
		{name: "duplicate binary", entries: []tarEntry{{name: "memorable", body: "A"}, {name: "memorable", body: "B"}}, wantErr: true},
		{name: "binary over size cap", entries: []tarEntry{{name: "memorable", body: "0123456789"}}, limit: 5, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			limit := tt.limit
			if limit == 0 {
				limit = 1 << 20
			}
			var out bytes.Buffer
			err := ExtractBinary(makeArchive(t, tt.entries...), &out, limit)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrVerification)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, out.String())
		})
	}
}

func TestExtractBinary_NotGzip(t *testing.T) {
	t.Parallel()
	require.ErrorIs(t, ExtractBinary([]byte("plain"), &bytes.Buffer{}, 1<<20), ErrVerification)
}

func TestVerifySignature_RealRelease(t *testing.T) {
	t.Parallel()
	checksums, err := os.ReadFile("testdata/v0.1.0-alpha.1/checksums.txt")
	require.NoError(t, err)
	sig, err := os.ReadFile("testdata/v0.1.0-alpha.1/checksums.txt.sig")
	require.NoError(t, err)

	require.NoError(t, VerifySignature(ReleaseKey, checksums, sig))
	require.ErrorIs(t, VerifySignature(ReleaseKey, append(checksums, '\n'), sig), ErrVerification)
}
