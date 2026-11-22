package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"omnishare/internal/durable"
)

const documentFileMarker = "@omnishare-markdown:"

var markdownFileName = regexp.MustCompile("^[A-Za-z0-9_-]{1,160}--[0-9a-f]{64}[.]md$")
var documentFileID = regexp.MustCompile("^[A-Za-z0-9_-]{1,160}$")

func (s *Store) hydrateMarkdown(candidate *state) error {
	read := func(dir, value string) (string, error) {
		if !strings.HasPrefix(value, documentFileMarker) {
			return value, nil
		}
		name := strings.TrimPrefix(value, documentFileMarker)
		if !markdownFileName.MatchString(name) {
			return "", errors.New("invalid Markdown filename in workspace index")
		}
		data, err := os.ReadFile(filepath.Join(s.dataDir, dir, name))
		if err != nil {
			return "", fmt.Errorf("read Markdown %s: %w", name, err)
		}
		return string(data), nil
	}
	for i := range candidate.Notes {
		n := &candidate.Notes[i]
		content, err := read("notes", n.Content)
		if err != nil {
			return err
		}
		n.Content = content
	}
	for i := range candidate.Pads {
		p := &candidate.Pads[i]
		content, err := read("documents", p.Content)
		if err != nil {
			return err
		}
		p.Content = content
	}
	return nil
}

func (s *Store) writeMarkdownFiles(candidate state) (state, error) {
	// Each index generation points at immutable Markdown filenames. The commit
	// switches the references together. Markdown for the backup stays readable.
	index := cloneState(candidate)
	write := func(dir, id, content string) (string, error) {
		if !documentFileID.MatchString(id) {
			return "", errors.New("invalid note or document identifier")
		}
		hash := sha256.Sum256([]byte(content))
		name := id + "--" + hex.EncodeToString(hash[:]) + ".md"
		file := filepath.Join(s.dataDir, dir, name)
		if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
			if err := durable.WriteFile(file, []byte(content), 0o600); err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		}
		return documentFileMarker + name, nil
	}
	for i := range index.Notes {
		n := &index.Notes[i]
		reference, err := write("notes", n.ID, n.Content)
		if err != nil {
			return state{}, err
		}
		n.Content = reference
	}
	for i := range index.Pads {
		p := &index.Pads[i]
		reference, err := write("documents", p.ID, p.Content)
		if err != nil {
			return state{}, err
		}
		p.Content = reference
	}
	return index, nil
}
