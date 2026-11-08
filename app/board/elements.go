package board

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/sqlitedb"
)

// maxElements bounds one save. A scene past it is split across saves by the
// page, or is too big for one board.
const maxElements = 10_000

// Board is a board with its scene. Elements are Excalidraw's own JSON, kept
// as they came; images are listed by reference and fetched with Image.
type Board struct {
	Summary
	AppState json.RawMessage   `json:"appState"`
	Elements []json.RawMessage `json:"elements"`
	Images   []ImageRef        `json:"images"`
}

// ImageRef names an image the board's scene uses, by the file ID the scene
// gives it.
type ImageRef struct {
	FileID   string `json:"fileId"`
	MimeType string `json:"mimeType"`
	Created  int64  `json:"created"`
}

// header is the part of an element the merge reads.
type header struct {
	ID           string `json:"id"`
	Version      int64  `json:"version"`
	VersionNonce int64  `json:"versionNonce"`
}

// storedWins reports whether the stored element outranks the incoming one. It
// is Excalidraw's rule (reconcileElements): a higher version wins, and on a
// tie the lower versionNonce wins, so the page and the server agree on which
// edit stands. Deletions are tombstones (isDeleted), so they merge the same way.
func storedWins(stored, in header) bool {
	return stored.Version > in.Version || stored.Version == in.Version && stored.VersionNonce < in.VersionNonce
}

// Load returns a board with its scene.
func (s *Service) Load(workspaceID string, id int64) (Board, error) {
	if err := checkWorkspace(workspaceID); err != nil {
		return Board{}, err
	}
	m, err := find(s.db, workspaceID, id)
	if err != nil {
		return Board{}, err
	}
	var appState string
	if err := s.db.QueryRow(`SELECT app_state FROM boards WHERE id = ?`, id).Scan(&appState); err != nil {
		return Board{}, err
	}
	out := Board{Summary: m, AppState: json.RawMessage(appState), Elements: []json.RawMessage{}, Images: []ImageRef{}}
	rows, err := s.db.Query(`SELECT data FROM board_elements WHERE board_id = ? ORDER BY rowid`, id)
	if err != nil {
		return Board{}, err
	}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			return Board{}, err
		}
		out.Elements = append(out.Elements, json.RawMessage(data))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Board{}, err
	}
	imgs, err := s.db.Query(`SELECT file_id, mime_type, created FROM board_images WHERE board_id = ? ORDER BY created, file_id`, id)
	if err != nil {
		return Board{}, err
	}
	defer imgs.Close()
	for imgs.Next() {
		var r ImageRef
		if err := imgs.Scan(&r.FileID, &r.MimeType, &r.Created); err != nil {
			return Board{}, err
		}
		out.Images = append(out.Images, r)
	}
	return out, imgs.Err()
}

type SaveRequest struct {
	WorkspaceID string            `json:"workspaceId"`
	ID          int64             `json:"id"`
	Source      string            `json:"source"`   // the page saving, echoed in the notice
	Elements    []json.RawMessage `json:"elements"` // the page's elements, deleted ones included
	AppState    json.RawMessage   `json:"appState"`
}

// SaveResult tells the page what the save did not take: Newer holds the
// stored elements that outrank the ones sent. The page merges them into its
// scene.
type SaveResult struct {
	UpdatedAt time.Time         `json:"updatedAt"`
	Newer     []json.RawMessage `json:"newer"`
}

// Save merges the page's elements into the board, one by one: an element
// new to the board is added, an element the board holds at an older version
// is replaced, and one the board holds newer is left alone and returned in
// Newer. Elements the request does not mention stay as they are, so a save
// never removes another tab's work. The app state is replaced.
func (s *Service) Save(req SaveRequest) (SaveResult, error) {
	if err := checkWorkspace(req.WorkspaceID); err != nil {
		return SaveResult{}, err
	}
	if len(req.Elements) > maxElements {
		return SaveResult{}, fmt.Errorf("%w: a board has at most %d elements", apperr.Invalid, maxElements)
	}
	heads := make([]header, len(req.Elements))
	for i, raw := range req.Elements {
		if err := json.Unmarshal(raw, &heads[i]); err != nil || heads[i].ID == "" {
			return SaveResult{}, fmt.Errorf("%w: element %d has no id", apperr.Invalid, i)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return SaveResult{}, err
	}
	defer tx.Rollback()
	if _, err := find(tx, req.WorkspaceID, req.ID); err != nil {
		return SaveResult{}, err
	}
	at := sqlitedb.Stamp(s.now())
	accepted, newer, err := mergeElements(tx, req.ID, req.Elements, heads)
	if err != nil {
		return SaveResult{}, err
	}
	updated := sqlitedb.ParseStamp(at)
	if len(accepted) > 0 {
		if _, err := tx.Exec(`UPDATE boards SET updated_at = ? WHERE id = ?`, at, req.ID); err != nil {
			return SaveResult{}, err
		}
	} else {
		var c string
		if err := tx.QueryRow(`SELECT updated_at FROM boards WHERE id = ?`, req.ID).Scan(&c); err != nil {
			return SaveResult{}, err
		}
		updated = sqlitedb.ParseStamp(c)
	}
	if _, err := tx.Exec(`UPDATE boards SET app_state = ? WHERE id = ?`, appStateOrEmpty(req.AppState), req.ID); err != nil {
		return SaveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SaveResult{}, err
	}
	if len(accepted) > 0 {
		s.notify(req.WorkspaceID, Notice{Kind: NoticeSaved, WorkspaceID: req.WorkspaceID, ID: req.ID, Source: req.Source, UpdatedAt: updated, Elements: accepted})
	}
	return SaveResult{UpdatedAt: updated, Newer: newer}, nil
}

// mergeElements merges elements into a board, one by one, by Excalidraw's
// rule (storedWins): an element new to the board is added, one the board holds
// at an older edit is replaced, and one the board holds newer is left alone
// and returned in newer. accepted is what changed. A page's save and an
// agent's addition (tools.go) both go through it.
func mergeElements(tx *sql.Tx, boardID int64, raws []json.RawMessage, heads []header) (accepted, newer []json.RawMessage, err error) {
	accepted, newer = []json.RawMessage{}, []json.RawMessage{}
	for i, raw := range raws {
		h := heads[i]
		var sv, sn int64
		var stored string
		err := tx.QueryRow(`SELECT version, version_nonce, data FROM board_elements WHERE board_id = ? AND element_id = ?`, boardID, h.ID).Scan(&sv, &sn, &stored)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec(`INSERT INTO board_elements (board_id, element_id, version, version_nonce, data) VALUES (?, ?, ?, ?, ?)`,
				boardID, h.ID, h.Version, h.VersionNonce, string(raw)); err != nil {
				return nil, nil, err
			}
		case err != nil:
			return nil, nil, err
		case sv == h.Version && sn == h.VersionNonce:
			continue // the same edit the board has
		case storedWins(header{Version: sv, VersionNonce: sn}, h):
			newer = append(newer, json.RawMessage(stored))
			continue
		default:
			if _, err := tx.Exec(`UPDATE board_elements SET version = ?, version_nonce = ?, data = ? WHERE board_id = ? AND element_id = ?`,
				h.Version, h.VersionNonce, string(raw), boardID, h.ID); err != nil {
				return nil, nil, err
			}
		}
		accepted = append(accepted, raw)
	}
	return accepted, newer, nil
}

// appStateOrEmpty stores an absent app state as an empty object.
func appStateOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	return string(raw)
}
