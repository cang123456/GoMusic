package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func newTestApp(t *testing.T, files map[string][]byte) *App {
	t.Helper()

	root := t.TempDir()
	musicDir := filepath.Join(root, "music")
	if err := os.MkdirAll(musicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(musicDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	app, err := NewApp(musicDir, filepath.Join(root, "music.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Errorf("close app: %v", err)
		}
	})
	return app
}

func getTracks(t *testing.T, app *App) []Track {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/api/tracks", nil)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/tracks status = %d, body = %s", response.Code, response.Body.String())
	}

	var payload struct {
		Tracks []Track `json:"tracks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Tracks
}

func TestNewAppScansMusicAndSetsDefault(t *testing.T) {
	app := newTestApp(t, map[string][]byte{
		"第一首.mp3":   {0x49, 0x44, 0x33, 0x01},
		"notes.txt": []byte("not music"),
	})

	tracks := getTracks(t, app)
	if len(tracks) != 1 {
		t.Fatalf("track count = %d, want 1", len(tracks))
	}
	if tracks[0].Title != "第一首" {
		t.Errorf("title = %q, want 第一首", tracks[0].Title)
	}
	if !tracks[0].IsDefault {
		t.Error("first track should be the default")
	}
}

func TestAudioEndpointSupportsRangeRequests(t *testing.T) {
	content := []byte("ID3-test-audio-bytes")
	app := newTestApp(t, map[string][]byte{"sample.mp3": content})
	track := getTracks(t, app)[0]

	request := httptest.NewRequest(http.MethodGet, track.AudioURL, nil)
	request.Header.Set("Range", "bytes=0-3")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusPartialContent)
	}
	if got := response.Body.Bytes(); !bytes.Equal(got, content[:4]) {
		t.Fatalf("body = %q, want %q", got, content[:4])
	}
	if got := response.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", got)
	}
}

func TestSetDefaultTrack(t *testing.T) {
	app := newTestApp(t, map[string][]byte{
		"a.mp3": []byte("a"),
		"b.mp3": []byte("b"),
	})
	tracks := getTracks(t, app)
	target := tracks[1]

	body := bytes.NewBufferString(`{"track_id":` + strconv.FormatInt(target.ID, 10) + `}`)
	request := httptest.NewRequest(http.MethodPut, "/api/default", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT /api/default status = %d, body = %s", response.Code, response.Body.String())
	}

	tracks = getTracks(t, app)
	defaultCount := 0
	for _, track := range tracks {
		if track.IsDefault {
			defaultCount++
			if track.ID != target.ID {
				t.Errorf("default ID = %d, want %d", track.ID, target.ID)
			}
		}
	}
	if defaultCount != 1 {
		t.Errorf("default count = %d, want 1", defaultCount)
	}
}

func TestUploadTrackSanitizesFilename(t *testing.T) {
	app := newTestApp(t, nil)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", `..\..\new song.mp3`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "ID3-upload"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/tracks", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("POST /api/tracks status = %d, body = %s", response.Code, response.Body.String())
	}

	tracks := getTracks(t, app)
	if len(tracks) != 1 {
		t.Fatalf("track count = %d, want 1", len(tracks))
	}
	if tracks[0].Filename != "new song.mp3" {
		t.Errorf("filename = %q, want new song.mp3", tracks[0].Filename)
	}
	if _, err := os.Stat(filepath.Join(app.musicDir, "new song.mp3")); err != nil {
		t.Errorf("uploaded file: %v", err)
	}
}

func TestHomePageAndSecurityHeaders(t *testing.T) {
	app := newTestApp(t, nil)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", response.Code)
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("<title>MusicGo</title>")) {
		t.Error("home page does not contain the expected title")
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Error("Content-Security-Policy header is missing")
	}
}
