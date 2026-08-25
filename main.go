package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	defaultAddress       = ":8080"
	defaultMusicDir      = "data/music"
	defaultDBPath        = "data/music.db"
	maxUploadSize        = 100 << 20
	playbackModeLoop     = "loop"
	playbackModeSequence = "sequence"
)

var supportedAudio = map[string]string{
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".ogg":  "audio/ogg",
}

//go:embed web/*
var webFiles embed.FS

type App struct {
	db       *sql.DB
	musicDir string
	handler  http.Handler
}

type Track struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Filename  string `json:"filename"`
	IsDefault bool   `json:"is_default"`
	AudioURL  string `json:"audio_url"`
}

type Settings struct {
	PlaybackMode string `json:"playback_mode"`
}

func main() {
	address := envOrDefault("MUSICGO_ADDR", defaultAddress)
	musicDir := envOrDefault("MUSICGO_MUSIC_DIR", defaultMusicDir)
	dbPath := envOrDefault("MUSICGO_DB_PATH", defaultDBPath)

	app, err := NewApp(musicDir, dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()

	log.Printf("MusicGo 已启动：http://localhost%s", address)
	log.Printf("音乐目录：%s", musicDir)
	if err := http.ListenAndServe(address, app); err != nil {
		log.Fatal(err)
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func NewApp(musicDir, dbPath string) (*App, error) {
	if err := os.MkdirAll(musicDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建音乐目录: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建数据库目录: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	db.SetMaxOpenConns(1)

	app := &App{db: db, musicDir: musicDir}
	if err := app.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	if err := app.syncMusicDirectory(); err != nil {
		db.Close()
		return nil, err
	}
	if err := app.configureRoutes(); err != nil {
		db.Close()
		return nil, err
	}
	return app, nil
}

func (a *App) initialize() error {
	schema := `
		PRAGMA busy_timeout = 5000;
		PRAGMA journal_mode = WAL;
		CREATE TABLE IF NOT EXISTS tracks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			filename TEXT NOT NULL UNIQUE,
			is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE UNIQUE INDEX IF NOT EXISTS tracks_one_default
			ON tracks(is_default) WHERE is_default = 1;
		CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		INSERT INTO settings (key, value)
			VALUES ('playback_mode', 'loop')
			ON CONFLICT(key) DO NOTHING;
	`
	if _, err := a.db.Exec(schema); err != nil {
		return fmt.Errorf("初始化数据库: %w", err)
	}
	return nil
}

func (a *App) syncMusicDirectory() error {
	entries, err := os.ReadDir(a.musicDir)
	if err != nil {
		return fmt.Errorf("读取音乐目录: %w", err)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("开始同步歌曲: %w", err)
	}
	defer tx.Rollback()

	present := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !isSupportedAudio(entry.Name()) {
			continue
		}
		present[entry.Name()] = true
		title := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if _, err := tx.Exec(
			`INSERT INTO tracks (title, filename) VALUES (?, ?)
			 ON CONFLICT(filename) DO NOTHING`,
			title,
			entry.Name(),
		); err != nil {
			return fmt.Errorf("登记歌曲 %q: %w", entry.Name(), err)
		}
	}

	rows, err := tx.Query(`SELECT id, filename FROM tracks`)
	if err != nil {
		return fmt.Errorf("读取歌曲记录: %w", err)
	}
	var missingIDs []int64
	for rows.Next() {
		var id int64
		var filename string
		if err := rows.Scan(&id, &filename); err != nil {
			rows.Close()
			return fmt.Errorf("读取歌曲记录: %w", err)
		}
		if !present[filename] {
			missingIDs = append(missingIDs, id)
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭歌曲记录: %w", err)
	}
	for _, id := range missingIDs {
		if _, err := tx.Exec(`DELETE FROM tracks WHERE id = ?`, id); err != nil {
			return fmt.Errorf("清理失效歌曲: %w", err)
		}
	}

	if err := ensureDefaultTrack(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("保存歌曲同步结果: %w", err)
	}
	return nil
}

func ensureDefaultTrack(tx *sql.Tx) error {
	var defaultCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM tracks WHERE is_default = 1`).Scan(&defaultCount); err != nil {
		return fmt.Errorf("检查默认歌曲: %w", err)
	}
	if defaultCount > 0 {
		return nil
	}
	if _, err := tx.Exec(`
		UPDATE tracks
		SET is_default = 1
		WHERE id = (SELECT id FROM tracks ORDER BY id LIMIT 1)
	`); err != nil {
		return fmt.Errorf("设置默认歌曲: %w", err)
	}
	return nil
}

func (a *App) configureRoutes() error {
	staticFS, err := fs.Sub(webFiles, "web")
	if err != nil {
		return fmt.Errorf("载入网页资源: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tracks", a.listTracks)
	mux.HandleFunc("POST /api/tracks", a.uploadTrack)
	mux.HandleFunc("GET /api/tracks/{id}/audio", a.serveAudio)
	mux.HandleFunc("PUT /api/default", a.setDefaultTrack)
	mux.HandleFunc("GET /api/settings", a.getSettings)
	mux.HandleFunc("PUT /api/settings", a.updateSettings)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /", http.FileServer(http.FS(staticFS)))
	a.handler = securityHeaders(mux)
	return nil
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.handler.ServeHTTP(w, r)
}

func (a *App) Close() error {
	return a.db.Close()
}

func (a *App) listTracks(w http.ResponseWriter, _ *http.Request) {
	rows, err := a.db.Query(`
		SELECT id, title, filename, is_default
		FROM tracks
		ORDER BY is_default DESC, id ASC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取歌曲列表")
		return
	}
	defer rows.Close()

	tracks := make([]Track, 0)
	for rows.Next() {
		var track Track
		if err := rows.Scan(&track.ID, &track.Title, &track.Filename, &track.IsDefault); err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取歌曲信息")
			return
		}
		track.AudioURL = fmt.Sprintf("/api/tracks/%d/audio", track.ID)
		tracks = append(tracks, track)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取歌曲列表")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tracks": tracks})
}

func (a *App) getSettings(w http.ResponseWriter, _ *http.Request) {
	var settings Settings
	if err := a.db.QueryRow(
		`SELECT value FROM settings WHERE key = 'playback_mode'`,
	).Scan(&settings.PlaybackMode); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取播放设置")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (a *App) updateSettings(w http.ResponseWriter, r *http.Request) {
	var settings Settings
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil || !isPlaybackMode(settings.PlaybackMode) {
		writeError(w, http.StatusBadRequest, "播放模式仅支持 loop 或 sequence")
		return
	}

	if _, err := a.db.Exec(
		`INSERT INTO settings (key, value) VALUES ('playback_mode', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		settings.PlaybackMode,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存播放设置")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (a *App) uploadTrack(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "上传失败，歌曲不能超过 100 MB")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请选择歌曲文件")
		return
	}
	defer file.Close()

	filename := cleanFilename(header.Filename)
	if filename == "" || !isSupportedAudio(filename) {
		writeError(w, http.StatusBadRequest, "仅支持 MP3、WAV、FLAC、M4A 和 OGG")
		return
	}

	filename, destination, output, err := a.createUniqueFile(filename)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存歌曲")
		return
	}
	if _, err := io.Copy(output, file); err != nil {
		output.Close()
		os.Remove(destination)
		writeError(w, http.StatusInternalServerError, "保存歌曲失败")
		return
	}
	if err := output.Close(); err != nil {
		os.Remove(destination)
		writeError(w, http.StatusInternalServerError, "保存歌曲失败")
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = strings.TrimSuffix(filename, filepath.Ext(filename))
	}

	result, err := a.db.Exec(`INSERT INTO tracks (title, filename) VALUES (?, ?)`, title, filename)
	if err != nil {
		os.Remove(destination)
		writeError(w, http.StatusInternalServerError, "无法登记歌曲")
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取新歌曲")
		return
	}

	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE is_default = 1`).Scan(&count); err == nil && count == 0 {
		_, _ = a.db.Exec(`UPDATE tracks SET is_default = 1 WHERE id = ?`, id)
	}

	var isDefault bool
	if err := a.db.QueryRow(`SELECT is_default FROM tracks WHERE id = ?`, id).Scan(&isDefault); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取新歌曲")
		return
	}
	writeJSON(w, http.StatusCreated, Track{
		ID:        id,
		Title:     title,
		Filename:  filename,
		IsDefault: isDefault,
		AudioURL:  fmt.Sprintf("/api/tracks/%d/audio", id),
	})
}

