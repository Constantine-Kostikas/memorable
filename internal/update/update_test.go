package update

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testGOOS    = "linux"
	testGOARCH  = "amd64"
	testArchive = "memorable_Linux_x86_64.tar.gz"
	oldBinary   = "OLD BINARY"
)

// fakeRelease is one release served by fakeGitHub. files holds asset bodies by name.
type fakeRelease struct {
	tag   string
	pre   bool
	draft bool
	files map[string][]byte
}

// signedRelease builds a release whose archive holds "new <tag>" and whose checksums are signed by s.
func signedRelease(t *testing.T, s signer, tag string, archive []byte) fakeRelease {
	t.Helper()
	if archive == nil {
		archive = makeArchive(t, tarEntry{name: "README.md", body: "docs"}, tarEntry{name: "memorable", body: "new " + tag})
	}
	sums := []byte(sha256Hex(archive) + "  " + testArchive + "\n" + strings.Repeat("0", 64) + "  memorable_Darwin_arm64.tar.gz\n")
	return fakeRelease{
		tag: tag,
		pre: strings.Contains(tag, "-"),
		files: map[string][]byte{
			testArchive:         archive,
			"checksums.txt":     sums,
			"checksums.txt.sig": s.sign(t, sums),
		},
	}
}

type fakeGitHub struct {
	srv          *httptest.Server
	releases     []fakeRelease
	requests     atomic.Int32
	assetFetches atomic.Int32
}

