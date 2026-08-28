package sessionarchive

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func testCodec(t *testing.T, max int64) *Codec {
	t.Helper()
	codec, err := NewCodec(map[string][]byte{"key-1": []byte("0123456789abcdef0123456789abcdef")}, "key-1", 32, max, t.TempDir())
	require.NoError(t, err)
	return codec
}

func TestCodecRoundTripAndCASKey(t *testing.T) {
	codec := testCodec(t, 1<<20)
	plaintext := bytes.Repeat([]byte("archive event\n"), 1000)
	var ciphertext bytes.Buffer
	info, err := codec.Encode(bytes.NewReader(plaintext), &ciphertext)
	require.NoError(t, err)
	require.Equal(t, int64(len(plaintext)), info.StoredBytes)
	require.Equal(t, int64(ciphertext.Len()), info.CiphertextBytes)
	var decoded bytes.Buffer
	require.NoError(t, codec.Decode(bytes.NewReader(ciphertext.Bytes()), &decoded, info))
	require.Equal(t, plaintext, decoded.Bytes())
	key, err := CASObjectKey("session-archive", info)
	require.NoError(t, err)
	require.Contains(t, key, "/v1/key-1/")
}

func TestCodecRejectsCorruptionWithoutPartialPlaintext(t *testing.T) {
	codec := testCodec(t, 1<<20)
	var ciphertext bytes.Buffer
	info, err := codec.Encode(bytes.NewReader([]byte("sensitive plaintext")), &ciphertext)
	require.NoError(t, err)
	corrupt := append([]byte(nil), ciphertext.Bytes()...)
	corrupt[len(corrupt)-1] ^= 0xff
	var decoded bytes.Buffer
	require.Error(t, codec.Decode(bytes.NewReader(corrupt), &decoded, info))
	require.Zero(t, decoded.Len())
}

func TestCodecRejectsTrailingDataAndCiphertextLengthMismatch(t *testing.T) {
	codec := testCodec(t, 1<<20)
	var ciphertext bytes.Buffer
	info, err := codec.Encode(bytes.NewReader([]byte("payload")), &ciphertext)
	require.NoError(t, err)
	trailing := append(append([]byte(nil), ciphertext.Bytes()...), 1, 2, 3)
	var decoded bytes.Buffer
	require.ErrorContains(t, codec.Decode(bytes.NewReader(trailing), &decoded, info), "trailing")
	require.Zero(t, decoded.Len())
	wrong := info
	wrong.CiphertextBytes++
	require.ErrorContains(t, codec.Decode(bytes.NewReader(ciphertext.Bytes()), &decoded, wrong), "ciphertext length")
}

func TestCodecRejectsWrongKeyAndDecompressionLimit(t *testing.T) {
	codec := testCodec(t, 1<<20)
	var ciphertext bytes.Buffer
	info, err := codec.Encode(bytes.NewReader(bytes.Repeat([]byte("x"), 1024)), &ciphertext)
	require.NoError(t, err)
	wrongCodec, err := NewCodec(map[string][]byte{"key-1": []byte("abcdef0123456789abcdef0123456789")}, "key-1", 32, 1<<20, t.TempDir())
	require.NoError(t, err)
	require.Error(t, wrongCodec.Decode(bytes.NewReader(ciphertext.Bytes()), &bytes.Buffer{}, info))
	smallCodec, err := NewCodec(map[string][]byte{"key-1": []byte("0123456789abcdef0123456789abcdef")}, "key-1", 32, 16, t.TempDir())
	require.NoError(t, err)
	require.ErrorContains(t, smallCodec.Decode(bytes.NewReader(ciphertext.Bytes()), &bytes.Buffer{}, info), "exceeds")
}
