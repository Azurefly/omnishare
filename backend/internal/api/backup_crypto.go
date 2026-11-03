package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"omnishare/internal/model"
)

// Encrypted backups use a small self-describing container:
//
//	magic(16) | header length(4, BE) | header JSON | chunk...
//
// Each chunk is length(4, BE) | AES-256-GCM ciphertext. A zero length marks the
// end of the stream, so the reader never needs the plaintext size in advance.
// The header is used as additional authenticated data and carries the KDF
// parameters, nonce prefix, creation time and the same manifest that is stored
// inside the ZIP, so a truncated or tampered package fails before extraction.
var backupMagic = []byte("OMNISHARE-ENC-BK")

const (
	backupFormatVersion = 2
	backupChunkSize     = 1 << 20
	backupKDFIterations = 210_000
	backupSaltSize      = 16
	backupNoncePrefix   = 8
	backupMaxHeaderSize = 8 << 20
)

type BackupHeader struct {
	FormatVersion int                  `json:"format_version"`
	Cipher        string               `json:"cipher"`
	KDF           string               `json:"kdf"`
	Iterations    int                  `json:"iterations"`
	Salt          string               `json:"salt"`
	NoncePrefix   string               `json:"nonce_prefix"`
	ChunkSize     int                  `json:"chunk_size"`
	CreatedAt     time.Time            `json:"created_at"`
	AppVersion    string               `json:"app_version"`
	Manifest      model.BackupManifest `json:"manifest"`
}

// pbkdf2SHA256 implements PBKDF2-HMAC-SHA256 without pulling in a new module.
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	hashLen := sha256.Size
	blocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	var counter [4]byte
	for block := 1; block <= blocks; block++ {
		binary.BigEndian.PutUint32(counter[:], uint32(block))
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write(counter[:])
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func deriveBackupKey(password string, salt []byte, iterations int) []byte {
	return pbkdf2SHA256([]byte(password), salt, iterations, 32)
}

func chunkNonce(prefix []byte, index uint32) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[backupNoncePrefix:], index)
	return nonce
}

type backupEncryptWriter struct {
	w          io.Writer
	aead       cipher.AEAD
	prefix     []byte
	buf        []byte
	index      uint32
	closed     bool
	headerSize int
}

// NewBackupEncryptWriter writes the header immediately and encrypts subsequent
// bytes as fixed-size authenticated chunks.
func NewBackupEncryptWriter(w io.Writer, password string, manifest model.BackupManifest) (*backupEncryptWriter, error) {
	if password == "" {
		return nil, errors.New("backup password is required")
	}
	salt := make([]byte, backupSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	prefix := make([]byte, backupNoncePrefix)
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	header := BackupHeader{
		FormatVersion: backupFormatVersion,
		Cipher:        "AES-256-GCM",
		KDF:           "PBKDF2-HMAC-SHA256",
		Iterations:    backupKDFIterations,
		Salt:          base64.StdEncoding.EncodeToString(salt),
		NoncePrefix:   base64.StdEncoding.EncodeToString(prefix),
		ChunkSize:     backupChunkSize,
		CreatedAt:     time.Now().UTC(),
		AppVersion:    Version,
		Manifest:      manifest,
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	if len(headerJSON) > backupMaxHeaderSize {
		return nil, errors.New("backup header is too large")
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(headerJSON)))
	if _, err := w.Write(backupMagic); err != nil {
		return nil, err
	}
	if _, err := w.Write(length[:]); err != nil {
		return nil, err
	}
	if _, err := w.Write(headerJSON); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(deriveBackupKey(password, salt, header.Iterations))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &backupEncryptWriter{
		w:          w,
		aead:       aead,
		prefix:     prefix,
		buf:        make([]byte, 0, backupChunkSize),
		headerSize: len(backupMagic) + 4 + len(headerJSON),
	}, nil
}

func (b *backupEncryptWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		space := backupChunkSize - len(b.buf)
		if space > len(p) {
			space = len(p)
		}
		b.buf = append(b.buf, p[:space]...)
		p = p[space:]
		written += space
		if len(b.buf) == backupChunkSize {
			if err := b.flushChunk(); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

func (b *backupEncryptWriter) flushChunk() error {
	if len(b.buf) == 0 {
		return nil
	}
	aad := []byte(fmt.Sprintf("omnishare-backup-chunk:%d:%d", b.headerSize, b.index))
	sealed := b.aead.Seal(nil, chunkNonce(b.prefix, b.index), b.buf, aad)
	b.index++
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(sealed)))
	if _, err := b.w.Write(length[:]); err != nil {
		return err
	}
	if _, err := b.w.Write(sealed); err != nil {
		return err
	}
	b.buf = b.buf[:0]
	return nil
}

// Close flushes the tail and writes the end-of-stream marker.
func (b *backupEncryptWriter) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	if err := b.flushChunk(); err != nil {
		return err
	}
	var terminator [4]byte
	_, err := b.w.Write(terminator[:])
	return err
}

