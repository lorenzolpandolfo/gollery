package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"embed"
)

const (
	mediaDirectory = "media"
	maxUploadSize  = 100 << 20
)

var (
	errFileTooLarge = errors.New("file exceeds maximum upload size")

	//go:embed web/*
	embeddedFiles embed.FS

	tmpl = template.Must(
		template.ParseFS(
			embeddedFiles,
			"web/index.html",
		),
	)
)

type File struct {
	Id          string
	Name        string
	ServerName  string
	Ext         string
	MIMEType    string
	Size        int64
	CreatedDate time.Time
}

type FileData struct {
	Files []File
}

func main() {
	if err := os.MkdirAll(mediaDirectory, 0755); err != nil {
		log.Fatal(err)
	}

	cleanupTemporaryFiles()

	staticFS, err := fs.Sub(embeddedFiles, "web/static")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()

	mux.Handle(
		"/static/",
		http.StripPrefix(
			"/static/",
			http.FileServer(http.FS(staticFS)),
		),
	)

	mux.HandleFunc("/media/", mediaHandler)
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/upload", uploadHandler)
	mux.HandleFunc("/delete", deleteHandler)
	mux.HandleFunc("/files", filesHandler)
	mux.HandleFunc("/files/", fileHandler)
	mux.HandleFunc("/", homeHandler)

	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	serverErrors := make(chan error, 1)

	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	log.Printf("server listening on %s", server.Addr)

	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}

	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()

		if err := server.Shutdown(shutdownContext); err != nil {
			log.Printf("server shutdown error: %v", err)
			server.Close()
		}

		if err := <-serverErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
		}
	}
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	renderAllFiles(w)
}

func filesHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/files" {
		http.NotFound(w, r)
		return
	}

	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	renderAllFiles(w)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/health" {
		http.NotFound(w, r)
		return
	}

	if !requireMethod(w, r, http.MethodGet, http.MethodHead) {
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte("ok"))
	}
}

func requireMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}

	w.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

	return false
}

