package sessionruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const (
	MaxAttachmentBytes         int64 = 100 * 1024 * 1024
	MaxAttachmentsPerMessage         = 8
	maxSessionAttachmentBytes  int64 = 1024 * 1024 * 1024
	maxSessionAttachmentCount        = 128
	attachmentDirectoryName          = "attachments"
	attachmentDataFileName           = "data"
	attachmentMetadataFileName       = "metadata.json"
)

var (
	ErrAttachmentNotFound      = errors.New("Session attachment not found")
	ErrAttachmentTooLarge      = errors.New("Session attachment is too large")
	ErrAttachmentQuotaExceeded = errors.New("Session attachment storage quota exceeded")
)

// Attachment describes one file supplied with a Session message.
type Attachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	SizeBytes int64  `json:"sizeBytes"`
}

// ResolvedAttachment adds the runtime-local path used by provider adapters.
type ResolvedAttachment struct {
	Attachment
	Path string `json:"-"`
}

// TurnInput is the provider-neutral input for one Session turn.
type TurnInput struct {
	Text        string
	Attachments []ResolvedAttachment
}

// AttachmentStore persists Session attachments beneath the runtime state directory.
type AttachmentStore struct {
	directory string
}

// NewAttachmentStore constructs a store rooted in the Session state directory.
func NewAttachmentStore(stateDir string) (*AttachmentStore, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("Session state directory must not be empty")
	}
	return &AttachmentStore{directory: filepath.Join(stateDir, attachmentDirectoryName)}, nil
}

// Put validates and atomically stores one attachment.
func (s *AttachmentStore) Put(name string, source io.Reader) (Attachment, error) {
	name, err := normalizeAttachmentName(name)
	if err != nil {
		return Attachment{}, err
	}
	if err := os.MkdirAll(s.directory, 0700); err != nil {
		return Attachment{}, fmt.Errorf("creating Session attachment directory: %w", err)
	}
	lock, err := s.lock()
	if err != nil {
		return Attachment{}, err
	}
	defer func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}()
	storedBytes, err := s.checkQuota()
	if err != nil {
		return Attachment{}, err
	}

	id := uuid.NewString()
	temporary, err := os.MkdirTemp(s.directory, ".attachment-*")
	if err != nil {
		return Attachment{}, fmt.Errorf("creating temporary Session attachment: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.RemoveAll(temporary)
		}
	}()

	dataPath := filepath.Join(temporary, attachmentDataFileName)
	data, err := os.OpenFile(dataPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Attachment{}, fmt.Errorf("creating Session attachment data: %w", err)
	}
	limited := io.LimitReader(source, MaxAttachmentBytes+1)
	written, copyErr := io.Copy(data, limited)
	closeErr := data.Close()
	if copyErr != nil {
		return Attachment{}, fmt.Errorf("writing Session attachment data: %w", copyErr)
	}
	if closeErr != nil {
		return Attachment{}, fmt.Errorf("closing Session attachment data: %w", closeErr)
	}
	if written > MaxAttachmentBytes {
		return Attachment{}, fmt.Errorf("%w: maximum size is %d bytes", ErrAttachmentTooLarge, MaxAttachmentBytes)
	}
	if storedBytes+written > maxSessionAttachmentBytes {
		return Attachment{}, fmt.Errorf("%w: maximum is %d files or %d bytes", ErrAttachmentQuotaExceeded, maxSessionAttachmentCount, maxSessionAttachmentBytes)
	}

	mediaType, err := detectAttachmentMediaType(dataPath, name)
	if err != nil {
		return Attachment{}, err
	}
	attachment := Attachment{ID: id, Name: name, MediaType: mediaType, SizeBytes: written}
	metadata, err := json.Marshal(attachment)
	if err != nil {
		return Attachment{}, fmt.Errorf("encoding Session attachment metadata: %w", err)
	}
	metadata = append(metadata, '\n')
	if err := os.WriteFile(filepath.Join(temporary, attachmentMetadataFileName), metadata, 0600); err != nil {
		return Attachment{}, fmt.Errorf("writing Session attachment metadata: %w", err)
	}
	if err := os.Rename(temporary, filepath.Join(s.directory, id)); err != nil {
		return Attachment{}, fmt.Errorf("committing Session attachment: %w", err)
	}
	removeTemporary = false
	return attachment, nil
}

