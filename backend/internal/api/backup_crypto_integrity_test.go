package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
)

func encryptedTestPayload(t *testing.T, payload []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewBackupEncryptWriter(&out, "integrity-test-password", modelManifest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func replaceEncryptedHeader(t *testing.T, archive []byte, edit func(*BackupHeader)) []byte {
	t.Helper()
	start := len(backupMagic) + 4
	end := start + int(binary.BigEndian.Uint32(archive[len(backupMagic):start]))
	var header BackupHeader
	if err := json.Unmarshal(archive[start:end], &header); err != nil {
		t.Fatal(err)
	}
	edit(&header)
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	out.Write(backupMagic)
	if err := binary.Write(&out, binary.BigEndian, uint32(len(raw))); err != nil {
		t.Fatal(err)
	}
	out.Write(raw)
	out.Write(archive[end:])
	return out.Bytes()
}

func TestEncryptedBackupAuthenticatesCompleteContainer(t *testing.T) {
	archive := encryptedTestPayload(t, bytes.Repeat([]byte("x"), backupChunkSize+17))
	start := len(backupMagic) + 4
	firstChunk := start + int(binary.BigEndian.Uint32(archive[len(backupMagic):start]))
	secondChunk := firstChunk + 4 + int(binary.BigEndian.Uint32(archive[firstChunk:firstChunk+4]))
	cases := map[string][]byte{
		"same-length header change": replaceEncryptedHeader(t, archive, func(h *BackupHeader) {
			h.AppVersion = "X" + h.AppVersion[1:]
		}),
		"early terminator": append(append([]byte(nil), archive[:secondChunk]...), 0, 0, 0, 0),
		"trailing bytes":   append(append([]byte(nil), archive...), []byte("unexpected suffix")...),
	}
	for name, altered := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecryptBackup(bytes.NewReader(altered), io.Discard, "integrity-test-password"); err == nil {
				t.Fatal("modified backup was accepted")
			}
		})
	}
}

func TestEncryptedEmptyBackupStillAuthenticatesPassword(t *testing.T) {
	archive := encryptedTestPayload(t, nil)
	if _, err := DecryptBackup(bytes.NewReader(archive), io.Discard, "wrong password"); err == nil {
		t.Fatal("empty encrypted stream accepted the wrong password")
	}
	if _, err := DecryptBackup(bytes.NewReader(archive), io.Discard, "integrity-test-password"); err != nil {
		t.Fatalf("valid empty stream failed: %v", err)
	}
}

func TestEncryptedBackupRejectsUnboundedKDFBeforeDerivation(t *testing.T) {
	archive := encryptedTestPayload(t, []byte("payload"))
	archive = replaceEncryptedHeader(t, archive, func(h *BackupHeader) { h.Iterations = 1_000_001 })
	if _, _, err := readBackupHeader(bytes.NewReader(archive)); err == nil {
		t.Fatal("untrusted KDF work factor has no upper bound")
	}
}

func TestEncryptedWriterRejectsWriteAfterClose(t *testing.T) {
	w, err := NewBackupEncryptWriter(io.Discard, "password", modelManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("lost payload")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after close = %d, %v", n, err)
	}
}

// This fixture uses the original v2 wire format independently of the current
// writer. Existing .osb files must remain readable after the format upgrade.
func TestEncryptedBackupReadsLegacyV2(t *testing.T) {
	const password = "legacy-backup-password"
	payload := []byte("legacy encrypted backup payload")
	salt := bytes.Repeat([]byte{0x53}, 16)
	prefix := bytes.Repeat([]byte{0x71}, 8)
	header := BackupHeader{
		FormatVersion: 2, Cipher: "AES-256-GCM", KDF: "PBKDF2-HMAC-SHA256",
		Iterations: 210_000, Salt: base64.StdEncoding.EncodeToString(salt),
		NoncePrefix: base64.StdEncoding.EncodeToString(prefix), ChunkSize: 1 << 20,
		AppVersion: "1.3.0-rc2", Manifest: modelManifest(),
	}
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, header.Iterations, 32)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	archive.Write(backupMagic)
	if err := binary.Write(&archive, binary.BigEndian, uint32(len(raw))); err != nil {
		t.Fatal(err)
	}
	archive.Write(raw)
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	sealed := aead.Seal(nil, nonce, payload, []byte(fmt.Sprintf("omnishare-backup-chunk:%d:0", archive.Len())))
	if err := binary.Write(&archive, binary.BigEndian, uint32(len(sealed))); err != nil {
		t.Fatal(err)
	}
	archive.Write(sealed)
	archive.Write([]byte{0, 0, 0, 0})
	var decoded bytes.Buffer
	if _, err := DecryptBackup(bytes.NewReader(archive.Bytes()), &decoded, password); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Bytes(), payload) {
		t.Fatal("legacy backup payload changed")
	}
}
