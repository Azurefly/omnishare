package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
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
//	magic | header length(4, BE) | header JSON | chunk...
//
// Each chunk is length(4, BE) | AES-256-GCM ciphertext. Version 3 binds the
// complete header digest, sequence number and record type to every GCM tag.
// A zero length followed by an authenticated empty final record ends the
// stream; EOF is required immediately afterwards. Version 2 remains readable
// for existing backups, but only new v3 packages authenticate all metadata and
// the end of the stream.
var backupMagic = []byte("OMNISHARE-ENC-BK")

const (
	backupFormatVersion       = 3
	legacyBackupFormatVersion = 2
	backupChunkSize           = 1 << 20
	backupKDFIterations       = 210_000
	backupMaxKDFIterations    = 1_000_000
	backupSaltSize            = 16
	backupNoncePrefix         = 8
	backupMaxHeaderSize       = 8 << 20
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

func backupAEAD(password string, salt []byte, iterations int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkNonce(prefix []byte, index uint32) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[backupNoncePrefix:], index)
	return nonce
}

type backupEncryptWriter struct {
	w            io.Writer
	aead         cipher.AEAD
	prefix       []byte
	buf          []byte
	index        uint32
	closed       bool
	err          error
	headerDigest [sha256.Size]byte
}

func backupRecordAAD(digest [sha256.Size]byte, index uint32, final bool) []byte {
	aad := append([]byte("omnishare-backup-v3:"), digest[:]...)
	var sequence [4]byte
	binary.BigEndian.PutUint32(sequence[:], index)
	aad = append(aad, sequence[:]...)
	if final {
		return append(aad, 1)
	}
	return append(aad, 0)
}

func writeBackupBytes(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
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
	headerBytes := append([]byte(nil), backupMagic...)
	headerBytes = append(headerBytes, length[:]...)
	headerBytes = append(headerBytes, headerJSON...)
	aead, err := backupAEAD(password, salt, header.Iterations)
	if err != nil {
		return nil, err
	}
	if err := writeBackupBytes(w, headerBytes); err != nil {
		return nil, err
	}
	return &backupEncryptWriter{
		w:            w,
		aead:         aead,
		prefix:       prefix,
		buf:          make([]byte, 0, backupChunkSize),
		headerDigest: sha256.Sum256(headerBytes),
	}, nil
}

func (b *backupEncryptWriter) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if b.closed {
		return 0, io.ErrClosedPipe
	}
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
				b.err = err
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
	// Reserve the final nonce for the authenticated end record.
	if b.index == ^uint32(0) {
		return errors.New("encrypted backup contains too many chunks")
	}
	aad := backupRecordAAD(b.headerDigest, b.index, false)
	sealed := b.aead.Seal(nil, chunkNonce(b.prefix, b.index), b.buf, aad)
	b.index++
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(sealed)))
	if err := writeBackupBytes(b.w, length[:]); err != nil {
		return err
	}
	if err := writeBackupBytes(b.w, sealed); err != nil {
		return err
	}
	b.buf = b.buf[:0]
	return nil
}

// Close flushes the tail and authenticates the end-of-stream marker.
func (b *backupEncryptWriter) Close() error {
	if b.closed {
		return b.err
	}
	b.closed = true
	if b.err == nil {
		b.err = b.flushChunk()
	}
	if b.err != nil {
		return b.err
	}
	var terminator [4]byte
	tag := b.aead.Seal(nil, chunkNonce(b.prefix, b.index), nil, backupRecordAAD(b.headerDigest, b.index, true))
	b.err = writeBackupBytes(b.w, terminator[:])
	if b.err == nil {
		b.err = writeBackupBytes(b.w, tag)
	}
	return b.err
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
	if (header.FormatVersion != backupFormatVersion && header.FormatVersion != legacyBackupFormatVersion) || header.Cipher != "AES-256-GCM" || header.KDF != "PBKDF2-HMAC-SHA256" {
		return header, nil, errors.New("unsupported encrypted backup format")
	}
	// Parameters are untrusted until GCM verification. Bound KDF work before
	// deriving a key, and only accept the chunk size emitted by either writer.
	if header.ChunkSize != backupChunkSize || header.Iterations < 100_000 || header.Iterations > backupMaxKDFIterations {
		return header, nil, errors.New("encrypted backup header parameters are invalid")
	}
	headerBytes := make([]byte, 0, len(backupMagic)+4+len(headerJSON))
	headerBytes = append(headerBytes, backupMagic...)
	headerBytes = append(headerBytes, length[:]...)
	headerBytes = append(headerBytes, headerJSON...)
	return header, headerBytes, nil
}

// DecryptBackup streams an encrypted package into dst. The derived key never
// leaves this function. New v3 headers and the final record are authenticated;
// legacy v2 archives retain their original chunk-only authentication.
func DecryptBackup(src io.Reader, dst io.Writer, password string) (BackupHeader, error) {
	header, headerBytes, err := readBackupHeader(src)
	if err != nil {
		return header, err
	}
	if password == "" {
		return header, errors.New("this backup is encrypted; a password is required")
	}
	salt, err := base64.StdEncoding.DecodeString(header.Salt)
	if err != nil || len(salt) != backupSaltSize {
		return header, errors.New("encrypted backup salt is invalid")
	}
	prefix, err := base64.StdEncoding.DecodeString(header.NoncePrefix)
	if err != nil || len(prefix) != backupNoncePrefix {
		return header, errors.New("encrypted backup nonce is invalid")
	}
	aead, err := backupAEAD(password, salt, header.Iterations)
	if err != nil {
		return header, err
	}
	headerSize := len(headerBytes)
	headerDigest := sha256.Sum256(headerBytes)
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
			if header.FormatVersion == backupFormatVersion {
				tag := make([]byte, aead.Overhead())
				if _, err := io.ReadFull(src, tag); err != nil {
					return header, errors.New("encrypted backup final record is truncated")
				}
				if _, err := aead.Open(nil, chunkNonce(prefix, index), tag, backupRecordAAD(headerDigest, index, true)); err != nil {
					return header, errors.New("backup password is wrong or the final record was tampered with")
				}
			} else if index == 0 {
				return header, errors.New("legacy encrypted backup contains no authenticated data")
			}
			var extra [1]byte
			if n, err := io.ReadFull(src, extra[:]); n != 0 || !errors.Is(err, io.EOF) {
				return header, errors.New("encrypted backup contains trailing data")
			}
			return header, nil
		}
		if index == ^uint32(0) {
			return header, errors.New("encrypted backup contains too many chunks")
		}
		if size < 16 || size > backupChunkSize+16 {
			return header, errors.New("encrypted backup chunk has an invalid size")
		}
		sealed := make([]byte, size)
		if _, err := io.ReadFull(src, sealed); err != nil {
			return header, errors.New("encrypted backup is truncated")
		}
		aad := []byte(fmt.Sprintf("omnishare-backup-chunk:%d:%d", headerSize, index))
		if header.FormatVersion == backupFormatVersion {
			aad = backupRecordAAD(headerDigest, index, false)
		}
		plain, err := aead.Open(nil, chunkNonce(prefix, index), sealed, aad)
		if err != nil {
			return header, errors.New("backup password is wrong or the package was tampered with")
		}
		index++
		if err := writeBackupBytes(dst, plain); err != nil {
			return header, err
		}
	}
}