func renderAllFiles(w http.ResponseWriter) {
	files, err := getAllFiles()
	if err != nil {
		log.Printf("list files error: %v", err)
		http.Error(w, "unable to list files", http.StatusInternalServerError)
		return
	}

	var output bytes.Buffer

	if err := tmpl.Execute(&output, FileData{
		Files: files,
	}); err != nil {
		log.Printf("template execution error: %v", err)
		http.Error(w, "unable to render page", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if _, err := w.Write(output.Bytes()); err != nil {
		log.Printf("response write error: %v", err)
	}
}

func renderAllFilesList(w http.ResponseWriter) {
	files, err := getAllFiles()
	if err != nil {
		log.Printf("list files error: %v", err)
		http.Error(w, "unable to list files", http.StatusInternalServerError)
		return
	}

	var output bytes.Buffer

	for _, file := range files {
		if err := tmpl.ExecuteTemplate(&output, "file-card", file); err != nil {
			log.Printf("file template execution error: %v", err)
			http.Error(w, "unable to render files", http.StatusInternalServerError)
			return
		}
	}

	if err := tmpl.ExecuteTemplate(&output, "file-count", len(files)); err != nil {
		log.Printf("file count template execution error: %v", err)
		http.Error(w, "unable to render file count", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if _, err := w.Write(output.Bytes()); err != nil {
		log.Printf("response write error: %v", err)
	}
}

func renderFileCount(w http.ResponseWriter) {
	files, err := getAllFiles()
	if err != nil {
		log.Printf("list files error: %v", err)
		http.Error(w, "unable to list files", http.StatusInternalServerError)
		return
	}

	var output bytes.Buffer

	if err := tmpl.ExecuteTemplate(&output, "file-count", len(files)); err != nil {
		log.Printf("file count template execution error: %v", err)
		http.Error(w, "unable to render file count", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if _, err := w.Write(output.Bytes()); err != nil {
		log.Printf("response write error: %v", err)
	}
}

func getAllFiles() ([]File, error) {
	entries, err := os.ReadDir(mediaDirectory)
	if err != nil {
		return nil, err
	}

	files := make([]File, 0, len(entries))

	for _, entry := range entries {
		file, ok, err := fileFromEntry(entry)
		if err != nil {
			log.Printf("file metadata error for %q: %v", entry.Name(), err)
			continue
		}

		if !ok {
			continue
		}

		files = append(files, file)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name > files[j].Name
	})

	return files, nil
}

func fileFromEntry(entry fs.DirEntry) (File, bool, error) {
	if entry.IsDir() || strings.HasPrefix(entry.Name(), ".upload-") {
		return File{}, false, nil
	}

	id, originalName, ok := parseStoredFilename(entry.Name())
	if !ok {
		return File{}, false, nil
	}

	info, err := entry.Info()
	if err != nil {
		return File{}, false, err
	}

	if !info.Mode().IsRegular() {
		return File{}, false, nil
	}

	ext := strings.ToLower(filepath.Ext(originalName))
	mimeType := mime.TypeByExtension(ext)

	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	return File{
		Id:          id,
		Name:        originalName,
		ServerName:  entry.Name(),
		Ext:         ext,
		MIMEType:    mimeType,
		Size:        info.Size(),
		CreatedDate: info.ModTime(),
	}, true, nil
}

func parseStoredFilename(serverName string) (string, string, bool) {
	id, name, found := strings.Cut(serverName, "__")

	if found && isUUID(id) && name != "" {
		return strings.ToLower(id), name, true
	}

	if len(serverName) > 36 && isUUID(serverName[:36]) {
		return strings.ToLower(serverName[:36]), serverName[36:], true
	}

	return "", "", false
}

func findFileByID(id string) (File, error) {
	if !isUUID(id) {
		return File{}, os.ErrNotExist
	}

	files, err := getAllFiles()
	if err != nil {
		return File{}, err
	}

	id = strings.ToLower(id)

	for _, file := range files {
		if file.Id == id {
			return file, nil
		}
	}

	return File{}, os.ErrNotExist
}

func findFileByServerName(serverName string) (File, error) {
	if serverName == "" || strings.ContainsAny(serverName, `/\`) {
		return File{}, os.ErrNotExist
	}

	files, err := getAllFiles()
	if err != nil {
		return File{}, err
	}

	for _, file := range files {
		if file.ServerName == serverName {
			return file, nil
		}
	}

	return File{}, os.ErrNotExist
}

func findFileByReference(reference string) (File, error) {
	if isUUID(reference) {
		return findFileByID(reference)
	}

	return findFileByServerName(reference)
}

func removeFileByID(id string) error {
	file, err := findFileByID(id)
	if err != nil {
		return err
	}

	return os.Remove(filepath.Join(mediaDirectory, file.ServerName))
}

func removeFileByReference(reference string) error {
	file, err := findFileByReference(reference)
	if err != nil {
		return err
	}

	return os.Remove(filepath.Join(mediaDirectory, file.ServerName))
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "invalid multipart request", http.StatusBadRequest)
		return
	}

	savedFiles := make([]File, 0)

	for {
		part, err := reader.NextPart()

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			rollbackUploadedFiles(savedFiles)
			writeMultipartError(w, err)
			return
		}

		if part.FileName() == "" {
			if err := part.Close(); err != nil {
				rollbackUploadedFiles(savedFiles)
				writeMultipartError(w, err)
				return
			}

			continue
		}

		if part.FormName() != "Files" && part.FormName() != "files" {
			part.Close()
			rollbackUploadedFiles(savedFiles)
			http.Error(w, "invalid file field", http.StatusBadRequest)
			return
		}

		file, saveErr := savePart(part)
		closeErr := part.Close()

		if saveErr != nil {
			rollbackUploadedFiles(savedFiles)

			if isUploadTooLarge(saveErr) {
				http.Error(w, "upload exceeds maximum size", http.StatusRequestEntityTooLarge)
				return
			}

			log.Printf("file upload error: %v", saveErr)
			http.Error(w, "unable to save file", http.StatusInternalServerError)
			return
		}

		if closeErr != nil {
			_ = os.Remove(filepath.Join(mediaDirectory, file.ServerName))
			rollbackUploadedFiles(savedFiles)
			writeMultipartError(w, closeErr)
			return
		}

		savedFiles = append(savedFiles, file)
	}

	if len(savedFiles) == 0 {
		http.Error(w, "no files received", http.StatusBadRequest)
		return
	}

	renderAllFilesList(w)
}

func savePart(part *multipart.Part) (File, error) {
	originalName := sanitizeFilename(part.FileName())

	tempFile, err := os.CreateTemp(mediaDirectory, ".upload-*")
	if err != nil {
		return File{}, err
	}

	tempPath := tempFile.Name()

	defer os.Remove(tempPath)

	size, copyErr := io.Copy(
		tempFile,
		io.LimitReader(part, maxUploadSize+1),
	)

	closeErr := tempFile.Close()

	if copyErr != nil {
		return File{}, copyErr
	}

	if size > maxUploadSize {
		return File{}, errFileTooLarge
	}

	if closeErr != nil {
		return File{}, closeErr
	}

	for attempt := 0; attempt < 3; attempt++ {
		id, err := generateUUID()
		if err != nil {
			return File{}, err
		}

		serverName := id + "__" + originalName
		destination := filepath.Join(mediaDirectory, serverName)

		if err := os.Link(tempPath, destination); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}

			return File{}, err
		}

		info, err := os.Stat(destination)
		if err != nil {
			os.Remove(destination)
			return File{}, err
		}

		ext := strings.ToLower(filepath.Ext(originalName))
		mimeType := mime.TypeByExtension(ext)

		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		return File{
			Id:          id,
			Name:        originalName,
			ServerName:  serverName,
			Ext:         ext,
			MIMEType:    mimeType,
			Size:        info.Size(),
			CreatedDate: info.ModTime(),
		}, nil
	}

	return File{}, errors.New("unable to generate unique file identifier")
}

func generateUUID() (string, error) {
	var id [16]byte

	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}

	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80

	encoded := hex.EncodeToString(id[:])

	return encoded[:8] + "-" +
		encoded[8:12] + "-" +
		encoded[12:16] + "-" +
		encoded[16:20] + "-" +
		encoded[20:], nil
}

func isUUID(id string) bool {
	if len(id) != 36 {
		return false
	}

	for i := 0; i < len(id); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if id[i] != '-' {
				return false
			}
			continue
		}

		char := id[i]

		isDigit := char >= '0' && char <= '9'
		isLowerHex := char >= 'a' && char <= 'f'
		isUpperHex := char >= 'A' && char <= 'F'

		if !isDigit && !isLowerHex && !isUpperHex {
			return false
		}
	}

	return true
}

func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)

	var builder strings.Builder

	for _, char := range name {
		if unicode.IsControl(char) || char == '/' || char == '\\' {
			continue
		}

		builder.WriteRune(char)
	}

	name = strings.TrimSpace(builder.String())

	if name == "" || name == "." || name == ".." {
		return "file"
	}

	runes := []rune(name)

	if len(runes) > 180 {
		name = string(runes[:180])
	}

	return name
}

func rollbackUploadedFiles(files []File) {
	for _, file := range files {
		path := filepath.Join(mediaDirectory, file.ServerName)

		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("upload rollback error for %q: %v", file.ServerName, err)
		}
	}
}

func isUploadTooLarge(err error) bool {
	var maxBytesError *http.MaxBytesError

	return errors.As(err, &maxBytesError) ||
		errors.Is(err, multipart.ErrMessageTooLarge) ||
		errors.Is(err, errFileTooLarge)
}

func writeMultipartError(w http.ResponseWriter, err error) {
	if isUploadTooLarge(err) {
		http.Error(w, "upload exceeds maximum size", http.StatusRequestEntityTooLarge)
		return
	}

	log.Printf("multipart error: %v", err)
	http.Error(w, "invalid upload", http.StatusBadRequest)
}

func fileHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/files/")

	if id == r.URL.Path || id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}

	if !requireMethod(w, r, http.MethodGet, http.MethodHead, http.MethodDelete) {
		return
	}

	if r.Method == http.MethodDelete {
		if err := removeFileByID(id); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}

			log.Printf("file deletion error: %v", err)
			http.Error(w, "unable to delete file", http.StatusInternalServerError)
			return
		}

		renderFileCount(w)
		return
	}

	file, err := findFileByID(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}

		log.Printf("file lookup error: %v", err)
		http.Error(w, "unable to find file", http.StatusInternalServerError)
		return
	}

	serveStoredFile(w, r, file, true)
}

func deleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodDelete) {
		return
	}

	reference := r.URL.Query().Get("file")

	if reference == "" {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}

	if err := removeFileByReference(reference); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}

		log.Printf("file deletion error: %v", err)
		http.Error(w, "unable to delete file", http.StatusInternalServerError)
		return
	}

	renderFileCount(w)
}

func mediaHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodHead) {
		return
	}

	serverName := strings.TrimPrefix(r.URL.Path, "/media/")

	if serverName == "" ||
		serverName == "." ||
		serverName == ".." ||
		strings.ContainsAny(serverName, `/\`) {
		http.NotFound(w, r)
		return
	}

	file, err := findFileByServerName(serverName)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	serveStoredFile(w, r, file, false)
}

func serveStoredFile(w http.ResponseWriter, r *http.Request, file File, attachment bool) {
	path := filepath.Join(mediaDirectory, file.ServerName)

	src, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	contentType, err := detectContentType(src, file.Name)
	if err != nil {
		log.Printf("content type detection error: %v", err)
		http.Error(w, "unable to read file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if attachment {
		disposition := mime.FormatMediaType(
			"attachment",
			map[string]string{
				"filename": file.Name,
			},
		)

		if disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
	}

	http.ServeContent(
		w,
		r,
		file.Name,
		info.ModTime(),
		src,
	)
}

func detectContentType(file *os.File, filename string) (string, error) {
	var header [512]byte

	n, err := file.Read(header[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	contentType := http.DetectContentType(header[:n])

	if contentType == "application/octet-stream" {
		ext := strings.ToLower(filepath.Ext(filename))

		if extensionType := mime.TypeByExtension(ext); extensionType != "" {
			return extensionType, nil
		}
	}

	return contentType, nil
}

func cleanupTemporaryFiles() {
	entries, err := os.ReadDir(mediaDirectory)
	if err != nil {
		log.Printf("temporary file cleanup error: %v", err)
		return
	}

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".upload-") {
			continue
		}

		path := filepath.Join(mediaDirectory, entry.Name())

		if err := os.Remove(path); err != nil {
			log.Printf("temporary file removal error: %v", err)
		}
	}
}
