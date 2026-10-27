package sidecar

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	privatestorage "trill/sidecar/setup"
)

const (
	maxPhotoBytes        = 10 << 20
	maxPhotoPixels       = 40_000_000
	maxAttachmentBytes   = 250 << 20
	maxAttachmentEntries = 4096
)

var (
	errAttachmentQuota = errors.New("attachment storage quota reached")
	// Bound decoding memory and serialize quota checks across HTTP instances.
	// A directory flock additionally excludes writers in another sidecar process.
	attachmentGate = make(chan struct{}, 1)
)

type photoAttachment struct {
	Path        string `json:"path"`
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
	ByteCount   int    `json:"byteCount"`
}

func (h *HTTP) attachPhoto(w http.ResponseWriter, r *http.Request) {
	if !h.control(w) {
		return
	}
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid session ID")
		return
	}
	types := r.Header.Values("Content-Type")
	if len(types) != 1 || (types[0] != "image/jpeg" && types[0] != "image/png") || (r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity") {
		writeError(w, http.StatusUnsupportedMediaType, "raw image/jpeg or image/png required")
		return
	}
	if r.ContentLength > maxPhotoBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "photo exceeds 10 MiB")
		return
	}
	select {
	case attachmentGate <- struct{}{}:
		defer func() { <-attachmentGate }()
	case <-r.Context().Done():
		writeError(w, http.StatusServiceUnavailable, "attachment upload unavailable")
		return
	}
	if err := h.photoSession(r.Context(), id); err != nil {
		h.fail(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "photo exceeds 10 MiB")
		} else {
			writeError(w, http.StatusBadRequest, "invalid photo body")
		}
		return
	}
	if !validPhotoWithPixelLimit(data, types[0], h.photoPixelLimit) {
		message := "invalid photo or dimensions exceed 40 megapixels"
		if h.photoPixelLimit > 0 {
			message = fmt.Sprintf("invalid photo or dimensions exceed demo limit of %g megapixels; choose a smaller photo", float64(h.photoPixelLimit)/1_000_000)
		}
		writeError(w, http.StatusBadRequest, message)
		return
	}
	// Reading and decoding can be slow. Check eligibility again before persisting.
	if err := h.photoSession(r.Context(), id); err != nil {
		h.fail(w, err)
		return
	}
	if r.Context().Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "attachment upload unavailable")
		return
	}
	// A device can be revoked while its request body is still uploading.
	if deviceID := requestDevice(r); deviceID != "" {
		if h.store == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		current, err := h.store.Authenticate(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || current != deviceID {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}
	photo, err := h.storePhoto(id, types[0], data)
	switch {
	case errors.Is(err, errAttachmentQuota):
		writeError(w, http.StatusInsufficientStorage, "attachment storage quota reached")
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "attachment storage unavailable")
	default:
		writeJSON(w, http.StatusCreated, photo)
	}
}

func (h *HTTP) photoSession(ctx context.Context, id string) error {
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	h.service.mu.Lock()
	closed = closed || h.service.closed
	h.service.mu.Unlock()
	if closed {
		return errClosed
	}
	v, err := h.service.find(ctx, id)
	if err != nil {
		return err
	}
	if v.HiddenFromSidebar || v.Mode != "terminal" || !v.TerminalLive || !activeStatus(v.Status) || !herdrTerminalPattern.MatchString(v.HerdrTerminal) {
		return errNotAttachable
	}
	return nil
}

func validPhoto(data []byte, contentType string) bool {
	return validPhotoWithPixelLimit(data, contentType, 0)
}

func validPhotoWithPixelLimit(data []byte, contentType string, pixelLimit int64) bool {
	if pixelLimit <= 0 {
		pixelLimit = maxPhotoPixels
	}
	// Reject dimensions before Decode can allocate the full pixel buffer. A
	// 16-bit RGBA PNG may require eight bytes per pixel despite tiny compression.
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || "image/"+format != contentType || config.Width <= 0 || config.Height <= 0 || int64(config.Width) > pixelLimit/int64(config.Height) {
		return false
	}
	// DecodeConfig alone accepts truncated data and corrupt pixel streams.
	decoded, actualFormat, err := image.Decode(bytes.NewReader(data))
	return err == nil && actualFormat == format && decoded.Bounds().Dx() == config.Width && decoded.Bounds().Dy() == config.Height
}

func attachmentSessionDir(id string) string {
	hash := sha256.Sum256([]byte(id))
	return hex.EncodeToString(hash[:])
}

func (h *HTTP) storePhoto(id, contentType string, data []byte) (photoAttachment, error) {
	root := h.attachmentRoot
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return photoAttachment{}, err
		}
		root = filepath.Join(home, ".local", "share", "trill", "attachments")
	}
	if err := privatestorage.PrivateDir(root); err != nil {
		return photoAttachment{}, err
	}
	lock, err := os.Open(root)
	if err != nil {
		return photoAttachment{}, err
	}
	defer lock.Close()
	actual, err := lock.Stat()
	if err != nil {
		return photoAttachment{}, err
	}
	expected, err := os.Lstat(root)
	if err != nil || !os.SameFile(actual, expected) {
		return photoAttachment{}, errors.New("attachment directory changed")
	}
	// Fail closed when another process is uploading, without a retry or a wait
	// that could outlive the HTTP request deadline. Closing releases the flock.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return photoAttachment{}, err
	}
	limit := h.attachmentLimit
	if limit == 0 {
		limit = maxAttachmentBytes
	}
	if err := attachmentQuotaLimit(root, int64(len(data)), limit); err != nil {
		return photoAttachment{}, err
	}
	dir := filepath.Join(root, attachmentSessionDir(id))
	if err := privatestorage.PrivateDir(dir); err != nil {
		return photoAttachment{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return photoAttachment{}, err
	}
	ext := ".jpg"
	if contentType == "image/png" {
		ext = ".png"
	}
	name := "photo-" + hex.EncodeToString(random[:]) + ext
	path := filepath.Join(dir, name)
	// A private temporary file is synced, then linked exclusively into place.
	// No partial file is published and an existing file is never overwritten.
	if err := privatestorage.WritePrivate(path, data, false); err != nil {
		return photoAttachment{}, err
	}
	return photoAttachment{Path: path, FileName: name, ContentType: contentType, ByteCount: len(data)}, nil
}

func attachmentQuota(root string, incoming int64) error {
	return attachmentQuotaLimit(root, incoming, maxAttachmentBytes)
}

func attachmentQuotaLimit(root string, incoming, limit int64) error {
	var total int64
	entries := 0
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Getuid()) || (entry.IsDir() && info.Mode().Perm() != 0700) || (!entry.IsDir() && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600 || owner.Nlink != 1)) {
			return errors.New("unsafe attachment storage entry")
		}
		if path != root {
			entries++
		}
		if !entry.IsDir() {
			total += info.Size()
		}
		// Reserve entries for both a new session directory and its photo. Limit
		// tiny-file growth and scan cost as well as the total stored byte count.
		if entries > maxAttachmentEntries-2 || total > limit-incoming {
			return errAttachmentQuota
		}
		return nil
	})
}
