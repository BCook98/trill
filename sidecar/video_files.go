package sidecar

import (
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const maxVideoDownload int64 = 100 * 1024 * 1024

func (h *HTTP) serveVideo(w http.ResponseWriter, r *http.Request, project bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !h.control(w) {
		return
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		writeError(w, http.StatusBadRequest, "invalid resource ID")
		return
	}
	name, valid := fileRequestPath(r, true)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid relative path")
		return
	}
	rootPath, err := h.filesRoot(r.Context(), r.PathValue("id"), project)
	if err != nil {
		h.fail(w, err)
		return
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		filesReadError(w, err)
		return
	}
	defer root.Close()
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".m4v", ".mov":
	default:
		writeError(w, http.StatusUnsupportedMediaType, "video requires an MP4, M4V, or MOV file")
		return
	}
	file, err := openExplorerFile(root, name, false)
	if err != nil {
		filesReadError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		filesReadError(w, err)
		return
	}
	size := info.Size()
	if size > maxVideoDownload {
		writeError(w, http.StatusRequestEntityTooLarge, "video exceeds the 100 MiB (104857600 bytes) download limit; choose a smaller video")
		return
	}
	contentType := videoContentType(file, size, strings.EqualFold(path.Ext(name), ".mov"))
	if contentType == "" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported video header; expected MP4 or QuickTime media")
		return
	}
	// The API's 40-second write deadline is too short for videos over a
	// mobile connection. Keep a bounded video-specific transfer deadline.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, http.StatusServiceUnavailable, "video download unavailable")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	// ReadAt sniffing leaves this descriptor at offset zero. Copy exactly the
	// observed size: concurrent growth cannot bypass the cap. A short read or
	// write aborts the response so clients cannot cache an incomplete download.
	if _, err := io.CopyN(w, file, size); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// Header validation is bounded, not a codec/playability check. Atom sizes are
// checked against the observed descriptor size, including extended/EOF sizes.
// Seeking over mdat supports old QuickTime files whose moov follows the media.
func videoAtom(reader io.ReaderAt, offset, end int64) (kind string, payload, next int64, ok bool) {
	if offset < 0 || end-offset < 8 {
		return
	}
	var header [16]byte
	if _, err := reader.ReadAt(header[:8], offset); err != nil {
		return
	}
	size := uint64(binary.BigEndian.Uint32(header[:4]))
	headerSize := int64(8)
	switch size {
	case 0:
		size = uint64(end - offset)
	case 1:
		if end-offset < 16 {
			return
		}
		if _, err := reader.ReadAt(header[8:], offset+8); err != nil {
			return
		}
		size = binary.BigEndian.Uint64(header[8:])
		headerSize = 16
	}
	if size < uint64(headerSize) || size > uint64(end-offset) {
		return
	}
	return string(header[4:8]), offset + headerSize, offset + int64(size), true
}

func videoBrandType(brand string) string {
	switch brand {
	case "qt  ":
		return "video/quicktime"
	case "isom", "iso2", "iso3", "iso4", "iso5", "iso6", "iso7", "iso8", "iso9",
		"mp41", "mp42", "avc1", "M4V ", "M4VH", "M4VP", "MSNV", "dash":
		return "video/mp4"
	}
	return ""
}

func videoContentType(reader io.ReaderAt, size int64, legacyMOV bool) string {
	for offset, count := int64(0), 0; offset < size && count < 64; count++ {
		kind, payload, next, ok := videoAtom(reader, offset, size)
		if !ok {
			return ""
		}
		switch kind {
		case "ftyp":
			length := next - payload
			if length < 8 || length%4 != 0 {
				return ""
			}
			// Brand/version plus a bounded list of compatible brands. No media
			// payload is buffered, even for a maliciously oversized ftyp box.
			var brands [4096]byte
			n := min(length, int64(len(brands)))
			if _, err := reader.ReadAt(brands[:n], payload); err != nil {
				return ""
			}
			if mime := videoBrandType(string(brands[:4])); mime != "" {
				return mime
			}
			for i := int64(8); i+4 <= n; i += 4 {
				if mime := videoBrandType(string(brands[i : i+4])); mime != "" {
					return mime
				}
			}
			return ""
		case "moov":
			if legacyMOV && videoLegacyMovie(reader, payload, next) {
				return "video/quicktime"
			}
			return ""
		case "wide", "free", "skip", "mdat":
			// Known leading padding or media atoms may precede ftyp/moov.
		default:
			return ""
		}
		offset = next
	}
	return ""
}

func videoLegacyMovie(reader io.ReaderAt, offset, end int64) bool {
	for count := 0; offset < end && count < 64; count++ {
		kind, payload, next, ok := videoAtom(reader, offset, end)
		if !ok {
			return false
		}
		if kind == "mvhd" {
			var version [1]byte
			if next-payload < 100 {
				return false
			}
			if _, err := reader.ReadAt(version[:], payload); err != nil {
				return false
			}
			return version[0] == 0 || (version[0] == 1 && next-payload >= 112)
		}
		offset = next
	}
	return false
}