func (a *App) createUniqueFile(original string) (string, string, *os.File, error) {
	extension := filepath.Ext(original)
	base := strings.TrimSuffix(original, extension)
	for suffix := 1; suffix <= 9999; suffix++ {
		filename := original
		if suffix > 1 {
			filename = fmt.Sprintf("%s (%d)%s", base, suffix, extension)
		}
		destination := filepath.Join(a.musicDir, filename)
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return filename, destination, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", "", nil, err
		}
	}
	return "", "", nil, errors.New("同名歌曲过多")
}

func (a *App) setDefaultTrack(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TrackID int64 `json:"track_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.TrackID <= 0 {
		writeError(w, http.StatusBadRequest, "歌曲编号无效")
		return
	}

	tx, err := a.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法设置默认歌曲")
		return
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tracks WHERE id = ?)`, request.TrackID).Scan(&exists); err != nil {
		writeError(w, http.StatusInternalServerError, "无法设置默认歌曲")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "没有找到这首歌曲")
		return
	}
	if _, err := tx.Exec(`UPDATE tracks SET is_default = 0 WHERE is_default = 1`); err != nil {
		writeError(w, http.StatusInternalServerError, "无法设置默认歌曲")
		return
	}
	if _, err := tx.Exec(`UPDATE tracks SET is_default = 1 WHERE id = ?`, request.TrackID); err != nil {
		writeError(w, http.StatusInternalServerError, "无法设置默认歌曲")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法设置默认歌曲")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"track_id": request.TrackID})
}

