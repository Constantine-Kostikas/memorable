package update

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingReporter struct {
	statuses  []string
	transfers []*recordedTransfer
}

type recordedTransfer struct {
	name     string
	size     int64
	received int
	closed   bool
}

func (r *recordingReporter) Status(msg string) { r.statuses = append(r.statuses, msg) }

func (r *recordingReporter) Transfer(name string, size int64) io.WriteCloser {
	tr := &recordedTransfer{name: name, size: size}
	r.transfers = append(r.transfers, tr)
	return tr
}

func (t *recordedTransfer) Write(p []byte) (int, error) {
	t.received += len(p)
	return len(p), nil
}

func (t *recordedTransfer) Close() error {
	t.closed = true
	return nil
}

func TestRun_ReportsProgress(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	s := newSigner(t)
	f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", nil))
	inst := newInstall(t, "bin")
	u := newTestUpdater(f, s, inst, "0.1.0")
	rep := &recordingReporter{}
	u.Report = rep

	_, err := u.Run(t.Context(), Options{})
	require.NoError(t, err)
	is.Equal([]string{
		"Checking GitHub for releases",
		"Found memorable v0.2.0 (installed: v0.1.0)",
		"Verifying the release signature",
		"Checksum verified",
		"Installing to " + inst.target,
	}, rep.statuses)
	require.Len(t, rep.transfers, 1, "only the archive gets a progress bar")
	tr := rep.transfers[0]
	archiveName, err := AssetName(testGOOS, testGOARCH)
	require.NoError(t, err)
	is.Equal(archiveName, tr.name)
	is.Positive(tr.size)
	is.Equal(int(tr.size), tr.received)
	is.True(tr.closed)
}

func TestRun_CheckReportsNoDownload(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", nil))
	u := newTestUpdater(f, s, newInstall(t, "bin"), "0.1.0")
	rep := &recordingReporter{}
	u.Report = rep

	_, err := u.Run(t.Context(), Options{Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"Checking GitHub for releases"}, rep.statuses)
	assert.Empty(t, rep.transfers)
}
