package sessionarchive

import (
	"bufio"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

const (
	CodecFormatVersion = 1
	codecMagic         = "SAR1"
	defaultChunkSize   = 64 * 1024
	maxKeyIDLength     = 128
)

var safeKeyID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type EncodingInfo struct {
	StoredPlaintextSHA256 string `json:"stored_plaintext_sha256"`
	StoredBytes           int64  `json:"stored_bytes"`
	CompressedBytes       int64  `json:"compressed_bytes"`
	CiphertextBytes       int64  `json:"ciphertext_bytes"`
	GZIPVersion           int    `json:"gzip_version"`
	FormatVersion         int    `json:"format_version"`
	KeyID                 string `json:"key_id"`
}

type Codec struct {
	keys          map[string][]byte
	activeKeyID   string
	chunkSize     int
	maxPlainBytes int64
	tempDir       string
}

func NewCodec(keys map[string][]byte, activeKeyID string, chunkSize int, maxPlainBytes int64, tempDir string) (*Codec, error) {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	if chunkSize > 4*1024*1024 || maxPlainBytes < 1 {
		return nil, errors.New("invalid codec size limit")
	}
	activeKeyID = strings.TrimSpace(activeKeyID)
	if !safeKeyID.MatchString(activeKeyID) || len(activeKeyID) > maxKeyIDLength {
		return nil, errors.New("invalid active key ID")
	}
	copyKeys := make(map[string][]byte, len(keys))
	for id, key := range keys {
		if !safeKeyID.MatchString(id) || len(id) > maxKeyIDLength || len(key) != 32 {
			return nil, fmt.Errorf("invalid encryption key %q", id)
		}
		copyKeys[id] = append([]byte(nil), key...)
	}
	if _, ok := copyKeys[activeKeyID]; !ok {
		return nil, errors.New("active key is missing")
	}
	return &Codec{keys: copyKeys, activeKeyID: activeKeyID, chunkSize: chunkSize, maxPlainBytes: maxPlainBytes, tempDir: tempDir}, nil
}

// Encode 执行 plaintext -> gzip -> 分块 AES-256-GCM，正文不会被二次聚合到内存。
func (c *Codec) Encode(src io.Reader, dst io.Writer) (EncodingInfo, error) {
	key := c.keys[c.activeKeyID]
	block, err := aes.NewCipher(key)
	if err != nil {
		return EncodingInfo{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return EncodingInfo{}, err
	}
	countedDst := &countingWriter{writer: dst}
	encrypted, err := newChunkEncryptWriter(countedDst, aead, c.activeKeyID, c.chunkSize)
	if err != nil {
		return EncodingInfo{}, err
	}
	compressed := &countingWriter{writer: encrypted}
	gzipWriter, err := gzip.NewWriterLevel(compressed, gzip.BestSpeed)
	if err != nil {
		return EncodingInfo{}, err
	}
	hasher := sha256.New()
	plaintext := &countingWriter{writer: io.MultiWriter(gzipWriter, hasher)}
	written, copyErr := io.Copy(plaintext, io.LimitReader(src, c.maxPlainBytes+1))
	if copyErr == nil && written > c.maxPlainBytes {
		copyErr = errors.New("plaintext exceeds codec limit")
	}
	closeErr := gzipWriter.Close()
	encryptCloseErr := encrypted.Close()
	if copyErr != nil {
		return EncodingInfo{}, copyErr
	}
	if closeErr != nil {
		return EncodingInfo{}, closeErr
	}
	if encryptCloseErr != nil {
		return EncodingInfo{}, encryptCloseErr
	}
	return EncodingInfo{
		StoredPlaintextSHA256: hex.EncodeToString(hasher.Sum(nil)), StoredBytes: written,
		CompressedBytes: compressed.count, CiphertextBytes: countedDst.count,
		GZIPVersion: 1, FormatVersion: CodecFormatVersion, KeyID: c.activeKeyID,
	}, nil
}

// Decode 在受限临时文件内完成全部认证、解压上限和明文 hash 校验，成功后才写入 dst。
// 因而认证失败不会向管理员返回部分明文。
func (c *Codec) Decode(src io.Reader, dst io.Writer, expected EncodingInfo) error {
	countedSrc := &countingReader{reader: src}
	reader, keyID, err := c.newDecryptReader(countedSrc)
	if err != nil {
		return err
	}
	if expected.KeyID != "" && expected.KeyID != keyID {
		return errors.New("archive key ID mismatch")
	}
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	tmp, err := os.CreateTemp(c.tempDir, "session-archive-decode-*")
	if err != nil {
		_ = gzipReader.Close()
		return err
	}
	name := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(name) }()
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(gzipReader, c.maxPlainBytes+1))
	closeErr := gzipReader.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > c.maxPlainBytes {
		return errors.New("decompressed plaintext exceeds limit")
	}
	if expected.StoredBytes > 0 && written != expected.StoredBytes {
		return errors.New("archive plaintext length mismatch")
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if expected.StoredPlaintextSHA256 != "" && !strings.EqualFold(actualHash, expected.StoredPlaintextSHA256) {
		return errors.New("archive plaintext hash mismatch")
	}
	if expected.CiphertextBytes > 0 && countedSrc.count != expected.CiphertextBytes {
		return errors.New("archive ciphertext length mismatch")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(dst, tmp)
	return err
}

func (c *Codec) newDecryptReader(src io.Reader) (io.Reader, string, error) {
	buffered := bufio.NewReader(src)
	header := make([]byte, len(codecMagic)+1+2)
	if _, err := io.ReadFull(buffered, header); err != nil {
		return nil, "", fmt.Errorf("read archive header: %w", err)
	}
	if string(header[:len(codecMagic)]) != codecMagic || int(header[len(codecMagic)]) != CodecFormatVersion {
		return nil, "", errors.New("unsupported archive format")
	}
	keyLen := int(binary.BigEndian.Uint16(header[len(codecMagic)+1:]))
	if keyLen < 1 || keyLen > maxKeyIDLength {
		return nil, "", errors.New("invalid archive key ID length")
	}
	keyIDBytes := make([]byte, keyLen)
	if _, err := io.ReadFull(buffered, keyIDBytes); err != nil {
		return nil, "", err
	}
	keyID := string(keyIDBytes)
	key, ok := c.keys[keyID]
	if !ok {
		return nil, "", fmt.Errorf("archive key %q is unavailable", keyID)
	}
	var chunkSize uint32
	if err := binary.Read(buffered, binary.BigEndian, &chunkSize); err != nil {
		return nil, "", err
	}
	if chunkSize < 1 || chunkSize > 4*1024*1024 {
		return nil, "", errors.New("invalid encrypted chunk size")
	}
	noncePrefix := make([]byte, 8)
	if _, err := io.ReadFull(buffered, noncePrefix); err != nil {
		return nil, "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	return &chunkDecryptReader{src: buffered, aead: aead, keyID: keyID, noncePrefix: noncePrefix, maxFrame: int(chunkSize) + aead.Overhead()}, keyID, nil
}

func CASObjectKey(prefix string, info EncodingInfo) (string, error) {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" || prefix == "." || strings.Contains(prefix, "..") || strings.HasPrefix(prefix, "/") {
		return "", errors.New("invalid archive object prefix")
	}
	if !safeKeyID.MatchString(info.KeyID) || len(info.StoredPlaintextSHA256) != 64 {
		return "", errors.New("invalid archive CAS identity")
	}
	if _, err := hex.DecodeString(info.StoredPlaintextSHA256); err != nil {
		return "", errors.New("invalid archive plaintext hash")
	}
	return path.Join(prefix, fmt.Sprintf("v%d", info.FormatVersion), info.KeyID, info.StoredPlaintextSHA256[:2], info.StoredPlaintextSHA256+".sar"), nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += int64(n)
	return n, err
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.count += int64(n)
	return n, err
}

type chunkEncryptWriter struct {
	dst         io.Writer
	aead        cipher.AEAD
	keyID       string
	noncePrefix []byte
	chunkSize   int
	buffer      []byte
	index       uint32
	closed      bool
}

func newChunkEncryptWriter(dst io.Writer, aead cipher.AEAD, keyID string, chunkSize int) (*chunkEncryptWriter, error) {
	noncePrefix := make([]byte, 8)
	if _, err := rand.Read(noncePrefix); err != nil {
		return nil, err
	}
	header := make([]byte, 0, len(codecMagic)+1+2+len(keyID)+4+8)
	header = append(header, codecMagic...)
	header = append(header, byte(CodecFormatVersion))
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(keyID)))
	header = append(header, length...)
	header = append(header, keyID...)
	chunk := make([]byte, 4)
	binary.BigEndian.PutUint32(chunk, uint32(chunkSize))
	header = append(header, chunk...)
	header = append(header, noncePrefix...)
	if _, err := dst.Write(header); err != nil {
		return nil, err
	}
	return &chunkEncryptWriter{dst: dst, aead: aead, keyID: keyID, noncePrefix: noncePrefix, chunkSize: chunkSize, buffer: make([]byte, 0, chunkSize)}, nil
}