func (a *App) serveAudio(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "歌曲编号无效")
		return
	}

	var title, filename string
	if err := a.db.QueryRow(`SELECT title, filename FROM tracks WHERE id = ?`, id).Scan(&title, &filename); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "没有找到这首歌曲")
			return
		}
		writeError(w, http.StatusInternalServerError, "无法读取歌曲")
		return
	}
	if cleanFilename(filename) != filename {
		writeError(w, http.StatusInternalServerError, "歌曲路径无效")
		return
	}

	file, err := os.Open(filepath.Join(a.musicDir, filename))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "歌曲文件不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "无法打开歌曲")
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取歌曲")
		return
	}
	contentType := supportedAudio[strings.ToLower(filepath.Ext(filename))]
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(filename))
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename*=UTF-8''%s`, urlEncodeFilename(filename)))
	http.ServeContent(w, r, title, info.ModTime(), file)
}

func isSupportedAudio(filename string) bool {
	_, ok := supportedAudio[strings.ToLower(filepath.Ext(filename))]
	return ok
}

func isPlaybackMode(mode string) bool {
	return mode == playbackModeLoop || mode == playbackModeSequence
}

func cleanFilename(filename string) string {
	filename = strings.ReplaceAll(filename, `\`, "/")
	filename = filepath.Base(filename)
	filename = strings.TrimSpace(strings.ReplaceAll(filename, "\x00", ""))
	if filename == "." || filename == string(filepath.Separator) {
		return ""
	}
	return filename
}

func urlEncodeFilename(filename string) string {
	return url.PathEscape(filename)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set(
			"Content-Security-Policy",
			"default-src 'self'; img-src 'self' https://trae-api-cn.mchost.guru https://lf-cdn.trae.com.cn; "+
				"script-src 'self' https://unpkg.com; style-src 'self'; media-src 'self'; connect-src 'self'",
		)
		next.ServeHTTP(w, r)
	})
}