// IsEncryptedBackup reports whether the file starts with the container magic.
func IsEncryptedBackup(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, len(backupMagic))
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	return n == len(backupMagic) && bytes.Equal(head, backupMagic), nil
}

func readBackupHeader(r io.Reader) (BackupHeader, []byte, error) {
	var header BackupHeader
	magic := make([]byte, len(backupMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return header, nil, errors.New("backup is not an encrypted package")
	}
	if !bytes.Equal(magic, backupMagic) {
		return header, nil, errors.New("backup is not an encrypted package")
	}
	var length [4]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return header, nil, errors.New("encrypted backup header is truncated")
	}
	size := binary.BigEndian.Uint32(length[:])
	if size == 0 || size > backupMaxHeaderSize {
		return header, nil, errors.New("encrypted backup header has an invalid size")
	}
	headerJSON := make([]byte, size)
	if _, err := io.ReadFull(r, headerJSON); err != nil {
		return header, nil, errors.New("encrypted backup header is truncated")
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return header, nil, errors.New("encrypted backup header is invalid")
	}
	if header.FormatVersion != backupFormatVersion || header.Cipher != "AES-256-GCM" || header.KDF != "PBKDF2-HMAC-SHA256" {
		return header, nil, errors.New("unsupported encrypted backup format")
	}
	if header.ChunkSize < 1 || header.ChunkSize > 64<<20 || header.Iterations < 100_000 {
		return header, nil, errors.New("encrypted backup header parameters are invalid")
	}
	headerBytes := make([]byte, 0, len(backupMagic)+4+len(headerJSON))
	headerBytes = append(headerBytes, backupMagic...)
	headerBytes = append(headerBytes, length[:]...)
	headerBytes = append(headerBytes, headerJSON...)
	return header, headerBytes, nil
}

// DecryptBackup streams an encrypted package into dst. The derived key never
// leaves this function; the caller only receives the authenticated header.
func DecryptBackup(src io.Reader, dst io.Writer, password string) (BackupHeader, error) {
	header, headerBytes, err := readBackupHeader(src)
	if err != nil {
		return header, err
	}
	if password == "" {
		return header, errors.New("this backup is encrypted; a password is required")
	}
	salt, err := base64.StdEncoding.DecodeString(header.Salt)
	if err != nil || len(salt) < 8 {
		return header, errors.New("encrypted backup salt is invalid")
	}
	prefix, err := base64.StdEncoding.DecodeString(header.NoncePrefix)
	if err != nil || len(prefix) != backupNoncePrefix {
		return header, errors.New("encrypted backup nonce is invalid")
	}
	block, err := aes.NewCipher(deriveBackupKey(password, salt, header.Iterations))
	if err != nil {
		return header, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return header, err
	}
	headerSize := len(headerBytes)
	var index uint32
	var length [4]byte
	for {
		if _, err := io.ReadFull(src, length[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return header, errors.New("encrypted backup is truncated")
			}
			return header, err
		}
		size := binary.BigEndian.Uint32(length[:])
		if size == 0 {
			return header, nil
		}
		if size < 16 || size > backupChunkSize+16 {
			return header, errors.New("encrypted backup chunk has an invalid size")
		}
		sealed := make([]byte, size)
		if _, err := io.ReadFull(src, sealed); err != nil {
			return header, errors.New("encrypted backup is truncated")
		}
		aad := []byte(fmt.Sprintf("omnishare-backup-chunk:%d:%d", headerSize, index))
		plain, err := aead.Open(nil, chunkNonce(prefix, index), sealed, aad)
		if err != nil {
			return header, errors.New("backup password is wrong or the package was tampered with")
		}
		index++
		if _, err := dst.Write(plain); err != nil {
			return header, err
		}
	}
}
