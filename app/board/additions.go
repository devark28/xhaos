package board

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // image.DecodeConfig reads their sizes
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// Agents add to boards through the tools in tools.go. An addition is merged
// into the board exactly as a page's save is (mergeElements) and reaches the
// open pages as the same live Notice.
//
// An image an agent adds is staged when its call is prepared: the bytes are
// read once, checked, and stored by hash like any board image. The approval
// card shows a small preview of it (StagedImage), and the call adds those same
// bytes when it runs, even if the file changed meanwhile.

const stagedSchema = `
CREATE TABLE IF NOT EXISTS board_staged (
	workspace_id TEXT NOT NULL,
	agent_id     INTEGER NOT NULL,
	call_id      TEXT NOT NULL,
	hash         TEXT NOT NULL,
	mime_type    TEXT NOT NULL,
	name         TEXT NOT NULL,
	size         INTEGER NOT NULL,
	width        INTEGER NOT NULL,
	height       INTEGER NOT NULL,
	created_at   TEXT NOT NULL,
	PRIMARY KEY (workspace_id, agent_id, call_id)
)`

// stagedFor is how long a staged image waits for its call to run. A call
// denied or never decided leaves its image; it is removed after this.
const stagedFor = 7 * 24 * time.Hour

// staged is an image an agent's call will add.
type staged struct {
	Hash     string `json:"-"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// readImage reads an image file for an agent: path is relative to the
// project, or absolute; either way it must be inside the project (root) or
// the agent's own temp folder (tmp), after symlinks. The type comes from the
// bytes: PNG, JPEG, GIF or WebP, at most 20 MB.
func readImage(root, tmp, path string) ([]byte, staged, error) {
	if strings.TrimSpace(path) == "" {
		return nil, staged{}, errors.New("board_add_image needs path")
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, full)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(full))
	if err != nil {
		return nil, staged{}, fmt.Errorf("cannot find %s", path)
	}
	inside := false
	for _, dir := range []string{root, tmp} {
		if dir == "" {
			continue
		}
		if d, err := filepath.EvalSymlinks(dir); err == nil && (real == d || strings.HasPrefix(real, d+string(filepath.Separator))) {
			inside = true
		}
	}
	if !inside {
		return nil, staged{}, fmt.Errorf("%s is outside your project and your $TMPDIR (%s); copy the image into one of them", path, tmp)
	}
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, staged{}, fmt.Errorf("cannot open %s: %v", path, errors.Unwrap(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, staged{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, staged{}, fmt.Errorf("%s is not a file", path)
	}
	if info.Size() > maxImage {
		return nil, staged{}, fmt.Errorf("%s is %.1f MB; a board image is at most %d MB", path, float64(info.Size())/(1<<20), maxImage>>20)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxImage+1))
	if err != nil {
		return nil, staged{}, err
	}
	if len(data) > maxImage {
		return nil, staged{}, fmt.Errorf("%s is over %d MB; a board image is at most %d MB", path, maxImage>>20, maxImage>>20)
	}
	if len(data) == 0 {
		return nil, staged{}, fmt.Errorf("%s is empty", path)
	}
	mime := http.DetectContentType(data)
	if !imageTypes[mime] {
		return nil, staged{}, fmt.Errorf("%s is not a PNG, JPEG, GIF or WebP image (its bytes look like %s)", path, mime)
	}
	w, h, err := imageSize(mime, data)
	if err != nil || w <= 0 || h <= 0 {
		return nil, staged{}, fmt.Errorf("%s looks like %s but its size cannot be read; it may be damaged", path, mime)
	}
	sum := sha256.Sum256(data)
	return data, staged{Hash: hex.EncodeToString(sum[:]), Name: filepath.Base(real), MimeType: mime, Size: int64(len(data)), Width: w, Height: h}, nil
}

// imageSize reads an image's pixel size from its header.
func imageSize(mime string, data []byte) (int, int, error) {
	if mime == "image/webp" {
		return webpSize(data)
	}
	c, _, err := image.DecodeConfig(bytes.NewReader(data))
	return c.Width, c.Height, err
}

// webpSize reads a WebP's size from its first chunk (lossy VP8, lossless
// VP8L, or extended VP8X); the standard library has no WebP decoder.
func webpSize(b []byte) (int, int, error) {
	bad := errors.New("not a WebP header")
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, bad
	}
	le24 := func(p []byte) int { return int(p[0]) | int(p[1])<<8 | int(p[2])<<16 }
	switch string(b[12:16]) {
	case "VP8 ":
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, bad
		}
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), nil
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0, bad
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, nil
	case "VP8X":
		return le24(b[24:27]) + 1, le24(b[27:30]) + 1, nil
	}
	return 0, 0, bad
}

// stage keeps an agent call's image until the call runs. A call staged
// already keeps its first image: preparing it again (before it runs) does not
// read the file again.
func (s *Service) stage(ws string, agentID int64, callID, root, tmp, path string) (staged, error) {
	if err := checkWorkspace(ws); err != nil {
		return staged{}, err
	}
	if st, err := s.stagedImage(ws, agentID, callID); err == nil {
		return st, nil
	} else if !errors.Is(err, apperr.NotFound) {
		return staged{}, err
	}
	data, st, err := readImage(root, tmp, path)
	if err != nil {
		return staged{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeStaged(ws)
	if err := s.writeImage(ws, st.Hash, data); err != nil {
		return staged{}, err
	}
	_, err = s.db.Exec(`INSERT INTO board_staged (workspace_id, agent_id, call_id, hash, mime_type, name, size, width, height, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		ws, agentID, callID, st.Hash, st.MimeType, st.Name, st.Size, st.Width, st.Height, sqlitedb.Stamp(s.now()))
	if err != nil {
		return staged{}, err
	}
	return s.stagedImage(ws, agentID, callID)
}

