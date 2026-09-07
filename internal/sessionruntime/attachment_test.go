package sessionruntime

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachmentStorePutOpenAndResolve(t *testing.T) {
	store, err := NewAttachmentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte("\x89PNG\r\n\x1a\n")
	attachment, err := store.Put("../screen.png", bytes.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Name != "screen.png" || attachment.MediaType != "image/png" || attachment.SizeBytes != int64(len(contents)) {
		t.Fatalf("attachment = %#v", attachment)
	}

	opened, data, err := store.Open(attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	readContents, err := io.ReadAll(data)
	if err != nil {
		t.Fatal(err)
	}
	if opened != attachment || !bytes.Equal(readContents, contents) {
		t.Fatalf("opened attachment = %#v data = %q", opened, readContents)
	}

	resolved, err := store.Resolve([]string{attachment.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Attachment != attachment || filepath.Base(resolved[0].Path) != attachmentDataFileName {
		t.Fatalf("resolved attachments = %#v", resolved)
	}
}

func TestAttachmentStoreAcceptsMaximumSizeFiles(t *testing.T) {
	store, err := NewAttachmentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		attachment, err := store.Put(fmt.Sprintf("file-%d.bin", index), io.LimitReader(zeroReader{}, MaxAttachmentBytes))
		if err != nil {
			t.Fatal(err)
		}
		if attachment.SizeBytes != MaxAttachmentBytes {
			t.Fatalf("attachment size = %d, want %d", attachment.SizeBytes, MaxAttachmentBytes)
		}
		opened, data, err := store.Open(attachment.ID)
		if err != nil {
			t.Fatal(err)
		}
		readBytes, readErr := io.Copy(io.Discard, data)
		closeErr := data.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if opened != attachment || readBytes != MaxAttachmentBytes {
			t.Fatalf("opened attachment = %#v, read %d bytes, want %#v and %d bytes", opened, readBytes, attachment, MaxAttachmentBytes)
		}
	}
}

func TestAttachmentStoreRejectsOversizedInputAndCleansTemporaryData(t *testing.T) {
	stateDir := t.TempDir()
	store, err := NewAttachmentStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put("large.bin", io.LimitReader(zeroReader{}, MaxAttachmentBytes+1))
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("Put() error = %v, want ErrAttachmentTooLarge", err)
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, attachmentDirectoryName))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" {
			t.Fatalf("attachment directory contains %q after rejected upload", entry.Name())
		}
	}
}

func TestAttachmentStoreRejectsInvalidMetadataAndIdentifiers(t *testing.T) {
	store, err := NewAttachmentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open("../../secret"); !errors.Is(err, ErrAttachmentNotFound) {
		t.Fatalf("Open() error = %v, want ErrAttachmentNotFound", err)
	}
	if _, err := store.Put("bad\nname", strings.NewReader("data")); err == nil {
		t.Fatal("Put() accepted a control character in the attachment name")
	}
}

func TestAttachmentStoreEnforcesSessionFileQuota(t *testing.T) {
	store, err := NewAttachmentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxSessionAttachmentCount; index++ {
		if _, err := store.Put(fmt.Sprintf("file-%03d.txt", index), strings.NewReader("data")); err != nil {
			t.Fatalf("Put() attachment %d: %v", index, err)
		}
	}
	if _, err := store.Put("over-quota.txt", strings.NewReader("data")); !errors.Is(err, ErrAttachmentQuotaExceeded) {
		t.Fatalf("Put() error = %v, want ErrAttachmentQuotaExceeded", err)
	}
}

func TestAttachmentPromptIncludesFilesAndSupportsFileOnlyInput(t *testing.T) {
	input := TurnInput{Attachments: []ResolvedAttachment{{
		Attachment: Attachment{Name: "screen.png", MediaType: "image/png"},
		Path:       "/workspace/.kelos/session/attachments/id/data",
	}}}
	prompt := attachmentPrompt(input)
	for _, expected := range []string{"screen.png", "image/png", input.Attachments[0].Path, "Inspect the attached files"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("attachmentPrompt() = %q, want %q", prompt, expected)
		}
	}
}

func TestCodexTurnInputUsesNativeRasterImages(t *testing.T) {
	input := TurnInput{
		Text: "review these",
		Attachments: []ResolvedAttachment{
			{Attachment: Attachment{Name: "screen.png", MediaType: "image/png"}, Path: "/attachments/image/data"},
			{Attachment: Attachment{Name: "notes.txt", MediaType: "text/plain"}, Path: "/attachments/text/data"},
		},
	}
	items := codexTurnInputItems(input)
	if len(items) != 2 {
		t.Fatalf("Codex input items = %#v", items)
	}
	if items[1]["type"] != "localImage" || items[1]["path"] != input.Attachments[0].Path {
		t.Fatalf("Codex image input = %#v", items[1])
	}
	if !strings.Contains(items[0]["text"].(string), input.Attachments[1].Path) {
		t.Fatalf("Codex text input = %#v", items[0])
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}
