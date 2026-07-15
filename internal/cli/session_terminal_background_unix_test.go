//go:build darwin || linux

package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

func TestQuerySessionTUIDefaultColors(t *testing.T) {
	responseReader, responseWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer responseReader.Close()
	defer responseWriter.Close()
	queryReader, queryWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer queryReader.Close()
	defer queryWriter.Close()

	done := make(chan error, 1)
	go func() {
		query := make([]byte, len("\x1b]10;?\x1b\\\x1b]11;?\x1b\\"))
		if _, err := io.ReadFull(queryReader, query); err != nil {
			done <- err
			return
		}
		if !bytes.Equal(query, []byte("\x1b]10;?\x1b\\\x1b]11;?\x1b\\")) {
			done <- fmt.Errorf("terminal query = %q", query)
			return
		}
		_, err := io.WriteString(responseWriter, "\x1b]10;rgb:eeee/eeee/eeee\x1b\\\x1b]11;rgb:1111/1111/1111\a")
		done <- err
	}()

	colors := querySessionTUIDefaultColors(responseReader, queryWriter, time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if colors == nil {
		t.Fatal("terminal query returned no colors")
	}
	if want := (sessionTUIRGB{red: 238, green: 238, blue: 238}); colors.foreground != want {
		t.Errorf("foreground = %#v, want %#v", colors.foreground, want)
	}
	if want := (sessionTUIRGB{red: 17, green: 17, blue: 17}); colors.background != want {
		t.Errorf("background = %#v, want %#v", colors.background, want)
	}
}

func TestQuerySessionTUIDefaultColorsReturnsUnknownAfterTimeout(t *testing.T) {
	responseReader, responseWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer responseReader.Close()
	defer responseWriter.Close()

	if colors := querySessionTUIDefaultColors(responseReader, io.Discard, 10*time.Millisecond); colors != nil {
		t.Fatalf("terminal query returned %#v, want nil", colors)
	}
}
