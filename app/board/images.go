package board

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/devark28/xhaos/app/apperr"
)

// maxImage bounds one uploaded image (as attachment.MaxSize does for files).
const maxImage = 20 << 20

// maxFileID bounds the scene's file IDs (Excalidraw's are short random strings).
const maxFileID = 200

// imageTypes are the formats a board stores, by what the bytes are: SVG is not
// among them, since the page would have to trust its markup.
var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// ImageUpload is one image of a board's scene. FileID is the ID the scene
// gives the image; the bytes are stored under their SHA-256, so an image
// that several boards use, or that a board adds twice, is kept once.
type ImageUpload struct {
	WorkspaceID string `json:"workspaceId"`
	BoardID     int64  `json:"boardId"`
	FileID      string `json:"fileId"`
	Created     int64  `json:"created"` // milliseconds, as the scene has it
	Data        []byte `json:"data"`    // base64 in JSON
}

// ImageFile is an image with its bytes, for the page to draw.
type ImageFile struct {
	FileID   string `json:"fileId"`
	MimeType string `json:"mimeType"`
	Data     []byte `json:"data"`
}

// AddImage stores an image for a board. The type is taken from the bytes, not
// from the request. A file ID the board already has keeps its first image.
func (s *Service) AddImage(req ImageUpload) error {
	if err := checkWorkspace(req.WorkspaceID); err != nil {
		return err
	}
	if req.FileID == "" || len(req.FileID) > maxFileID {
		return fmt.Errorf("%w: an image needs a file id of 1 to %d characters", apperr.Invalid, maxFileID)
	}
	if len(req.Data) == 0 || len(req.Data) > maxImage {
		return fmt.Errorf("%w: an image is 1 byte to %d MB", apperr.Invalid, maxImage>>20)
	}
	mime := http.DetectContentType(req.Data)
	if !imageTypes[mime] {
		return fmt.Errorf("%w: a board stores PNG, JPEG, GIF or WebP images, not %s", apperr.Invalid, mime)
	}
	sum := sha256.Sum256(req.Data)
	hash := hex.EncodeToString(sum[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := find(s.db, req.WorkspaceID, req.BoardID); err != nil {
		return err
	}
	if err := s.writeImage(req.WorkspaceID, hash, req.Data); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO board_images (board_id, file_id, hash, mime_type, created) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (board_id, file_id) DO NOTHING`, req.BoardID, req.FileID, hash, mime, req.Created)
	return err
}

// Image returns one of a board's images with its bytes.
func (s *Service) Image(workspaceID string, boardID int64, fileID string) (ImageFile, error) {
	if err := checkWorkspace(workspaceID); err != nil {
		return ImageFile{}, err
	}
	if _, err := find(s.db, workspaceID, boardID); err != nil {
		return ImageFile{}, err
	}
	var hash, mime string
	err := s.db.QueryRow(`SELECT bi.hash, bi.mime_type FROM board_images bi WHERE bi.board_id = ? AND bi.file_id = ?`, boardID, fileID).Scan(&hash, &mime)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ImageFile{}, fmt.Errorf("%w: image %s", apperr.NotFound, fileID)
		}
		return ImageFile{}, err
	}
	data, err := os.ReadFile(s.path(workspaceID, hash))
	if err != nil {
		return ImageFile{}, fmt.Errorf("image %s: %w", fileID, err)
	}
	return ImageFile{FileID: fileID, MimeType: mime, Data: data}, nil
}

// writeImage stores the bytes under their hash unless the file is there.
// Callers hold s.mu.
func (s *Service) writeImage(ws, hash string, data []byte) error {
	dest := s.path(ws, hash)
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// removeUnusedImages deletes the files of hashes no board of the workspace
// still uses. Callers hold s.mu.
func (s *Service) removeUnusedImages(ws string, hashes []string) {
	for _, h := range hashes {
		var n int
		err := s.db.QueryRow(`SELECT COUNT(*) FROM board_images bi JOIN boards b ON b.id = bi.board_id WHERE b.workspace_id = ? AND bi.hash = ?`, ws, h).Scan(&n)
		if err != nil || n > 0 {
			continue
		}
		os.Remove(s.path(ws, h)) // already gone is fine
	}
}

func (s *Service) path(ws, hash string) string { return filepath.Join(s.dir, ws, hash) }
