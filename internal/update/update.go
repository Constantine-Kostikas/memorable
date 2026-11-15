package update

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/mod/semver"
)

var (
	// ErrDevBuild means the running binary has no release version to compare against.
	ErrDevBuild = errors.New("this is a development build; pass --force to replace it with a release")
	// ErrHomebrew means Homebrew owns the binary.
	ErrHomebrew = errors.New("this install is managed by Homebrew; run `brew upgrade memorable` instead")
	// ErrInvalidVersion means --version is not a semantic version.
	ErrInvalidVersion = errors.New("invalid version")
)

const (
	maxSignedFileBytes = 64 << 10
	maxArchiveBytes    = 100 << 20
	maxBinaryBytes     = 200 << 20
)

// Status is the outcome of Run.
type Status string

// Outcomes of Run.
const (
	StatusUpToDate  Status = "up_to_date"
	StatusAvailable Status = "available"
	StatusUpdated   Status = "updated"
	StatusNeedsRoot Status = "needs_root"
)

// Options select the release to install.
type Options struct {
	Pre     bool   // include pre-releases even on a stable install
	Force   bool   // replace a development build
	Check   bool   // report only; touch nothing
	Version string // install this tag instead of the newest
}

// Result describes what Run did. Command is set when root must finish the install.
type Result struct {
	Status  Status   `json:"status"`
	Current string   `json:"current"`
	Target  string   `json:"target,omitempty"`
	Path    string   `json:"path"`
	Command []string `json:"command,omitempty"`
}

// Updater replaces the running binary with a verified release.
type Updater struct {
	Current    string // the running version, from ldflags
	GitHub     *GitHub
	PublicKey  []byte // armored key that must sign checksums.txt
	GOOS       string
	GOARCH     string
	Executable func() (string, error)
	// StageDir is the parent of the private directory that holds the verified binary when the install directory
	// is not writable. Empty means os.TempDir().
	StageDir string
	IsTTY    bool
	// Sudo runs argv (argv[0] is "sudo") attached to the terminal. It is only used on a TTY.
	Sudo func(ctx context.Context, argv ...string) error
}

// Run finds the release opts selects and, unless opts.Check is set, installs it over the running binary.
// Nothing touches the installed binary until the signature, checksum, and archive have all been verified.
func (u *Updater) Run(ctx context.Context, opts Options) (Result, error) {
	pinned, err := normalizeVersion(opts.Version)
	if err != nil {
		return Result{}, err
	}
	current, _ := normalizeVersion(u.Current)
	target, err := u.targetPath()
	if err != nil {
		return Result{}, err
	}
	res := Result{Current: u.Current, Path: target}
	if current != "" {
		res.Current = current
	}
	if !opts.Check {
		if isHomebrew(target) {
			return res, ErrHomebrew
		}
		if current == "" && !opts.Force {
			return res, ErrDevBuild
		}
	}

	rel, found, err := u.selectRelease(ctx, current, pinned, opts.Pre)
	if err != nil {
		return res, err
	}
	if !found {
		res.Status = StatusUpToDate
		return res, nil
	}
	res.Target = rel.Tag
	if current != "" && semver.Compare(rel.Tag, current) == 0 && !opts.Force {
		res.Status = StatusUpToDate
		return res, nil
	}
	if pinned == "" && current != "" && semver.Compare(rel.Tag, current) < 0 {
		res.Status = StatusUpToDate
		return res, nil
	}
	if opts.Check {
		res.Status = StatusAvailable
		return res, nil
	}

	bin, err := u.fetchVerified(ctx, rel)
	if err != nil {
		return res, err
	}
	return u.install(ctx, res, bin)
}

// normalizeVersion returns v as a "v"-prefixed semantic version, "" for an empty v, or ErrInvalidVersion.
func normalizeVersion(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return "", fmt.Errorf("%w %q: use a semantic version such as v1.2.3", ErrInvalidVersion, strings.TrimPrefix(v, "v"))
	}
	return v, nil
}

func (u *Updater) targetPath() (string, error) {
	exe, err := u.Executable()
	if err != nil {
		return "", fmt.Errorf("locating the running binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", exe, err)
	}
	return resolved, nil
}

func isHomebrew(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	return slices.Contains(parts, "Cellar") || slices.Contains(parts, "Caskroom")
}

func isPrerelease(rel Release) bool {
	return rel.Prerelease || semver.Prerelease(rel.Tag) != ""
}