func (w *chunkEncryptWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("encrypted writer closed")
	}
	written := len(p)
	for len(p) > 0 {
		space := w.chunkSize - len(w.buffer)
		if space > len(p) {
			space = len(p)
		}
		w.buffer = append(w.buffer, p[:space]...)
		p = p[space:]
		if len(w.buffer) == w.chunkSize {
			if err := w.flush(false); err != nil {
				return written - len(p), err
			}
		}
	}
	return written, nil
}

func (w *chunkEncryptWriter) Close() error {
	if w.closed {
		return nil
	}
	if len(w.buffer) > 0 {
		if err := w.flush(false); err != nil {
			return err
		}
	}
	if err := w.flush(true); err != nil {
		return err
	}
	w.closed = true
	return nil
}

func (w *chunkEncryptWriter) flush(final bool) error {
	nonce := make([]byte, w.aead.NonceSize())
	copy(nonce, w.noncePrefix)
	binary.BigEndian.PutUint32(nonce[len(nonce)-4:], w.index)
	plaintext := w.buffer
	if final {
		plaintext = nil
	}
	sealed := w.aead.Seal(nil, nonce, plaintext, chunkAAD(w.keyID, w.index, final))
	if err := binary.Write(w.dst, binary.BigEndian, uint32(len(sealed))); err != nil {
		return err
	}
	if _, err := w.dst.Write(sealed); err != nil {
		return err
	}
	w.buffer = w.buffer[:0]
	w.index++
	return nil
}

