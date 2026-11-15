package update

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssetName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		goos   string
		goarch string
		want   string
	}{
		{name: "linux amd64", goos: "linux", goarch: "amd64", want: "memorable_Linux_x86_64.tar.gz"},
		{name: "linux arm64", goos: "linux", goarch: "arm64", want: "memorable_Linux_aarch64.tar.gz"},
		{name: "darwin amd64", goos: "darwin", goarch: "amd64", want: "memorable_Darwin_x86_64.tar.gz"},
		{name: "darwin arm64", goos: "darwin", goarch: "arm64", want: "memorable_Darwin_arm64.tar.gz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := AssetName(tt.goos, tt.goarch)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAssetName_Unsupported(t *testing.T) {
	t.Parallel()
	for _, p := range [][2]string{{"windows", "amd64"}, {"linux", "386"}, {"freebsd", "arm64"}} {
		_, err := AssetName(p[0], p[1])
		require.ErrorIs(t, err, ErrUnsupportedPlatform, "%s/%s", p[0], p[1])
	}
}