// selectRelease returns the pinned release, or the newest eligible one. found is false when no release qualifies.
func (u *Updater) selectRelease(ctx context.Context, current, pinned string, pre bool) (Release, bool, error) {
	if pinned != "" {
		rel, err := u.GitHub.ReleaseByTag(ctx, pinned)
		if err != nil {
			return Release{}, false, err
		}
		if rel.Draft || rel.Tag != pinned {
			return Release{}, false, fmt.Errorf("release %s is not published", pinned)
		}
		return rel, true, nil
	}
	rels, err := u.GitHub.Releases(ctx)
	if err != nil {
		return Release{}, false, err
	}
	includePre := pre || current == "" || semver.Prerelease(current) != ""
	var best Release
	for _, rel := range rels {
		if rel.Draft || !semver.IsValid(rel.Tag) || (isPrerelease(rel) && !includePre) {
			continue
		}
		if best.Tag == "" || semver.Compare(rel.Tag, best.Tag) > 0 {
			best = rel
		}
	}
	if best.Tag == "" {
		if current == "" {
			return Release{}, false, errors.New("no published releases found") //nolint:err113 // shown to the user
		}
		return Release{}, false, nil
	}
	return best, true, nil
}

func findAsset(rel Release, name string) (Asset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// fetchVerified downloads the archive for this platform and returns the binary inside it, after checking the
// signature on checksums.txt and the archive's checksum.
func (u *Updater) fetchVerified(ctx context.Context, rel Release) ([]byte, error) {
	archiveName, err := AssetName(u.GOOS, u.GOARCH)
	if err != nil {
		return nil, err
	}
	archiveAsset, ok := findAsset(rel, archiveName)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s", rel.Tag, archiveName)
	}
	sumsAsset, okSums := findAsset(rel, "checksums.txt")
	sigAsset, okSig := findAsset(rel, "checksums.txt.sig")
	if !okSums || !okSig {
		return nil, verificationError("release %s is missing checksums.txt or checksums.txt.sig", rel.Tag)
	}

	sums, err := u.GitHub.Download(ctx, sumsAsset, maxSignedFileBytes)
	if err != nil {
		return nil, err
	}
	sig, err := u.GitHub.Download(ctx, sigAsset, maxSignedFileBytes)
	if err != nil {
		return nil, err
	}
	if err := VerifySignature(u.PublicKey, sums, sig); err != nil {
		return nil, err
	}
	wantSum, err := ChecksumFor(sums, archiveName)
	if err != nil {
		return nil, err
	}
	archive, err := u.GitHub.Download(ctx, archiveAsset, maxArchiveBytes)
	if err != nil {
		return nil, err
	}
	if err := VerifyChecksum(archive, wantSum); err != nil {
		return nil, err
	}
	var bin bytes.Buffer
	if err := ExtractBinary(archive, &bin, maxBinaryBytes); err != nil {
		return nil, err
	}
	return bin.Bytes(), nil
}

var errNotWritable = errors.New("install directory is not writable")

func (u *Updater) install(ctx context.Context, res Result, bin []byte) (Result, error) {
	err := replaceInDir(res.Path, bin)
	if err == nil {
		res.Status = StatusUpdated
		return res, nil
	}
	if !errors.Is(err, errNotWritable) {
		return res, err
	}

	staged, cleanup, err := u.stage(bin)
	if err != nil {
		return res, err
	}
	argv := []string{"sudo", "install", "-m", "0755", staged, res.Path}
	if !u.IsTTY || u.Sudo == nil {
		res.Status = StatusNeedsRoot
		res.Command = argv
		return res, nil
	}
	defer cleanup()
	if err := u.Sudo(ctx, argv...); err != nil {
		return res, fmt.Errorf("sudo install: %w", err)
	}
	res.Status = StatusUpdated
	return res, nil
}

// replaceInDir writes bin to a temp file beside target and renames it over target, so running processes keep
// the old file and a crash never leaves a partial binary. All file operations are confined to target's directory.
func replaceInDir(target string, bin []byte) (err error) {
	dir, base := filepath.Split(target)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("opening %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()

	tmp := ".memorable-update-" + rand.Text()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) {
			return errNotWritable
		}
		return fmt.Errorf("creating a temp file in %s: %w", dir, err)
	}
	defer func() {
		if err != nil {
			_ = root.Remove(tmp)
		}
	}()
	if _, err = f.Write(bin); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("writing the new binary: %w", err)
	}
	if err = root.Chmod(tmp, 0o755); err != nil { //nolint:gosec // G302: the binary must be executable by everyone, like the one it replaces
		return fmt.Errorf("making the new binary executable: %w", err)
	}
	if err = root.Rename(tmp, base); err != nil {
		return fmt.Errorf("replacing %s: %w", target, err)
	}
	return nil
}

// stage writes bin to a new 0700 directory so only this user (and root) can change it before sudo installs it.
func (u *Updater) stage(bin []byte) (string, func(), error) {
	dir, err := os.MkdirTemp(u.StageDir, "memorable-update-")
	if err != nil {
		return "", nil, fmt.Errorf("creating a staging directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	staged := filepath.Join(dir, binaryName)
	if err := os.WriteFile(staged, bin, 0o755); err != nil { //nolint:gosec // G306: install copies the mode, and the directory is 0700
		cleanup()
		return "", nil, fmt.Errorf("staging the new binary: %w", err)
	}
	return staged, cleanup, nil
}
