package update

import (
	"context"
	"fmt"
	"io"
)

// Reporter receives progress while Run works, so a slow download does not look like a hang.
type Reporter interface {
	// Status reports a step, such as the release that was found.
	Status(msg string)
	// Transfer starts a download of size bytes (0 when unknown). Downloaded bytes are written to the returned
	// writer as they arrive, and Close is called when the download ends, successfully or not.
	Transfer(name string, size int64) io.WriteCloser
}

func (u *Updater) status(format string, args ...any) {
	if u.Report != nil {
		u.Report.Status(fmt.Sprintf(format, args...))
	}
}

func (u *Updater) download(ctx context.Context, a Asset, maxBytes int64) ([]byte, error) {
	if u.Report == nil {
		return u.GitHub.Download(ctx, a, maxBytes)
	}
	progress := u.Report.Transfer(a.Name, a.Size)
	defer func() { _ = progress.Close() }()
	return u.GitHub.DownloadTo(ctx, a, maxBytes, progress)
}