type chunkDecryptReader struct {
	src         *bufio.Reader
	aead        cipher.AEAD
	keyID       string
	noncePrefix []byte
	maxFrame    int
	index       uint32
	buffer      []byte
	final       bool
}

func (r *chunkDecryptReader) Read(p []byte) (int, error) {
	for len(r.buffer) == 0 {
		if r.final {
			return 0, io.EOF
		}
		var frameLen uint32
		if err := binary.Read(r.src, binary.BigEndian, &frameLen); err != nil {
			return 0, fmt.Errorf("read encrypted frame: %w", err)
		}
		if frameLen < uint32(r.aead.Overhead()) || frameLen > uint32(r.maxFrame) {
			return 0, errors.New("invalid encrypted frame length")
		}
		sealed := make([]byte, frameLen)
		if _, err := io.ReadFull(r.src, sealed); err != nil {
			return 0, err
		}
		nonce := make([]byte, r.aead.NonceSize())
		copy(nonce, r.noncePrefix)
		binary.BigEndian.PutUint32(nonce[len(nonce)-4:], r.index)
		final := frameLen == uint32(r.aead.Overhead())
		plaintext, err := r.aead.Open(nil, nonce, sealed, chunkAAD(r.keyID, r.index, final))
		if err != nil {
			return 0, errors.New("archive authentication failed")
		}
		r.index++
		if final {
			if len(plaintext) != 0 {
				return 0, errors.New("invalid terminal frame")
			}
			if _, err := r.src.Peek(1); !errors.Is(err, io.EOF) {
				return 0, errors.New("trailing encrypted data")
			}
			r.final = true
			continue
		}
		r.buffer = plaintext
	}
	n := copy(p, r.buffer)
	r.buffer = r.buffer[n:]
	return n, nil
}

func chunkAAD(keyID string, index uint32, final bool) []byte {
	aad := make([]byte, 0, len(codecMagic)+1+len(keyID)+4+1)
	aad = append(aad, codecMagic...)
	aad = append(aad, byte(CodecFormatVersion))
	aad = append(aad, keyID...)
	indexBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(indexBytes, index)
	aad = append(aad, indexBytes...)
	if final {
		aad = append(aad, 1)
	} else {
		aad = append(aad, 0)
	}
	return aad
}