func (s *Service) stagedImage(ws string, agentID int64, callID string) (staged, error) {
	var st staged
	err := s.db.QueryRow(`SELECT hash, mime_type, name, size, width, height FROM board_staged WHERE workspace_id = ? AND agent_id = ? AND call_id = ?`,
		ws, agentID, callID).Scan(&st.Hash, &st.MimeType, &st.Name, &st.Size, &st.Width, &st.Height)
	if errors.Is(err, sql.ErrNoRows) {
		return staged{}, fmt.Errorf("%w: no image waits for call %s", apperr.NotFound, callID)
	}
	return st, err
}

// purgeStaged drops the workspace's staged images older than stagedFor.
// Callers hold s.mu.
func (s *Service) purgeStaged(ws string) {
	cut := sqlitedb.Stamp(s.now().Add(-stagedFor))
	rows, err := s.db.Query(`SELECT DISTINCT hash FROM board_staged WHERE workspace_id = ? AND created_at < ?`, ws, cut)
	if err != nil {
		return
	}
	var hashes []string
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil {
			hashes = append(hashes, h)
		}
	}
	rows.Close()
	if _, err := s.db.Exec(`DELETE FROM board_staged WHERE workspace_id = ? AND created_at < ?`, ws, cut); err == nil {
		s.removeUnusedImages(ws, hashes)
	}
}

// StagedImage returns the approval card's preview of the image an agent's
// waiting call will add: small (see preview.go), or NoPreview.
func (s *Service) StagedImage(workspaceID string, agentID int64, callID string) (StagedPreview, error) {
	if err := checkWorkspace(workspaceID); err != nil {
		return StagedPreview{}, err
	}
	st, err := s.stagedImage(workspaceID, agentID, callID)
	if err != nil {
		return StagedPreview{}, err
	}
	data, err := os.ReadFile(s.path(workspaceID, st.Hash))
	if err != nil {
		return StagedPreview{}, fmt.Errorf("staged image: %w", err)
	}
	out := StagedPreview{CallID: callID, Name: st.Name, FileType: st.MimeType, Size: st.Size}
	mime, small, err := preview(st.MimeType, data)
	if err != nil {
		out.NoPreview = true
		return out, nil
	}
	out.MimeType, out.Data = mime, small
	return out, nil
}

// added is what an agent's addition did, for the tool's result.
type added struct {
	Board    Summary
	X, Y     float64
	Group    string
	Elements []string
}

// addForAgent places an addition on a board and merges it in, as a page's
// save is merged; img, when set, is the staged image the addition shows
// (stored for the board under fileID). The open pages get it live.
func (s *Service) addForAgent(ws string, agentID int64, boardID int64, a *addition, img *staged, fileID, callID string) (added, error) {
	if err := checkWorkspace(ws); err != nil {
		return added{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if img != nil {
		if _, err := os.Stat(s.path(ws, img.Hash)); err != nil {
			return added{}, fmt.Errorf("the image is no longer staged; add it again")
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return added{}, err
	}
	defer tx.Rollback()
	board, err := find(tx, ws, boardID)
	if err != nil {
		return added{}, err
	}
	scene, err := sceneOf(tx, boardID)
	if err != nil {
		return added{}, err
	}
	w, h := a.size()
	x, y := place(scene, w, h)
	a.moveTo(x, y)
	last := ""
	for _, e := range scene {
		if e.Index != nil && *e.Index > last {
			last = *e.Index
		}
	}
	a.order(last)
	raws := make([]json.RawMessage, len(a.elements))
	heads := make([]header, len(a.elements))
	ids := make([]string, len(a.elements))
	for i, e := range a.elements {
		if raws[i], err = json.Marshal(e); err != nil {
			return added{}, err
		}
		ids[i] = e["id"].(string)
		heads[i] = header{ID: ids[i], Version: 1, VersionNonce: e["versionNonce"].(int64)}
	}
	if img != nil {
		if _, err := tx.Exec(`INSERT INTO board_images (board_id, file_id, hash, mime_type, created) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (board_id, file_id) DO NOTHING`, boardID, fileID, img.Hash, img.MimeType, s.now().UnixMilli()); err != nil {
			return added{}, err
		}
		if _, err := tx.Exec(`DELETE FROM board_staged WHERE workspace_id = ? AND agent_id = ? AND call_id = ?`, ws, agentID, callID); err != nil {
			return added{}, err
		}
	}
	accepted, _, err := mergeElements(tx, boardID, raws, heads)
	if err != nil {
		return added{}, err
	}
	at := s.now()
	if _, err := tx.Exec(`UPDATE boards SET updated_at = ? WHERE id = ?`, sqlitedb.Stamp(at), boardID); err != nil {
		return added{}, err
	}
	if err := tx.Commit(); err != nil {
		return added{}, err
	}
	s.notify(ws, Notice{Kind: NoticeSaved, WorkspaceID: ws, ID: boardID, Source: fmt.Sprintf("agent-%d", agentID),
		UpdatedAt: sqlitedb.ParseStamp(sqlitedb.Stamp(at)), Elements: accepted})
	return added{Board: board, X: x, Y: y, Group: a.group, Elements: ids}, nil
}

// sceneOf reads a board's elements (deleted ones included: they hold scene
// order keys) in the board's order.
func sceneOf(q sqlitedb.Querier, boardID int64) ([]sceneElement, error) {
	rows, err := q.Query(`SELECT data FROM board_elements WHERE board_id = ? ORDER BY rowid`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sceneElement
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var e sceneElement
		if json.Unmarshal([]byte(data), &e) == nil {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}
