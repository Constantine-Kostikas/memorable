package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// ErrVerification means a release failed a signature, checksum, or archive check. The installed binary is untouched.
var ErrVerification = errors.New("release verification failed")

// ReleaseKey is the armored public key that signs checksums.txt in every release.
//
//go:embed release-key.asc
var ReleaseKey []byte

// ReleaseKeyFingerprint identifies ReleaseKey.
const ReleaseKeyFingerprint = "D4A4DEE111ABA5ACBE1F49773B21B7770B4334A4"

const binaryName = "memorable"

// archiveOverhead bounds how much the rest of an archive (README, headers) may add to the binary when decompressed.
const archiveOverhead = 16 << 20

func verificationError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrVerification, fmt.Sprintf(format, args...))
}

func keyFingerprint(armored []byte) (string, error) {
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil || len(keys) != 1 {
		return "", verificationError("release key is not a single OpenPGP key")
	}
	return strings.ToUpper(hex.EncodeToString(keys[0].PrimaryKey.Fingerprint)), nil
}

// VerifySignature checks that sig is a valid detached signature over signed by armoredKey.
func VerifySignature(armoredKey, signed, sig []byte) error {
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armoredKey))
	if err != nil || len(keys) == 0 {
		return verificationError("cannot read the release key")
	}
	if len(sig) == 0 {
		return verificationError("checksums.txt.sig is empty")
	}
	signerEntity, err := openpgp.CheckDetachedSignature(keys, bytes.NewReader(signed), bytes.NewReader(sig), nil)
	if err != nil || signerEntity == nil {
		return verificationError("bad signature on checksums.txt: %v", err)
	}
	return nil
}

// ChecksumFor returns the lowercase hex sha256 listed for name in a GoReleaser checksums.txt.
func ChecksumFor(checksums []byte, name string) (string, error) {
	var found []string
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == name {
			found = append(found, strings.ToLower(fields[0]))
		}
	}
	if err := sc.Err(); err != nil {
		return "", verificationError("reading checksums.txt: %v", err)
	}
	if len(found) != 1 {
		return "", verificationError("checksums.txt lists %s %d times", name, len(found))
	}
	if sum, err := hex.DecodeString(found[0]); err != nil || len(sum) != sha256.Size {
		return "", verificationError("checksums.txt has a malformed sha256 for %s", name)
	}
	return found[0], nil
}

// VerifyChecksum checks that data hashes to wantHex.
func VerifyChecksum(data []byte, wantHex string) error {
	want, err := hex.DecodeString(wantHex)
	if err != nil {
		return verificationError("malformed sha256 %q", wantHex)
	}
	got := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return verificationError("sha256 mismatch: got %x, want %s", got, strings.ToLower(wantHex))
	}
	return nil
}

// ExtractBinary writes the top-level `memorable` regular file from a tar.gz archive to w.
// Any entry that is absolute or escapes the archive root fails the whole archive.
func ExtractBinary(archive []byte, w io.Writer, maxSize int64) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return verificationError("archive is not gzip: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(io.LimitReader(gz, maxSize+archiveOverhead))
	found := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return verificationError("reading archive: %v", err)
		}
		if !filepath.IsLocal(hdr.Name) {
			return verificationError("archive entry %q escapes the archive", hdr.Name)
		}
		if path.Clean(hdr.Name) != binaryName {
			continue
		}
		if found {
			return verificationError("archive contains %s more than once", binaryName)
		}
		if hdr.Typeflag != tar.TypeReg {
			return verificationError("archive entry %s is not a regular file", binaryName)
		}
		if hdr.Size > maxSize {
			return verificationError("%s is %d bytes, over the %d byte limit", binaryName, hdr.Size, maxSize)
		}
		n, err := io.Copy(w, io.LimitReader(tr, maxSize+1))
		if err != nil {
			return fmt.Errorf("writing %s: %w", binaryName, err)
		}
		if n > maxSize || n != hdr.Size {
			return verificationError("%s size does not match its header", binaryName)
		}
		found = true
	}
	if !found {
		return verificationError("archive has no %s binary", binaryName)
	}
	return nil
}