func (s *AttachmentStore) lock() (*os.File, error) {
	lock, err := os.OpenFile(filepath.Join(s.directory, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening Session attachment lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("locking Session attachment store: %w", err)
	}
	return lock, nil
}

// Open validates metadata and opens an attachment for reading.
func (s *AttachmentStore) Open(id string) (Attachment, *os.File, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Attachment{}, nil, ErrAttachmentNotFound
	}
	directory := filepath.Join(s.directory, id)
	metadata, err := os.ReadFile(filepath.Join(directory, attachmentMetadataFileName))
	if errors.Is(err, os.ErrNotExist) {
		return Attachment{}, nil, ErrAttachmentNotFound
	}
	if err != nil {
		return Attachment{}, nil, fmt.Errorf("reading Session attachment metadata: %w", err)
	}
	var attachment Attachment
	if err := json.Unmarshal(metadata, &attachment); err != nil {
		return Attachment{}, nil, fmt.Errorf("decoding Session attachment metadata: %w", err)
	}
	if attachment.ID != id || attachment.Name == "" || attachment.MediaType == "" || attachment.SizeBytes < 0 || attachment.SizeBytes > MaxAttachmentBytes {
		return Attachment{}, nil, errors.New("Session attachment metadata is invalid")
	}
	data, err := os.Open(filepath.Join(directory, attachmentDataFileName))
	if errors.Is(err, os.ErrNotExist) {
		return Attachment{}, nil, ErrAttachmentNotFound
	}
	if err != nil {
		return Attachment{}, nil, fmt.Errorf("opening Session attachment data: %w", err)
	}
	info, err := data.Stat()
	if err != nil {
		_ = data.Close()
		return Attachment{}, nil, fmt.Errorf("checking Session attachment data: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != attachment.SizeBytes {
		_ = data.Close()
		return Attachment{}, nil, errors.New("Session attachment data is invalid")
	}
	return attachment, data, nil
}

// Resolve loads attachment metadata and local paths for one provider turn.
func (s *AttachmentStore) Resolve(ids []string) ([]ResolvedAttachment, error) {
	if len(ids) > MaxAttachmentsPerMessage {
		return nil, fmt.Errorf("a Session message supports at most %d attachments", MaxAttachmentsPerMessage)
	}
	resolved := make([]ResolvedAttachment, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("Session attachment %q is repeated", id)
		}
		seen[id] = struct{}{}
		attachment, data, err := s.Open(id)
		if err != nil {
			return nil, fmt.Errorf("resolving Session attachment %q: %w", id, err)
		}
		_ = data.Close()
		resolved = append(resolved, ResolvedAttachment{
			Attachment: attachment,
			Path:       filepath.Join(s.directory, id, attachmentDataFileName),
		})
	}
	return resolved, nil
}

func (s *AttachmentStore) checkQuota() (int64, error) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return 0, fmt.Errorf("listing Session attachments: %w", err)
	}
	count := 0
	var size int64
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := os.Stat(filepath.Join(s.directory, entry.Name(), attachmentDataFileName))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("checking Session attachment quota: %w", err)
		}
		count++
		size += info.Size()
	}
	if count >= maxSessionAttachmentCount {
		return 0, fmt.Errorf("%w: maximum is %d files or %d bytes", ErrAttachmentQuotaExceeded, maxSessionAttachmentCount, maxSessionAttachmentBytes)
	}
	return size, nil
}

func normalizeAttachmentName(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) {
		return "", errors.New("Session attachment name is invalid")
	}
	if len(name) > 255 {
		return "", errors.New("Session attachment name must be at most 255 bytes")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", errors.New("Session attachment name must not contain control characters")
		}
	}
	return name, nil
}

func detectAttachmentMediaType(path, name string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening Session attachment for media detection: %w", err)
	}
	defer file.Close()
	header := make([]byte, 512)
	read, err := file.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading Session attachment for media detection: %w", err)
	}
	mediaType := http.DetectContentType(header[:read])
	if mediaType == "application/octet-stream" {
		if extensionType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); extensionType != "" {
			if parsed, _, err := mime.ParseMediaType(extensionType); err == nil && !strings.HasPrefix(parsed, "image/") {
				mediaType = parsed
			}
		}
	}
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = parsed
	}
	return mediaType, nil
}

func attachmentPrompt(input TurnInput) string {
	if len(input.Attachments) == 0 {
		return input.Text
	}
	var builder strings.Builder
	if strings.TrimSpace(input.Text) != "" {
		builder.WriteString(input.Text)
		builder.WriteString("\n\n")
	}
	builder.WriteString("The user attached these local files:\n")
	for _, attachment := range input.Attachments {
		fmt.Fprintf(&builder, "- %s (%s): %s\n", attachment.Name, attachment.MediaType, attachment.Path)
	}
	if strings.TrimSpace(input.Text) == "" {
		builder.WriteString("Inspect the attached files and respond to the user.")
	}
	return strings.TrimSpace(builder.String())
}

func nativeImageAttachment(mediaType string) bool {
	switch mediaType {
	case "image/gif", "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}
