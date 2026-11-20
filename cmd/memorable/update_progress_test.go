package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/ui"
)

func TestProgressReporter_PipedPrintsStartAndEndLines(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := newProgressReporter(&out, func(string) string { return "" })
	require.False(t, r.live, "a buffer is not a terminal")

	r.Status("Found memorable v0.2.0 (installed: v0.1.0)")
	bar := r.Transfer("memorable_linux_amd64.tar.gz", 3<<20)
	_, err := bar.Write(make([]byte, 3<<20))
	require.NoError(t, err)
	require.NoError(t, bar.Close())

	assert.Equal(t, "Found memorable v0.2.0 (installed: v0.1.0)\n"+
		"Downloading memorable_linux_amd64.tar.gz (3.0 MiB)\n"+
		"Downloaded memorable_linux_amd64.tar.gz (3.0 MiB)\n", out.String())
}

func TestProgressReporter_LiveRedrawsBarInPlace(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	clock := time.Unix(0, 0)
	r := &progressReporter{out: &out, live: true, now: func() time.Time { return clock }, bar: ui.ProgressBar(progressBarWidth)}

	bar := r.Transfer("a.tar.gz", 1000)
	clock = clock.Add(progressRedrawGap)
	_, _ = bar.Write(make([]byte, 500))
	_, _ = bar.Write(make([]byte, 100))
	clock = clock.Add(progressRedrawGap)
	_, _ = bar.Write(make([]byte, 400))
	require.NoError(t, bar.Close())

	frames := strings.Split(out.String(), "\r\x1b[2K")[1:]
	require.Len(t, frames, 4, "start, one throttled redraw per gap, and the final frame")
	assert.Contains(t, frames[0], "  0%")
	assert.Contains(t, frames[1], " 50%  500 B / 1000 B")
	assert.Contains(t, frames[2], "100%")
	assert.Equal(t, strings.TrimSuffix(frames[3], "\n"), frames[2])
	assert.True(t, strings.HasSuffix(out.String(), "\n"))
}

func TestProgressBar_UnknownSizeShowsBytesOnly(t *testing.T) {
	t.Parallel()
	b := &progressBar{reporter: &progressReporter{}, name: "a.tar.gz", received: 2048}
	assert.Equal(t, "Downloading a.tar.gz  2.0 KiB", b.line())
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 9_750_000: "9.3 MiB", 3 << 30: "3.0 GiB"} {
		assert.Equal(t, want, formatBytes(n), n)
	}
}