func newFakeGitHub(t *testing.T, releases ...fakeRelease) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{releases: releases}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) apiRelease(r fakeRelease) Release {
	rel := Release{Tag: r.tag, Prerelease: r.pre, Draft: r.draft}
	for name, body := range r.files {
		rel.Assets = append(rel.Assets, Asset{
			Name: name, Size: int64(len(body)),
			URL: f.srv.URL + "/repos/" + testRepo + "/releases/assets/" + r.tag + "/" + name,
		})
	}
	return rel
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	base := "/repos/" + testRepo + "/releases"
	switch {
	case r.URL.Path == base:
		rels := make([]Release, 0, len(f.releases))
		for _, rel := range f.releases {
			rels = append(rels, f.apiRelease(rel))
		}
		_ = json.NewEncoder(w).Encode(rels)
	case strings.HasPrefix(r.URL.Path, base+"/tags/"):
		tag := strings.TrimPrefix(r.URL.Path, base+"/tags/")
		for _, rel := range f.releases {
			if rel.tag == tag {
				_ = json.NewEncoder(w).Encode(f.apiRelease(rel))
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(r.URL.Path, base+"/assets/"):
		f.assetFetches.Add(1)
		http.Redirect(w, r, "/download/"+strings.TrimPrefix(r.URL.Path, base+"/assets/"), http.StatusFound)
	case strings.HasPrefix(r.URL.Path, "/download/"):
		tag, name, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/download/"), "/")
		for _, rel := range f.releases {
			if body, ok := rel.files[name]; ok && rel.tag == tag {
				_, _ = w.Write(body)
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

type installFixture struct {
	dir    string
	target string
	link   string
}

// newInstall creates an installed binary at <tmp>/<sub>/memorable and a symlink to it, like /usr/local/bin.
func newInstall(t *testing.T, sub string) installFixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, sub)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	target := filepath.Join(dir, "memorable")
	require.NoError(t, os.WriteFile(target, []byte(oldBinary), 0o755))
	link := filepath.Join(root, "memorable-link")
	require.NoError(t, os.Symlink(target, link))
	return installFixture{dir: dir, target: target, link: link}
}

func newTestUpdater(f *fakeGitHub, s signer, inst installFixture, current string) *Updater {
	allowLocal := func(u *url.URL) bool { return u.Hostname() == "127.0.0.1" }
	return &Updater{
		Current: current,
		GitHub: &GitHub{
			BaseURL: f.srv.URL, Repo: testRepo, Token: "tok", UserAgent: "memorable/test",
			HTTP: NewHTTPClient(f.srv.Client().Transport, allowLocal),
		},
		PublicKey:  s.public,
		GOOS:       testGOOS,
		GOARCH:     testGOARCH,
		Executable: func() (string, error) { return inst.link, nil },
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func assertUntouched(t *testing.T, inst installFixture) {
	t.Helper()
	assert.Equal(t, oldBinary, readFile(t, inst.target))
	entries, err := os.ReadDir(inst.dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp files left beside the binary")
}

func TestRun_InstallsNewestStable(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	s := newSigner(t)
	f := newFakeGitHub(t,
		signedRelease(t, s, "v0.3.0-rc.1", nil),
		signedRelease(t, s, "v0.2.0", nil),
		signedRelease(t, s, "v0.1.5", nil),
	)
	inst := newInstall(t, "bin")

	res, err := newTestUpdater(f, s, inst, "0.1.0").Run(t.Context(), Options{})
	require.NoError(t, err)
	is.Equal(StatusUpdated, res.Status)
	is.Equal("v0.1.0", res.Current)
	is.Equal("v0.2.0", res.Target)
	is.Equal(inst.target, res.Path)
	is.Equal("new v0.2.0", readFile(t, inst.target))
	info, err := os.Stat(inst.target)
	require.NoError(t, err)
	is.Equal(os.FileMode(0o755), info.Mode().Perm())
	linkInfo, err := os.Lstat(inst.link)
	require.NoError(t, err)
	is.Equal(os.ModeSymlink, linkInfo.Mode()&os.ModeSymlink, "the symlink stays a symlink")
	entries, err := os.ReadDir(inst.dir)
	require.NoError(t, err)
	is.Len(entries, 1, "no temp files left beside the binary")
}

func TestRun_UpToDate(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", nil), signedRelease(t, s, "v0.1.0", nil))
	inst := newInstall(t, "bin")

	res, err := newTestUpdater(f, s, inst, "0.2.0").Run(t.Context(), Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusUpToDate, res.Status)
	assert.Zero(t, f.assetFetches.Load())
	assertUntouched(t, inst)
}

func TestRun_SelectsRelease(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	releases := []fakeRelease{
		signedRelease(t, s, "v0.4.0", nil),
		signedRelease(t, s, "v0.3.0-rc.1", nil),
		signedRelease(t, s, "v0.2.0", nil),
		signedRelease(t, s, "v0.1.0-alpha.1", nil),
		{tag: "not-semver"},
		{tag: "v9.0.0", draft: true},
	}
	tests := []struct {
		name    string
		current string
		opts    Options
		want    string
		status  Status
	}{
		{name: "ignores drafts and non-semver tags", current: "0.2.0", want: "v0.4.0", status: StatusAvailable},
		{name: "pre flag still prefers a higher stable", current: "0.3.0", opts: Options{Pre: true}, want: "v0.4.0", status: StatusAvailable},
		{name: "pinned version", current: "0.1.0-alpha.1", opts: Options{Version: "v0.2.0"}, want: "v0.2.0", status: StatusAvailable},
		{name: "pinned version without v", current: "0.1.0-alpha.1", opts: Options{Version: "0.3.0-rc.1"}, want: "v0.3.0-rc.1", status: StatusAvailable},
		{name: "pinned older version downgrades", current: "0.3.0", opts: Options{Version: "v0.2.0"}, want: "v0.2.0", status: StatusAvailable},
		{name: "pinned current version", current: "0.2.0", opts: Options{Version: "v0.2.0"}, want: "v0.2.0", status: StatusUpToDate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeGitHub(t, releases...)
			inst := newInstall(t, "bin")
			tt.opts.Check = true
			res, err := newTestUpdater(f, s, inst, tt.current).Run(t.Context(), tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.status, res.Status)
			assert.Equal(t, tt.want, res.Target)
			assertUntouched(t, inst)
		})
	}
}

func TestRun_PreReleaseSelection(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	releases := []fakeRelease{
		signedRelease(t, s, "v0.3.0-rc.1", nil),
		signedRelease(t, s, "v0.2.0", nil),
	}
	tests := []struct {
		name    string
		current string
		pre     bool
		want    string
	}{
		{name: "stable install stays on stable", current: "0.1.0", want: "v0.2.0"},
		{name: "stable install with --pre", current: "0.1.0", pre: true, want: "v0.3.0-rc.1"},
		{name: "pre-release install", current: "0.1.0-alpha.1", want: "v0.3.0-rc.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeGitHub(t, releases...)
			inst := newInstall(t, "bin")
			res, err := newTestUpdater(f, s, inst, tt.current).Run(t.Context(), Options{Pre: tt.pre, Check: true})
			require.NoError(t, err)
			assert.Equal(t, StatusAvailable, res.Status)
			assert.Equal(t, tt.want, res.Target)
		})
	}
}

func TestRun_StableInstallWithOnlyPreReleasesIsUpToDate(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	f := newFakeGitHub(t, signedRelease(t, s, "v0.3.0-rc.1", nil))
	res, err := newTestUpdater(f, s, newInstall(t, "bin"), "0.2.0").Run(t.Context(), Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusUpToDate, res.Status)
}

func TestRun_InvalidPinnedVersion(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	f := newFakeGitHub(t)
	_, err := newTestUpdater(f, s, newInstall(t, "bin"), "0.1.0").Run(t.Context(), Options{Version: "latest"})
	require.ErrorIs(t, err, ErrInvalidVersion)
	assert.Zero(t, f.requests.Load())
}

func TestRun_CheckTouchesNothing(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", nil))
	inst := newInstall(t, "bin")

	res, err := newTestUpdater(f, s, inst, "0.1.0").Run(t.Context(), Options{Check: true})
	require.NoError(t, err)
	assert.Equal(t, StatusAvailable, res.Status)
	assert.Equal(t, "v0.2.0", res.Target)
	assert.Zero(t, f.assetFetches.Load())
	assertUntouched(t, inst)
}

func TestRun_VerificationFailuresLeaveBinaryUntouched(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	other := newSigner(t)
	tests := []struct {
		name   string
		mutate func(t *testing.T, r *fakeRelease)
	}{
		{name: "signed by another key", mutate: func(t *testing.T, r *fakeRelease) {
			t.Helper()
			r.files["checksums.txt.sig"] = other.sign(t, r.files["checksums.txt"])
		}},
		{name: "checksums changed after signing", mutate: func(_ *testing.T, r *fakeRelease) {
			r.files["checksums.txt"] = append([]byte(strings.Repeat("1", 64)+"  "+testArchive+"\n"), r.files["checksums.txt"]...)
		}},
		{name: "missing signature", mutate: func(_ *testing.T, r *fakeRelease) {
			delete(r.files, "checksums.txt.sig")
		}},
		{name: "archive does not match checksum", mutate: func(t *testing.T, r *fakeRelease) {
			t.Helper()
			r.files[testArchive] = makeArchive(t, tarEntry{name: "memorable", body: "tampered"})
		}},
		{name: "archive missing from checksums", mutate: func(t *testing.T, r *fakeRelease) {
			t.Helper()
			sums := []byte(strings.Repeat("0", 64) + "  memorable_Darwin_arm64.tar.gz\n")
			r.files["checksums.txt"] = sums
			r.files["checksums.txt.sig"] = s.sign(t, sums)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rel := signedRelease(t, s, "v0.2.0", nil)
			tt.mutate(t, &rel)
			f := newFakeGitHub(t, rel)
			inst := newInstall(t, "bin")
			_, err := newTestUpdater(f, s, inst, "0.1.0").Run(t.Context(), Options{})
			require.ErrorIs(t, err, ErrVerification)
			assertUntouched(t, inst)
		})
	}
}

func TestRun_TraversalArchiveRefused(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	archive := makeArchive(t, tarEntry{name: "../../memorable", body: "EVIL"}, tarEntry{name: "memorable", body: "new"})
	f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", archive))
	inst := newInstall(t, "bin")

	_, err := newTestUpdater(f, s, inst, "0.1.0").Run(t.Context(), Options{})
	require.ErrorIs(t, err, ErrVerification)
	assertUntouched(t, inst)
	_, statErr := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(inst.dir)), "memorable"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestRun_MissingArchiveForPlatform(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	rel := signedRelease(t, s, "v0.2.0", nil)
	delete(rel.files, testArchive)
	f := newFakeGitHub(t, rel)
	inst := newInstall(t, "bin")
	_, err := newTestUpdater(f, s, inst, "0.1.0").Run(t.Context(), Options{})
	require.ErrorContains(t, err, testArchive)
	assertUntouched(t, inst)
}

func unwritableInstall(t *testing.T) installFixture {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can write to any directory")
	}
	inst := newInstall(t, "bin")
	require.NoError(t, os.Chmod(inst.dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(inst.dir, 0o755) })
	return inst
}

func TestRun_UnwritableDirWithoutTTYPrintsSudoCommand(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	s := newSigner(t)
	f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", nil))
	inst := unwritableInstall(t)
	u := newTestUpdater(f, s, inst, "0.1.0")
	u.StageDir = t.TempDir()
	u.Sudo = func(context.Context, ...string) error {
		t.Error("sudo must not run without a TTY")
		return nil
	}

	res, err := u.Run(t.Context(), Options{})
	require.NoError(t, err)
	is.Equal(StatusNeedsRoot, res.Status)
	require.Len(t, res.Command, 6)
	is.Equal([]string{"sudo", "install", "-m", "0755"}, res.Command[:4])
	is.Equal(inst.target, res.Command[5])
	staged := res.Command[4]
	is.True(strings.HasPrefix(staged, u.StageDir), "staged under StageDir")
	is.Equal("new v0.2.0", readFile(t, staged))
	info, err := os.Stat(staged)
	require.NoError(t, err)
	is.Equal(os.FileMode(0o755), info.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Dir(staged))
	require.NoError(t, err)
	is.Equal(os.FileMode(0o700), dirInfo.Mode().Perm(), "staging directory is private")
	is.Equal(oldBinary, readFile(t, inst.target))
}

func TestRun_UnwritableDirOnTTYRunsSudoInstall(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	s := newSigner(t)
	f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", nil))
	inst := unwritableInstall(t)
	u := newTestUpdater(f, s, inst, "0.1.0")
	u.StageDir = t.TempDir()
	u.IsTTY = true
	var gotArgs []string
	var stagedContent string
	u.Sudo = func(_ context.Context, args ...string) error {
		gotArgs = args
		stagedContent = readFile(t, args[4])
		return nil
	}

	res, err := u.Run(t.Context(), Options{})
	require.NoError(t, err)
	is.Equal(StatusUpdated, res.Status)
	require.Len(t, gotArgs, 6)
	is.Equal([]string{"sudo", "install", "-m", "0755"}, gotArgs[:4])
	is.Equal(inst.target, gotArgs[5])
	is.Equal("new v0.2.0", stagedContent)
	_, statErr := os.Stat(gotArgs[4])
	require.ErrorIs(t, statErr, os.ErrNotExist, "staged copy removed after install")
}

func TestRun_DevBuild(t *testing.T) {
	t.Parallel()
	s := newSigner(t)

	t.Run("refused without --force", func(t *testing.T) {
		t.Parallel()
		f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", nil))
		inst := newInstall(t, "bin")
		_, err := newTestUpdater(f, s, inst, "dev").Run(t.Context(), Options{})
		require.ErrorIs(t, err, ErrDevBuild)
		assert.Zero(t, f.requests.Load())
		assertUntouched(t, inst)
	})
	t.Run("non-semver build counts as dev", func(t *testing.T) {
		t.Parallel()
		f := newFakeGitHub(t)
		_, err := newTestUpdater(f, s, newInstall(t, "bin"), "abc1234").Run(t.Context(), Options{})
		require.ErrorIs(t, err, ErrDevBuild)
	})
	t.Run("--force installs the newest release including pre-releases", func(t *testing.T) {
		t.Parallel()
		f := newFakeGitHub(t, signedRelease(t, s, "v0.3.0-rc.1", nil), signedRelease(t, s, "v0.2.0", nil))
		inst := newInstall(t, "bin")
		res, err := newTestUpdater(f, s, inst, "dev").Run(t.Context(), Options{Force: true})
		require.NoError(t, err)
		assert.Equal(t, StatusUpdated, res.Status)
		assert.Equal(t, "dev", res.Current)
		assert.Equal(t, "new v0.3.0-rc.1", readFile(t, inst.target))
	})
}

func TestRun_HomebrewRefused(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	for _, sub := range []string{"Cellar/memorable/0.1.0/bin", "Caskroom/memorable/0.1.0"} {
		t.Run(sub, func(t *testing.T) {
			t.Parallel()
			f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", nil))
			inst := newInstall(t, sub)
			_, err := newTestUpdater(f, s, inst, "0.1.0").Run(t.Context(), Options{})
			require.ErrorIs(t, err, ErrHomebrew)
			assert.Contains(t, err.Error(), "brew upgrade")
			assert.Zero(t, f.requests.Load())
			assertUntouched(t, inst)
		})
	}
}

func TestRun_SudoFailureIsReported(t *testing.T) {
	t.Parallel()
	s := newSigner(t)
	f := newFakeGitHub(t, signedRelease(t, s, "v0.2.0", nil))
	inst := unwritableInstall(t)
	u := newTestUpdater(f, s, inst, "0.1.0")
	u.StageDir = t.TempDir()
	u.IsTTY = true
	u.Sudo = func(context.Context, ...string) error { return os.ErrPermission }

	_, err := u.Run(t.Context(), Options{})
	require.ErrorIs(t, err, os.ErrPermission)
	assert.Equal(t, oldBinary, readFile(t, inst.target))
	entries, err := os.ReadDir(u.StageDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "staging directory cleaned up")
}

func TestRun_ResultJSON(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(Result{Status: StatusUpdated, Current: "v0.1.0", Target: "v0.2.0", Path: "/usr/local/bin/memorable"}))
	assert.JSONEq(t, `{"status":"updated","current":"v0.1.0","target":"v0.2.0","path":"/usr/local/bin/memorable"}`, buf.String())
}
