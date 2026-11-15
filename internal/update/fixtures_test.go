package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/stretchr/testify/require"
)

// signer is a throwaway OpenPGP key that stands in for the release key.
type signer struct {
	entity *openpgp.Entity
	public []byte
}

func newSigner(t *testing.T) signer {
	t.Helper()
	e, err := openpgp.NewEntity("test", "", "release@example.invalid", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	require.NoError(t, err)
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	require.NoError(t, err)
	require.NoError(t, e.Serialize(w))
	require.NoError(t, w.Close())
	return signer{entity: e, public: buf.Bytes()}
}

func (s signer) sign(t *testing.T, data []byte) []byte {
	t.Helper()
	var sig bytes.Buffer
	require.NoError(t, openpgp.DetachSign(&sig, s.entity, bytes.NewReader(data), nil))
	return sig.Bytes()
}

type tarEntry struct {
	name     string
	body     string
	typeflag byte
}

func makeArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: e.typeflag}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag != tar.TypeReg {
			hdr.Size = 0
			hdr.Linkname = "/etc/passwd"
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if hdr.Typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
