package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

const (
	mediaDirectory       = "media"
	trashDirectory       = ".trash"
	dataFilePath         = "data.json"
	maxSearchHits        = 2000
	statsTTL             = 30 * time.Second
	recentWindow         = 7 * 24 * time.Hour
	trashRetention       = 30 * 24 * time.Hour
	trashCleanupInterval = time.Hour
)

var (
	errInsufficientStorage = errors.New("not enough free storage space")

	videoExtensions = map[string]bool{
		".mp4": true, ".webm": true, ".ogg": true, ".mov": true, ".mkv": true,
	}

	imageExtensions = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
		".webp": true, ".bmp": true, ".svg": true,
	}

	// Usado para pesquisa sem diferenciar maiúsculas/minúsculas e acentos.
	foldReplacer = strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ç", "c", "ñ", "n",
	)

	//go:embed web/*
	embeddedFiles embed.FS

	tmpl = template.Must(
		template.New("index.html").
			Funcs(template.FuncMap{"formatBytes": formatBytes}).
			ParseFS(embeddedFiles, "web/index.html"),
	)

	// Cache das estatísticas da biblioteca (percorrer a árvore é O(n)).
	libraryStats struct {
		sync.Mutex
		used     int64
		files    int
		computed time.Time
		valid    bool
	}
)

type File struct {
	Id           string
	Name         string
	ServerName   string // nome em disco: <uuid>__<nome>
	Path         string // caminho relativo a media/, com "/" (ex.: "viagens/2024/uuid__foto.jpg")
	Dir          string // pasta que contém o arquivo ("" = raiz)
	Located      bool   // mostrar a localização (resultados de pesquisa)
	Ext          string
	Kind         string // "image", "video" ou "other"
	MIMEType     string
	Size         int64
	CreatedDate  time.Time
	Starred      bool
	Trashed      bool
	TrashID      string
	OriginalPath string
	DeletedAt    time.Time
}

// URL é o endereço escapado do arquivo em /media/.
func (f File) URL() string {
	return (&url.URL{Path: "/media/" + f.Path}).EscapedPath()
}

func (f File) TrashPreviewURL() string {
	return "/trash/preview?id=" + url.QueryEscape(f.TrashID)
}

func (f File) SizeLabel() string { return formatBytes(f.Size) }

func (f File) Title() string {
	return strings.TrimSuffix(f.Name, filepath.Ext(f.Name))
}

func (f File) DateLabel() string { return f.CreatedDate.Local().Format("02/01/2006 15:04") }

func (f File) TrashDateLabel() string { return f.DeletedAt.Local().Format("02/01/2006 15:04") }

func (f File) Location() string { return locationLabel(f.Dir) }

type Folder struct {
	Name         string
	Path         string
	Dir          string
	Located      bool
	CreatedDate  time.Time
	Starred      bool
	Trashed      bool
	TrashID      string
	OriginalPath string
	DeletedAt    time.Time
	ItemCount    int
	Size         int64
}

func (f Folder) Location() string { return locationLabel(f.Dir) }

func (f Folder) TrashDateLabel() string { return f.DeletedAt.Local().Format("02/01/2006 15:04") }

func (f Folder) SizeLabel() string { return formatBytes(f.Size) }

func (f Folder) ItemsLabel() string {
	if f.ItemCount == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d itens", f.ItemCount)
}

func locationLabel(dir string) string {
	if dir == "" {
		return "Arquivos"
	}

	return strings.ReplaceAll(dir, "/", " / ")
}

type Crumb struct {
	Name    string
	Path    string
	Current bool
}

// StorageStats resume o uso de armazenamento da biblioteca e do disco.
type StorageStats struct {
	FileCount  int
	UsedBytes  int64 // soma do tamanho de todos os arquivos
	HasDisk    bool
	DiskTotal  int64
	DiskFree   int64
	GalleryPct float64 // % do disco ocupado pela galeria
	OtherPct   float64 // % do disco ocupado por outros dados
}

type DeviceInfo struct {
	Hostname        string
	OperatingSystem string
	Architecture    string
}

type ViewCount struct {
	Folders int
	Files   int
	Search  bool
}

type View struct {
	Folders []Folder
	Files   []File
	Count   ViewCount
}

type FileData struct {
	Folders      []Folder
	Files        []File
	LatestPhotos []File
	Count        ViewCount
	Stats        StorageStats
	Device       DeviceInfo
	View         string
	Path         string
	Crumbs       []Crumb
	Query        string
	Sort         string
	Order        string
}

// SortOptions descreve a ordenação pedida pelo cliente.
type SortOptions struct {
	By    string // "date", "name" ou "type"
	Order string // "asc" ou "desc"
}

// viewRequest reúne o que define o conteúdo exibido: pasta, pesquisa e ordem.
type viewRequest struct {
	Path  string
	Query string
	View  string
	Opts  SortOptions
}

type emptyStateData struct {
	Query string
	View  string
}

type starredEntry struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path,omitempty"`
}

type persistentData struct {
	Starred []starredEntry `json:"starred"`
	Trash   []trashEntry   `json:"trash,omitempty"`
}

type trashEntry struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	OriginalPath string    `json:"originalPath"`
	TrashPath    string    `json:"trashPath"`
	DeletedAt    time.Time `json:"deletedAt"`
}

type starRequest struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Starred bool   `json:"starred"`
}

type renameRequest struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Name string `json:"name"`
}

var persistentState = struct {
	sync.RWMutex
	starred map[string]starredEntry
	trash   []trashEntry
}{starred: make(map[string]starredEntry)}

func main() {
	var port string
	flag.StringVar(&port, "p", "8092", "port to listen on")
	flag.StringVar(&port, "port", "8092", "port to listen on")
	flag.Parse()

	addr := port
	if !strings.HasPrefix(addr, ":") {
		addr = ":" + addr
	}

	if err := os.MkdirAll(mediaDirectory, 0755); err != nil {
		log.Fatal(err)
	}

	if err := loadPersistentData(); err != nil {
		log.Fatalf("unable to load %s: %v", dataFilePath, err)
	}

	cleanupExpiredTrash()
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
	mux.HandleFunc("/star", starHandler)
	mux.HandleFunc("/rename", renameHandler)
	mux.HandleFunc("/trash/restore", restoreTrashHandler)
	mux.HandleFunc("/trash/preview", trashPreviewHandler)
	mux.HandleFunc("/trash", trashHandler)
	mux.HandleFunc("/delete", deleteHandler)
	mux.HandleFunc("/bulk-delete", bulkDeleteHandler)
	mux.HandleFunc("/bulk-download", bulkDownloadHandler)
	mux.HandleFunc("/bulk-move", bulkMoveHandler)
	mux.HandleFunc("/folders", foldersHandler)
	mux.HandleFunc("/files", filesHandler)
	mux.HandleFunc("/files/", fileHandler)
	mux.HandleFunc("/", homeHandler)

	server := &http.Server{
		Addr:              addr,
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
	go runTrashCleanup(ctx)

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

/* ------------------------------------------------------------------ */
/* Caminhos                                                           */
/* ------------------------------------------------------------------ */

// validSegment aceita um único nome de arquivo/pasta. Nomes que começam com "."
// são reservados (arquivos temporários de upload, "." e "..").
func validSegment(segment string) bool {
	if segment == "" || len(segment) > 255 || strings.HasPrefix(segment, ".") {
		return false
	}

	for _, char := range segment {
		if char == '/' || char == '\\' || unicode.IsControl(char) {
			return false
		}
	}

	return true
}

// cleanRelPath valida e normaliza um caminho relativo a media/ vindo do cliente.
func cleanRelPath(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}

	segments := make([]string, 0, 4)

	for _, segment := range strings.Split(raw, "/") {
		if segment == "" {
			continue
		}

		if !validSegment(segment) {
			return "", false
		}

		segments = append(segments, segment)
	}

	return strings.Join(segments, "/"), true
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}

	return dir + "/" + name
}

func parentRel(rel string) string {
	dir := pathpkg.Dir(rel)

	if dir == "." || dir == "/" {
		return ""
	}

	return dir
}

func fullPath(rel string) string {
	return filepath.Join(mediaDirectory, filepath.FromSlash(rel))
}

func buildCrumbs(path string) []Crumb {
	if path == "" {
		return nil
	}

	names := strings.Split(path, "/")
	crumbs := make([]Crumb, 0, len(names))

	for i, name := range names {
		crumbs = append(crumbs, Crumb{
			Name:    name,
			Path:    strings.Join(names[:i+1], "/"),
			Current: i == len(names)-1,
		})
	}

	return crumbs
}

func dirExists(rel string) bool {
	info, err := os.Stat(fullPath(rel))

	return err == nil && info.IsDir()
}

/* ------------------------------------------------------------------ */
/* Handlers de página                                                 */
/* ------------------------------------------------------------------ */

func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	renderPage(w, r)
}

func filesHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/files" {
		http.NotFound(w, r)
		return
	}

	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	// Requisições HTMX recebem apenas o fragmento da lista; o resto, a página.
	w.Header().Add("Vary", "HX-Request")

	if r.Header.Get("HX-Request") == "true" {
		renderList(w, r)
		return
	}

	renderPage(w, r)
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

func starredEntryKey(item starredEntry) string {
	switch item.Kind {
	case "file":
		return "file:" + item.ID
	case "folder":
		return "folder:" + item.Path
	default:
		return ""
	}
}

func readPersistentData(filename string) (persistentData, error) {
	content, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return persistentData{}, nil
	}
	if err != nil {
		return persistentData{}, err
	}

	var data persistentData
	if err := json.Unmarshal(content, &data); err != nil {
		return persistentData{}, fmt.Errorf("decode JSON: %w", err)
	}

	items := make(map[string]starredEntry, len(data.Starred))
	for _, item := range data.Starred {
		switch item.Kind {
		case "file":
			if !isUUID(item.ID) || item.Path != "" {
				return persistentData{}, errors.New("invalid starred file entry")
			}
			item.ID = strings.ToLower(item.ID)
		case "folder":
			path, ok := cleanRelPath(item.Path)
			if !ok || path == "" || path != item.Path || item.ID != "" {
				return persistentData{}, errors.New("invalid starred folder entry")
			}
		default:
			return persistentData{}, errors.New("invalid starred item kind")
		}

		items[starredEntryKey(item)] = item
	}

	data.Starred = make([]starredEntry, 0, len(items))
	for _, item := range items {
		data.Starred = append(data.Starred, item)
	}
	sort.Slice(data.Starred, func(i, j int) bool {
		return starredEntryKey(data.Starred[i]) < starredEntryKey(data.Starred[j])
	})

	trashIDs := make(map[string]bool, len(data.Trash))
	for _, item := range data.Trash {
		originalPath, originalOK := cleanRelPath(item.OriginalPath)
		trashItemPath := strings.TrimPrefix(item.TrashPath, trashDirectory+"/"+item.ID+"/")
		trashItemPath, trashPathOK := cleanRelPath(trashItemPath)
		if !isUUID(item.ID) ||
			(item.Kind != "file" && item.Kind != "folder") ||
			!originalOK || originalPath == "" || originalPath != item.OriginalPath ||
			!trashPathOK || trashItemPath == "" ||
			!strings.HasPrefix(item.TrashPath, trashDirectory+"/"+item.ID+"/") ||
			item.DeletedAt.IsZero() || trashIDs[item.ID] {
			return persistentData{}, errors.New("invalid trash entry")
		}
		trashIDs[item.ID] = true
	}

	return data, nil
}

func loadPersistentData() error {
	items, err := readPersistentData(dataFilePath)
	if err != nil {
		return err
	}

	starred := make(map[string]starredEntry, len(items.Starred))
	for _, item := range items.Starred {
		starred[starredEntryKey(item)] = item
	}

	persistentState.Lock()
	persistentState.starred = starred
	persistentState.trash = items.Trash
	persistentState.Unlock()

	return nil
}

func savePersistentData(filename string, data persistentData) error {
	sort.Slice(data.Starred, func(i, j int) bool {
		return starredEntryKey(data.Starred[i]) < starredEntryKey(data.Starred[j])
	})
	sort.Slice(data.Trash, func(i, j int) bool {
		return data.Trash[i].ID < data.Trash[j].ID
	})

	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')

	temp, err := os.CreateTemp(filepath.Dir(filename), ".data-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}

	return os.Rename(tempPath, filename)
}

func persistentDataLocked() persistentData {
	data := persistentData{
		Starred: make([]starredEntry, 0, len(persistentState.starred)),
		Trash:   append([]trashEntry(nil), persistentState.trash...),
	}
	for _, item := range persistentState.starred {
		data.Starred = append(data.Starred, item)
	}
	return data
}

func savePersistentDataLocked() error {
	return savePersistentData(dataFilePath, persistentDataLocked())
}

func removeStarredEntries(matches func(starredEntry) bool) {
	persistentState.Lock()
	defer persistentState.Unlock()

	previous := make(map[string]starredEntry, len(persistentState.starred))
	for key, item := range persistentState.starred {
		previous[key] = item
	}

	changed := false
	for key, item := range persistentState.starred {
		if matches(item) {
			delete(persistentState.starred, key)
			changed = true
		}
	}
	if !changed {
		return
	}

	if err := savePersistentDataLocked(); err != nil {
		persistentState.starred = previous
		log.Printf("star data save error: %v", err)
	}
}

func rebaseFolderMetadata(oldPath, newPath string) error {
	persistentState.Lock()
	defer persistentState.Unlock()

	updated := make(map[string]starredEntry, len(persistentState.starred))
	for key, item := range persistentState.starred {
		updated[key] = item
	}

	changed := false
	for key, item := range updated {
		if item.Kind != "folder" || (item.Path != oldPath && !strings.HasPrefix(item.Path, oldPath+"/")) {
			continue
		}
		delete(updated, key)
		item.Path = newPath + strings.TrimPrefix(item.Path, oldPath)
		updated[starredEntryKey(item)] = item
		changed = true
	}

	updatedTrash := append([]trashEntry(nil), persistentState.trash...)
	for i := range updatedTrash {
		path := updatedTrash[i].OriginalPath
		if path == oldPath || strings.HasPrefix(path, oldPath+"/") {
			updatedTrash[i].OriginalPath = newPath + strings.TrimPrefix(path, oldPath)
			changed = true
		}
	}
	if !changed {
		return nil
	}

	previous := persistentState.starred
	previousTrash := persistentState.trash
	persistentState.starred = updated
	persistentState.trash = updatedTrash
	if err := savePersistentDataLocked(); err != nil {
		persistentState.starred = previous
		persistentState.trash = previousTrash
		return err
	}
	return nil
}

func starHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)

	var req starRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	var item starredEntry
	switch req.Kind {
	case "file":
		file, err := findFileByReference(req.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			log.Printf("star file lookup error: %v", err)
			http.Error(w, "unable to find file", http.StatusInternalServerError)
			return
		}
		item = starredEntry{Kind: "file", ID: file.Id}
	case "folder":
		path, ok := cleanRelPath(req.Path)
		if !ok || path == "" {
			http.Error(w, "invalid folder path", http.StatusBadRequest)
			return
		}
		info, err := os.Lstat(fullPath(path))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			log.Printf("star folder lookup error: %v", err)
			http.Error(w, "unable to find folder", http.StatusInternalServerError)
			return
		}
		if !info.IsDir() {
			http.NotFound(w, r)
			return
		}
		item = starredEntry{Kind: "folder", Path: path}
	default:
		http.Error(w, "invalid item kind", http.StatusBadRequest)
		return
	}

	key := starredEntryKey(item)
	persistentState.Lock()
	previous, existed := persistentState.starred[key]
	if req.Starred {
		persistentState.starred[key] = item
	} else {
		delete(persistentState.starred, key)
	}
	if err := savePersistentDataLocked(); err != nil {
		if existed {
			persistentState.starred[key] = previous
		} else {
			delete(persistentState.starred, key)
		}
		persistentState.Unlock()
		log.Printf("star data save error: %v", err)
		http.Error(w, "unable to save starred items", http.StatusInternalServerError)
		return
	}
	persistentState.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(map[string]bool{"starred": req.Starred}); err != nil {
		log.Printf("star response error: %v", err)
	}
}

func renameHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	var req renameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Name, `/\`) {
		http.Error(w, "item name cannot contain path separators", http.StatusBadRequest)
		return
	}

	var oldPath, newPath string
	switch req.Kind {
	case "file":
		file, err := findFileByReference(req.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			log.Printf("rename file lookup error: %v", err)
			http.Error(w, "unable to find file", http.StatusInternalServerError)
			return
		}
		name := sanitizeFilename(req.Name)
		if ext := filepath.Ext(name); ext == "" || ext == "." {
			name = strings.TrimSuffix(name, ".") + filepath.Ext(file.Name)
		}
		if !validSegment(name) || !validSegment(file.Id+"__"+name) {
			http.Error(w, "invalid file name", http.StatusBadRequest)
			return
		}
		oldPath = file.Path
		newPath = pathpkg.Join(parentRel(oldPath), file.Id+"__"+name)
	case "folder":
		var ok bool
		oldPath, ok = cleanRelPath(req.Path)
		if !ok || oldPath == "" {
			http.Error(w, "invalid folder path", http.StatusBadRequest)
			return
		}
		name, ok := sanitizeFolderName(req.Name)
		if !ok {
			http.Error(w, "invalid folder name", http.StatusBadRequest)
			return
		}
		newPath = pathpkg.Join(parentRel(oldPath), name)
		info, err := os.Lstat(fullPath(oldPath))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			log.Printf("rename folder lookup error: %v", err)
			http.Error(w, "unable to find folder", http.StatusInternalServerError)
			return
		}
		if !info.IsDir() {
			http.NotFound(w, r)
			return
		}
	default:
		http.Error(w, "invalid item kind", http.StatusBadRequest)
		return
	}

	if oldPath == newPath {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, err := os.Lstat(fullPath(newPath)); err == nil {
		http.Error(w, "an item with that name already exists", http.StatusConflict)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Printf("rename destination lookup error: %v", err)
		http.Error(w, "unable to rename item", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(fullPath(oldPath), fullPath(newPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		log.Printf("rename error: %v", err)
		http.Error(w, "unable to rename item", http.StatusInternalServerError)
		return
	}
	if req.Kind == "folder" {
		if err := rebaseFolderMetadata(oldPath, newPath); err != nil {
			if rollbackErr := os.Rename(fullPath(newPath), fullPath(oldPath)); rollbackErr != nil {
				log.Printf("folder rename rollback error: %v", rollbackErr)
			}
			log.Printf("folder metadata update error: %v", err)
			http.Error(w, "unable to update folder metadata", http.StatusInternalServerError)
			return
		}
	}

	invalidateStats()
	w.WriteHeader(http.StatusNoContent)
}

func moveToTrash(kind, reference string) error {
	var originalPath string
	switch kind {
	case "file":
		file, err := findFileByReference(reference)
		if err != nil {
			return err
		}
		originalPath = file.Path
	case "folder":
		rel, ok := cleanRelPath(reference)
		if !ok || rel == "" {
			return os.ErrNotExist
		}
		info, err := os.Lstat(fullPath(rel))
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return os.ErrNotExist
		}
		originalPath = rel
	default:
		return errors.New("invalid item kind")
	}

	trashRoot := fullPath(trashDirectory)
	if err := os.MkdirAll(trashRoot, 0755); err != nil {
		return err
	}
	source := fullPath(originalPath)
	for attempts := 0; attempts < 3; attempts++ {
		id, err := generateUUID()
		if err != nil {
			return err
		}
		bucketPath := pathpkg.Join(trashDirectory, id)
		if err := os.Mkdir(fullPath(bucketPath), 0755); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return err
		}

		trashPath := pathpkg.Join(bucketPath, pathpkg.Base(originalPath))
		if err := os.Rename(source, fullPath(trashPath)); err != nil {
			_ = os.Remove(fullPath(bucketPath))
			return err
		}

		item := trashEntry{
			ID:           id,
			Kind:         kind,
			OriginalPath: originalPath,
			TrashPath:    trashPath,
			DeletedAt:    time.Now().UTC(),
		}
		persistentState.Lock()
		previous := append([]trashEntry(nil), persistentState.trash...)
		persistentState.trash = append(persistentState.trash, item)
		if err := savePersistentDataLocked(); err != nil {
			persistentState.trash = previous
			persistentState.Unlock()
			if rollbackErr := os.Rename(fullPath(trashPath), source); rollbackErr != nil {
				log.Printf("trash rollback error: %v", rollbackErr)
			}
			_ = os.Remove(fullPath(bucketPath))
			return err
		}
		persistentState.Unlock()
		return nil
	}
	return errors.New("unable to allocate trash item ID")
}

func restoreTrashItem(id string) error {
	if !isUUID(id) {
		return os.ErrNotExist
	}
	id = strings.ToLower(id)

	persistentState.Lock()
	defer persistentState.Unlock()

	index := -1
	var item trashEntry
	for i, candidate := range persistentState.trash {
		if candidate.ID == id {
			index, item = i, candidate
			break
		}
	}
	if index == -1 {
		return os.ErrNotExist
	}

	destination := fullPath(item.OriginalPath)
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := parentRel(item.OriginalPath)
	parentInfo, err := os.Stat(fullPath(parent))
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() {
		return os.ErrNotExist
	}

	source := fullPath(item.TrashPath)
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	previous := append([]trashEntry(nil), persistentState.trash...)
	persistentState.trash = append(persistentState.trash[:index], persistentState.trash[index+1:]...)
	if err := savePersistentDataLocked(); err != nil {
		persistentState.trash = previous
		if rollbackErr := os.Rename(destination, source); rollbackErr != nil {
			log.Printf("trash restore rollback error: %v", rollbackErr)
		}
		return err
	}
	_ = os.Remove(fullPath(pathpkg.Dir(item.TrashPath)))
	return nil
}

func permanentlyDeleteTrashItem(id string) error {
	if !isUUID(id) {
		return os.ErrNotExist
	}
	id = strings.ToLower(id)

	persistentState.Lock()
	index := -1
	var item trashEntry
	for i, candidate := range persistentState.trash {
		if candidate.ID == id {
			index, item = i, candidate
			break
		}
	}
	if index == -1 {
		persistentState.Unlock()
		return os.ErrNotExist
	}
	fileIDs := trashFileIDs(item)
	if err := os.RemoveAll(fullPath(item.TrashPath)); err != nil {
		persistentState.Unlock()
		return err
	}
	_ = os.Remove(fullPath(pathpkg.Dir(item.TrashPath)))
	previous := append([]trashEntry(nil), persistentState.trash...)
	persistentState.trash = append(persistentState.trash[:index], persistentState.trash[index+1:]...)
	if err := savePersistentDataLocked(); err != nil {
		persistentState.trash = previous
		persistentState.Unlock()
		return err
	}
	persistentState.Unlock()

	clearStarredForTrashItem(item, fileIDs)
	return nil
}

func trashFileIDs(item trashEntry) map[string]bool {
	ids := make(map[string]bool)
	if item.Kind == "file" {
		if id, _, ok := parseStoredFilename(pathpkg.Base(item.TrashPath)); ok {
			ids[id] = true
		}
		return ids
	}
	err := filepath.WalkDir(fullPath(item.TrashPath), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if id, _, ok := parseStoredFilename(entry.Name()); ok {
			ids[id] = true
		}
		return nil
	})
	if err != nil {
		log.Printf("trash star lookup error (%s): %v", item.ID, err)
	}
	return ids
}

func clearStarredForTrashItem(item trashEntry, fileIDs map[string]bool) {
	removeStarredEntries(func(starred starredEntry) bool {
		switch starred.Kind {
		case "file":
			return fileIDs[starred.ID]
		case "folder":
			return item.Kind == "folder" &&
				(starred.Path == item.OriginalPath || strings.HasPrefix(starred.Path, item.OriginalPath+"/"))
		default:
			return false
		}
	})
}

func cleanupExpiredTrash() {
	now := time.Now()
	persistentState.Lock()
	previous := append([]trashEntry(nil), persistentState.trash...)
	kept := make([]trashEntry, 0, len(previous))
	type expiredItem struct {
		entry   trashEntry
		fileIDs map[string]bool
	}
	expired := make([]expiredItem, 0)
	for _, item := range previous {
		if now.Before(item.DeletedAt.Add(trashRetention)) {
			kept = append(kept, item)
			continue
		}
		fileIDs := trashFileIDs(item)
		if err := os.RemoveAll(fullPath(item.TrashPath)); err != nil {
			log.Printf("expired trash item removal error (%s): %v", item.ID, err)
			kept = append(kept, item)
			continue
		}
		_ = os.Remove(fullPath(pathpkg.Dir(item.TrashPath)))
		expired = append(expired, expiredItem{entry: item, fileIDs: fileIDs})
	}
	if len(expired) == 0 {
		persistentState.Unlock()
		return
	}
	persistentState.trash = kept
	if err := savePersistentDataLocked(); err != nil {
		persistentState.trash = previous
		persistentState.Unlock()
		log.Printf("expired trash metadata save error: %v", err)
		return
	}
	persistentState.Unlock()

	for _, item := range expired {
		clearStarredForTrashItem(item.entry, item.fileIDs)
	}
}

func runTrashCleanup(ctx context.Context) {
	ticker := time.NewTicker(trashCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupExpiredTrash()
		}
	}
}

func restoreTrashHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if err := restoreTrashItem(r.URL.Query().Get("id")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, os.ErrExist) {
			http.Error(w, "an item with that name already exists", http.StatusConflict)
			return
		}
		log.Printf("trash restore error: %v", err)
		http.Error(w, "unable to restore item", http.StatusInternalServerError)
		return
	}
	invalidateStats()
	renderOOBOnly(w, r)
}

func trashHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodDelete) {
		return
	}
	if err := permanentlyDeleteTrashItem(r.URL.Query().Get("id")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		log.Printf("permanent trash deletion error: %v", err)
		http.Error(w, "unable to permanently delete item", http.StatusInternalServerError)
		return
	}
	invalidateStats()
	renderOOBOnly(w, r)
}

/* ------------------------------------------------------------------ */
/* Pedido de visualização, ordenação e pesquisa                       */
/* ------------------------------------------------------------------ */

// sortOptionsFromRequest lê ?sort=date|name|type&order=asc|desc. Sem parâmetros,
// usa o cookie "gollery_sort" (ex.: "name.asc") e, por fim, o padrão.
func sortOptionsFromRequest(r *http.Request) SortOptions {
	query := r.URL.Query()

	by := query.Get("sort")
	order := query.Get("order")

	if by == "" {
		if cookie, err := r.Cookie("gollery_sort"); err == nil {
			by, order, _ = strings.Cut(cookie.Value, ".")
		}
	}

	switch by {
	case "date", "name", "type":
	default:
		by = "date"
	}

	if order != "asc" && order != "desc" {
		if by == "date" {
			order = "desc"
		} else {
			order = "asc"
		}
	}

	return SortOptions{By: by, Order: order}
}

func viewRequestFrom(r *http.Request) (viewRequest, bool) {
	query := r.URL.Query()

	path, ok := cleanRelPath(query.Get("path"))
	if !ok {
		return viewRequest{}, false
	}

	search := strings.TrimSpace(query.Get("q"))

	if runes := []rune(search); len(runes) > 100 {
		search = string(runes[:100])
	}

	view := query.Get("view")
	if view != "" && view != "recent" && view != "starred" && view != "trash" && view != "photos" && view != "storage" {
		return viewRequest{}, false
	}

	return viewRequest{
		Path:  path,
		Query: search,
		View:  view,
		Opts:  sortOptionsFromRequest(r),
	}, true
}

func compareNames(a, b File) int {
	result := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))

	if result == 0 {
		result = strings.Compare(a.Id, b.Id)
	}

	return result
}

func sortFiles(files []File, opts SortOptions) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]

		var result int

		switch opts.By {
		case "name":
			result = compareNames(a, b)

		case "type":
			result = strings.Compare(a.Ext, b.Ext)

			if result == 0 {
				result = compareNames(a, b)
			}

		default: // date
			switch {
			case a.CreatedDate.Before(b.CreatedDate):
				result = -1
			case a.CreatedDate.After(b.CreatedDate):
				result = 1
			default:
				result = compareNames(a, b)
			}
		}

		if opts.Order == "desc" {
			return result > 0
		}

		return result < 0
	})
}

// sortFolders ordena pastas por data ou nome ("type" não se aplica a pastas).
func sortFolders(folders []Folder, opts SortOptions) {
	sort.SliceStable(folders, func(i, j int) bool {
		a, b := folders[i], folders[j]

		var result int

		if opts.By == "date" {
			switch {
			case a.CreatedDate.Before(b.CreatedDate):
				result = -1
			case a.CreatedDate.After(b.CreatedDate):
				result = 1
			}
		}

		if result == 0 {
			result = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}

		if opts.Order == "desc" {
			return result > 0
		}

		return result < 0
	})
}

func foldString(value string) string {
	return foldReplacer.Replace(strings.ToLower(value))
}

/* ------------------------------------------------------------------ */
/* Listagem e pesquisa                                                */
/* ------------------------------------------------------------------ */

func fileKind(ext string) string {
	switch {
	case videoExtensions[ext]:
		return "video"
	case imageExtensions[ext]:
		return "image"
	default:
		return "other"
	}
}

// fileFromInfo monta um File a partir de um arquivo em disco dentro de dir.
func fileFromInfo(dir string, info fs.FileInfo) (File, bool) {
	name := info.Name()

	if !info.Mode().IsRegular() || strings.HasPrefix(name, ".") {
		return File{}, false
	}

	id, originalName, ok := parseStoredFilename(name)
	if !ok {
		return File{}, false
	}

	ext := strings.ToLower(filepath.Ext(originalName))
	mimeType := mime.TypeByExtension(ext)

	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	return File{
		Id:          id,
		Name:        originalName,
		ServerName:  name,
		Path:        joinRel(dir, name),
		Dir:         dir,
		Ext:         ext,
		Kind:        fileKind(ext),
		MIMEType:    mimeType,
		Size:        info.Size(),
		CreatedDate: info.ModTime(),
	}, true
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

// folderStats calcula a quantidade de itens diretamente dentro da pasta e o tamanho total (recursivo) de seus arquivos.
func folderStats(rel string) (int, int64) {
	full := fullPath(rel)
	entries, err := os.ReadDir(full)
	if err != nil {
		return 0, 0
	}

	var itemCount int
	for _, entry := range entries {
		if validSegment(entry.Name()) {
			itemCount++
		}
	}

	var totalSize int64
	_ = filepath.WalkDir(full, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != full && !validSegment(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if validSegment(entry.Name()) {
			if info, err := entry.Info(); err == nil {
				totalSize += info.Size()
			}
		}
		return nil
	})

	return itemCount, totalSize
}

// listDir lista as pastas e os arquivos diretamente dentro de dir.
func listDir(dir string) ([]Folder, []File, error) {
	if !dirExists(dir) {
		return nil, nil, os.ErrNotExist
	}

	entries, err := os.ReadDir(fullPath(dir))
	if err != nil {
		return nil, nil, err
	}

	folders := make([]Folder, 0)
	files := make([]File, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()

		if !validSegment(name) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			log.Printf("file metadata error for %q: %v", name, err)
			continue
		}

		if entry.IsDir() {
			itemCount, size := folderStats(joinRel(dir, name))
			folders = append(folders, Folder{
				Name:        name,
				Path:        joinRel(dir, name),
				Dir:         dir,
				CreatedDate: info.ModTime(),
				ItemCount:   itemCount,
				Size:        size,
			})

			continue
		}

		if file, ok := fileFromInfo(dir, info); ok {
			files = append(files, file)
		}
	}

	return folders, files, nil
}

// searchLibrary procura por nome em toda a biblioteca (arquivos e pastas).
func searchLibrary(query string) ([]Folder, []File, error) {
	needle := foldString(query)

	folders := make([]Folder, 0)
	files := make([]File, 0)

	err := filepath.WalkDir(mediaDirectory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == mediaDirectory {
				return err
			}

			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if path == mediaDirectory {
			return nil
		}

		if len(folders)+len(files) >= maxSearchHits {
			return filepath.SkipAll
		}

		name := entry.Name()

		if !validSegment(name) {
			if entry.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		relative, relErr := filepath.Rel(mediaDirectory, path)
		if relErr != nil {
			return nil
		}

		rel := filepath.ToSlash(relative)
		dir := parentRel(rel)

		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}

		if entry.IsDir() {
			if strings.Contains(foldString(name), needle) {
				itemCount, size := folderStats(rel)
				folders = append(folders, Folder{
					Name:        name,
					Path:        rel,
					Dir:         dir,
					Located:     true,
					CreatedDate: info.ModTime(),
					ItemCount:   itemCount,
					Size:        size,
				})
			}

			return nil
		}

		file, ok := fileFromInfo(dir, info)
		if ok && strings.Contains(foldString(file.Name), needle) {
			file.Located = true
			files = append(files, file)
		}

		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return folders, files, nil
}

// walkFiles visita todos os arquivos da biblioteca. visit retorna true para parar.
func walkFiles(visit func(File) bool) error {
	return filepath.WalkDir(mediaDirectory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == mediaDirectory {
				return err
			}

			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if path == mediaDirectory {
			return nil
		}

		if !validSegment(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if entry.IsDir() {
			return nil
		}

		relative, relErr := filepath.Rel(mediaDirectory, path)
		if relErr != nil {
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}

		file, ok := fileFromInfo(parentRel(filepath.ToSlash(relative)), info)
		if !ok {
			return nil
		}

		if visit(file) {
			return filepath.SkipAll
		}

		return nil
	})
}

func filterRecentFiles(files []File, now time.Time) []File {
	cutoff := now.Add(-recentWindow)
	recent := make([]File, 0)
	for _, file := range files {
		if !file.CreatedDate.After(cutoff) || file.CreatedDate.After(now) {
			continue
		}
		recent = append(recent, file)
	}

	sort.SliceStable(recent, func(i, j int) bool {
		return recent[i].CreatedDate.After(recent[j].CreatedDate)
	})
	return recent
}

func recentFiles(now time.Time) ([]File, error) {
	files := make([]File, 0)
	if err := walkFiles(func(file File) bool {
		files = append(files, file)
		return false
	}); err != nil {
		return nil, err
	}
	return filterRecentFiles(files, now), nil
}

func photoFiles() ([]File, error) {
	files := make([]File, 0)
	if err := walkFiles(func(file File) bool {
		if file.Kind == "image" {
			files = append(files, file)
		}
		return false
	}); err != nil {
		return nil, err
	}
	sortFiles(files, SortOptions{By: "date", Order: "desc"})
	return files, nil
}

func starredSnapshot() map[string]starredEntry {
	persistentState.RLock()
	defer persistentState.RUnlock()

	items := make(map[string]starredEntry, len(persistentState.starred))
	for key, item := range persistentState.starred {
		items[key] = item
	}
	return items
}

func annotateStarred(folders []Folder, files []File) {
	items := starredSnapshot()
	for i := range folders {
		_, folders[i].Starred = items[starredEntryKey(starredEntry{
			Kind: "folder",
			Path: folders[i].Path,
		})]
	}
	for i := range files {
		_, files[i].Starred = items[starredEntryKey(starredEntry{
			Kind: "file",
			ID:   files[i].Id,
		})]
	}
}

func starredItems() ([]Folder, []File, error) {
	items := starredSnapshot()
	fileIDs := make(map[string]bool)
	folderPaths := make([]string, 0)
	for _, item := range items {
		switch item.Kind {
		case "file":
			fileIDs[item.ID] = true
		case "folder":
			folderPaths = append(folderPaths, item.Path)
		}
	}

	folders := make([]Folder, 0, len(folderPaths))
	for _, path := range folderPaths {
		info, err := os.Lstat(fullPath(path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if !info.IsDir() {
			continue
		}

		itemCount, size := folderStats(path)
		folders = append(folders, Folder{
			Name:        pathpkg.Base(path),
			Path:        path,
			Dir:         parentRel(path),
			Located:     true,
			CreatedDate: info.ModTime(),
			Starred:     true,
			ItemCount:   itemCount,
			Size:        size,
		})
	}

	files := make([]File, 0, len(fileIDs))
	if err := walkFiles(func(file File) bool {
		if fileIDs[file.Id] {
			file.Located = true
			file.Starred = true
			files = append(files, file)
		}
		return false
	}); err != nil {
		return nil, nil, err
	}

	return folders, files, nil
}

func trashedItems() ([]Folder, []File, error) {
	persistentState.RLock()
	items := append([]trashEntry(nil), persistentState.trash...)
	persistentState.RUnlock()

	folders := make([]Folder, 0)
	files := make([]File, 0)
	for _, item := range items {
		info, err := os.Lstat(fullPath(item.TrashPath))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if item.Kind == "folder" {
			if !info.IsDir() {
				continue
			}
			folders = append(folders, Folder{
				Name:         pathpkg.Base(item.OriginalPath),
				Path:         item.OriginalPath,
				Dir:          parentRel(item.OriginalPath),
				Located:      true,
				CreatedDate:  info.ModTime(),
				Trashed:      true,
				TrashID:      item.ID,
				OriginalPath: item.OriginalPath,
				DeletedAt:    item.DeletedAt,
			})
			continue
		}

		file, ok := fileFromInfo(parentRel(item.TrashPath), info)
		if !ok {
			continue
		}
		file.Path = item.OriginalPath
		file.Dir = parentRel(item.OriginalPath)
		file.Located = true
		file.Trashed = true
		file.TrashID = item.ID
		file.OriginalPath = item.OriginalPath
		file.DeletedAt = item.DeletedAt
		files = append(files, file)
	}

	sort.Slice(folders, func(i, j int) bool {
		return folders[i].DeletedAt.After(folders[j].DeletedAt)
	})
	sort.Slice(files, func(i, j int) bool {
		return files[i].DeletedAt.After(files[j].DeletedAt)
	})
	return folders, files, nil
}

func filterViewByQuery(folders []Folder, files []File, query string) ([]Folder, []File) {
	if query == "" {
		return folders, files
	}

	needle := foldString(query)
	filteredFolders := make([]Folder, 0, len(folders))
	for _, folder := range folders {
		if strings.Contains(foldString(folder.Name), needle) {
			filteredFolders = append(filteredFolders, folder)
		}
	}

	filteredFiles := make([]File, 0, len(files))
	for _, file := range files {
		if strings.Contains(foldString(file.Name), needle) {
			filteredFiles = append(filteredFiles, file)
		}
	}
	return filteredFolders, filteredFiles
}

func loadView(request viewRequest) (View, error) {
	var (
		folders []Folder
		files   []File
		err     error
	)

	switch request.View {
	case "storage":
		// A seção de armazenamento não lista arquivos.
	case "recent":
		files, err = recentFiles(time.Now())
	case "photos":
		files, err = photoFiles()
	case "starred":
		folders, files, err = starredItems()
	case "trash":
		folders, files, err = trashedItems()
	default:
		if request.Query != "" {
			folders, files, err = searchLibrary(request.Query)
		} else {
			folders, files, err = listDir(request.Path)
		}
	}

	if err != nil {
		return View{}, err
	}

	if request.View != "" && request.Query != "" {
		folders, files = filterViewByQuery(folders, files, request.Query)
	}

	if request.View != "trash" {
		annotateStarred(folders, files)
	}
	if request.View == "trash" {
		sort.Slice(folders, func(i, j int) bool {
			return folders[i].DeletedAt.After(folders[j].DeletedAt)
		})
		sort.Slice(files, func(i, j int) bool {
			return files[i].DeletedAt.After(files[j].DeletedAt)
		})
	} else {
		sortFolders(folders, request.Opts)
	}
	if request.View == "recent" || request.View == "photos" {
		sortFiles(files, SortOptions{By: "date", Order: "desc"})
	} else if request.View != "trash" {
		sortFiles(files, request.Opts)
	}

	return View{
		Folders: folders,
		Files:   files,
		Count: ViewCount{
			Folders: len(folders),
			Files:   len(files),
			Search:  request.Query != "",
		},
	}, nil
}

/* ------------------------------------------------------------------ */
/* Estatísticas                                                       */
/* ------------------------------------------------------------------ */

func invalidateStats() {
	libraryStats.Lock()
	libraryStats.valid = false
	libraryStats.Unlock()
}

// librarySize retorna o total de bytes e de arquivos, com cache curto que é
// invalidado a cada upload/exclusão.
func librarySize() (int64, int) {
	libraryStats.Lock()
	defer libraryStats.Unlock()

	if libraryStats.valid && time.Since(libraryStats.computed) < statsTTL {
		return libraryStats.used, libraryStats.files
	}

	var (
		used  int64
		count int
	)

	err := walkFiles(func(file File) bool {
		used += file.Size
		count++

		return false
	})

	if err != nil {
		log.Printf("library size error: %v", err)
		return libraryStats.used, libraryStats.files
	}

	libraryStats.used = used
	libraryStats.files = count
	libraryStats.computed = time.Now()
	libraryStats.valid = true

	return used, count
}

func buildStats() StorageStats {
	used, count := librarySize()

	stats := StorageStats{FileCount: count, UsedBytes: used}

	total, free, ok := diskUsage(mediaDirectory)

	if !ok || total <= 0 {
		return stats
	}

	other := total - free - stats.UsedBytes

	if other < 0 {
		other = 0
	}

	stats.HasDisk = true
	stats.DiskTotal = total
	stats.DiskFree = free
	stats.GalleryPct = roundPercent(float64(stats.UsedBytes) / float64(total) * 100)
	stats.OtherPct = roundPercent(float64(other) / float64(total) * 100)

	if sum := stats.GalleryPct + stats.OtherPct; sum > 100 {
		stats.OtherPct = roundPercent(100 - stats.GalleryPct)
	}

	return stats
}

func currentDeviceInfo() DeviceInfo {
	hostname, err := os.Hostname()
	if err != nil {
		log.Printf("hostname lookup error: %v", err)
	}

	return DeviceInfo{
		Hostname:        hostname,
		OperatingSystem: runtime.GOOS,
		Architecture:    runtime.GOARCH,
	}
}

func roundPercent(value float64) float64 {
	if value < 0 {
		return 0
	}

	if value > 100 {
		return 100
	}

	return float64(int(value*100+0.5)) / 100
}

func formatBytes(n int64) string {
	const unit = 1024

	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	divisor, exp := int64(unit), 0

	for remaining := n / unit; remaining >= unit; remaining /= unit {
		divisor *= unit
		exp++
	}

	value := fmt.Sprintf("%.1f %cB", float64(n)/float64(divisor), "KMGTPE"[exp])

	return strings.Replace(value, ".", ",", 1)
}

/* ------------------------------------------------------------------ */
/* Renderização                                                       */
/* ------------------------------------------------------------------ */

func writeHTML(w http.ResponseWriter, output *bytes.Buffer) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if _, err := w.Write(output.Bytes()); err != nil {
		log.Printf("response write error: %v", err)
	}
}

func viewError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}

	log.Printf("list files error: %v", err)
	http.Error(w, "unable to list files", http.StatusInternalServerError)
}

// writeOOB escreve o contador que o HTMX aplica fora do alvo principal.
func writeOOB(output *bytes.Buffer, count ViewCount) error {
	return tmpl.ExecuteTemplate(output, "file-count", count)
}

func renderPage(w http.ResponseWriter, r *http.Request) {
	request, ok := viewRequestFrom(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	view, err := loadView(request)
	if err != nil {
		viewError(w, r, err)
		return
	}

	var latestPhotos []File
	if request.View == "photos" {
		latestPhotos = view.Files
		if len(latestPhotos) > 10 {
			latestPhotos = latestPhotos[:10]
		}
	}

	var device DeviceInfo
	if request.View == "storage" {
		device = currentDeviceInfo()
	}

	var output bytes.Buffer

	if err := tmpl.Execute(&output, FileData{
		Folders:      view.Folders,
		Files:        view.Files,
		LatestPhotos: latestPhotos,
		Count:        view.Count,
		Stats:        buildStats(),
		Device:       device,
		View:         request.View,
		Path:         request.Path,
		Crumbs:       buildCrumbs(request.Path),
		Query:        request.Query,
		Sort:         request.Opts.By,
		Order:        request.Opts.Order,
	}); err != nil {
		log.Printf("template execution error: %v", err)
		http.Error(w, "unable to render page", http.StatusInternalServerError)
		return
	}

	writeHTML(w, &output)
}

// renderList responde ao HTMX com os cards (pastas e arquivos) e os fragmentos OOB.
func renderList(w http.ResponseWriter, r *http.Request) {
	request, ok := viewRequestFrom(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	view, err := loadView(request)
	if err != nil {
		viewError(w, r, err)
		return
	}

	var output bytes.Buffer

	for _, folder := range view.Folders {
		if err := tmpl.ExecuteTemplate(&output, "folder-card", folder); err != nil {
			log.Printf("folder template execution error: %v", err)
			http.Error(w, "unable to render folders", http.StatusInternalServerError)
			return
		}
	}

	fileTemplate := "file-card"
	if request.View == "photos" {
		fileTemplate = "photo-card"
	}
	for _, file := range view.Files {
		if err := tmpl.ExecuteTemplate(&output, fileTemplate, file); err != nil {
			log.Printf("file template execution error: %v", err)
			http.Error(w, "unable to render files", http.StatusInternalServerError)
			return
		}
	}

	if len(view.Folders) == 0 && len(view.Files) == 0 {
		if err := tmpl.ExecuteTemplate(&output, "empty-state", emptyStateData{
			Query: request.Query,
			View:  request.View,
		}); err != nil {
			log.Printf("empty template execution error: %v", err)
			http.Error(w, "unable to render files", http.StatusInternalServerError)
			return
		}
	}

	if err := writeOOB(&output, view.Count); err != nil {
		log.Printf("oob template execution error: %v", err)
		http.Error(w, "unable to render page", http.StatusInternalServerError)
		return
	}

	writeHTML(w, &output)
}

// renderOOBOnly responde apenas com contador e armazenamento atualizados,
// calculados para a pasta/pesquisa atual (enviadas pelo cliente).
func renderOOBOnly(w http.ResponseWriter, r *http.Request) {
	request, ok := viewRequestFrom(r)
	if !ok {
		request = viewRequest{Opts: sortOptionsFromRequest(r)}
	}

	view, err := loadView(request)
	if err != nil {
		viewError(w, r, err)
		return
	}

	var output bytes.Buffer

	if err := writeOOB(&output, view.Count); err != nil {
		log.Printf("oob template execution error: %v", err)
		http.Error(w, "unable to render stats", http.StatusInternalServerError)
		return
	}

	writeHTML(w, &output)
}

/* ------------------------------------------------------------------ */
/* Busca de arquivos                                                  */
/* ------------------------------------------------------------------ */

// findFileByPath resolve um caminho relativo (já normalizado) para um arquivo,
// sem listar diretórios.
func findFileByPath(raw string) (File, error) {
	rel, ok := cleanRelPath(raw)
	if !ok || rel == "" {
		return File{}, os.ErrNotExist
	}

	info, err := os.Lstat(fullPath(rel))
	if err != nil {
		return File{}, err
	}

	file, ok := fileFromInfo(parentRel(rel), info)
	if !ok {
		return File{}, os.ErrNotExist
	}

	return file, nil
}

func findFileByID(id string) (File, error) {
	if !isUUID(id) {
		return File{}, os.ErrNotExist
	}

	id = strings.ToLower(id)

	var (
		found File
		ok    bool
	)

	err := walkFiles(func(file File) bool {
		if file.Id == id {
			found, ok = file, true
			return true
		}

		return false
	})

	if err != nil {
		return File{}, err
	}

	if !ok {
		return File{}, os.ErrNotExist
	}

	return found, nil
}

func findFileByReference(reference string) (File, error) {
	if isUUID(reference) {
		return findFileByID(reference)
	}

	return findFileByPath(reference)
}

func removeFileByReference(reference string) error {
	file, err := findFileByReference(reference)
	if err != nil {
		return err
	}

	if err := os.Remove(fullPath(file.Path)); err != nil {
		return err
	}
	removeStarredEntries(func(item starredEntry) bool {
		return item.Kind == "file" && item.ID == file.Id
	})
	return nil
}

func removeFolder(raw string) error {
	rel, ok := cleanRelPath(raw)
	if !ok || rel == "" {
		return os.ErrNotExist
	}

	info, err := os.Lstat(fullPath(rel))
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return os.ErrNotExist
	}

	if err := os.RemoveAll(fullPath(rel)); err != nil {
		return err
	}
	removeStarredEntries(func(item starredEntry) bool {
		return item.Kind == "folder" &&
			(item.Path == rel || strings.HasPrefix(item.Path, rel+"/"))
	})
	return nil
}

/* ------------------------------------------------------------------ */
/* Upload                                                             */
/* ------------------------------------------------------------------ */

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	dir, ok := cleanRelPath(r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if !dirExists(dir) {
		http.NotFound(w, r)
		return
	}

	// Sem limite fixo de tamanho: o único limite é o espaço livre no disco.
	// Quando o cliente informa o tamanho total, recusa cedo, antes de ler o corpo.
	if r.ContentLength > 0 {
		if _, free, ok := diskUsage(mediaDirectory); ok && r.ContentLength > free {
			http.Error(w, "not enough free storage space", http.StatusInsufficientStorage)
			return
		}
	}

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

		file, saveErr := savePart(part, dir)
		closeErr := part.Close()

		if saveErr != nil {
			rollbackUploadedFiles(savedFiles)

			if isNoSpace(saveErr) {
				http.Error(w, "not enough free storage space", http.StatusInsufficientStorage)
				return
			}

			log.Printf("file upload error: %v", saveErr)
			http.Error(w, "unable to save file", http.StatusInternalServerError)
			return
		}

		if closeErr != nil {
			_ = os.Remove(fullPath(file.Path))
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

	invalidateStats()

	// O cliente envia um arquivo por requisição (para mostrar o progresso) e
	// recarrega a lista uma única vez ao final, então não há corpo na resposta.
	w.WriteHeader(http.StatusNoContent)
}

func savePart(part *multipart.Part, dir string) (File, error) {
	originalName := sanitizeFilename(part.FileName())
	directory := fullPath(dir)

	// O temporário fica na própria pasta de destino para que o os.Link seja
	// sempre dentro do mesmo sistema de arquivos.
	tempFile, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		return File{}, err
	}

	tempPath := tempFile.Name()

	defer os.Remove(tempPath)

	_, copyErr := io.Copy(tempFile, part)

	closeErr := tempFile.Close()

	if copyErr != nil {
		return File{}, copyErr
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
		destination := filepath.Join(directory, serverName)

		if err := os.Link(tempPath, destination); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}

			return File{}, err
		}

		info, err := os.Lstat(destination)
		if err != nil {
			os.Remove(destination)
			return File{}, err
		}

		file, ok := fileFromInfo(dir, info)
		if !ok {
			os.Remove(destination)
			return File{}, errors.New("stored file has an invalid name")
		}

		return file, nil
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
		if err := os.Remove(fullPath(file.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("upload rollback error for %q: %v", file.Path, err)
		}
	}
}

// isNoSpace informa se o erro indica falta de espaço (ou cota) no disco.
func isNoSpace(err error) bool {
	return errors.Is(err, errInsufficientStorage) ||
		errors.Is(err, syscall.ENOSPC) ||
		errors.Is(err, syscall.EDQUOT)
}

func writeMultipartError(w http.ResponseWriter, err error) {
	if isNoSpace(err) {
		http.Error(w, "not enough free storage space", http.StatusInsufficientStorage)
		return
	}

	log.Printf("multipart error: %v", err)
	http.Error(w, "invalid upload", http.StatusBadRequest)
}

/* ------------------------------------------------------------------ */
/* Pastas                                                             */
/* ------------------------------------------------------------------ */

// sanitizeFolderName valida o nome de uma nova pasta.
func sanitizeFolderName(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" || strings.ContainsAny(raw, `/\`) {
		return "", false
	}

	name := sanitizeFilename(raw)

	if !validSegment(name) {
		return "", false
	}

	return name, true
}

func foldersHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	parent, ok := cleanRelPath(r.PostForm.Get("path"))
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	name, ok := sanitizeFolderName(r.PostForm.Get("name"))
	if !ok {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}

	if !dirExists(parent) {
		http.NotFound(w, r)
		return
	}

	if err := os.Mkdir(fullPath(joinRel(parent, name)), 0755); err != nil {
		switch {
		case errors.Is(err, os.ErrExist):
			http.Error(w, "folder already exists", http.StatusConflict)
		case isNoSpace(err):
			http.Error(w, "not enough free storage space", http.StatusInsufficientStorage)
		default:
			log.Printf("folder creation error: %v", err)
			http.Error(w, "unable to create folder", http.StatusInternalServerError)
		}

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

/* ------------------------------------------------------------------ */
/* Exclusão, download e mídia                                         */
/* ------------------------------------------------------------------ */

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
		if err := moveToTrash("file", id); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}

			log.Printf("file trash error: %v", err)
			http.Error(w, "unable to move file to trash", http.StatusInternalServerError)
			return
		}

		invalidateStats()
		renderOOBOnly(w, r)
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

// deleteHandler move um arquivo (?file=<caminho>) ou uma pasta (?folder=<caminho>) para a lixeira.
func deleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodDelete) {
		return
	}

	query := r.URL.Query()

	var err error

	switch {
	case query.Get("folder") != "":
		err = moveToTrash("folder", query.Get("folder"))
	case query.Get("file") != "":
		err = moveToTrash("file", query.Get("file"))
	default:
		http.Error(w, "missing file or folder", http.StatusBadRequest)
		return
	}

	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}

		log.Printf("trash error: %v", err)
		http.Error(w, "unable to move item to trash", http.StatusInternalServerError)
		return
	}

	invalidateStats()
	renderOOBOnly(w, r)
}

// BulkDeleteRequest representa uma requisição de exclusão em lote.
type BulkDeleteRequest struct {
	Items []BulkItem `json:"items"`
}

// BulkItem representa um item (arquivo ou pasta) para operações em lote.
type BulkItem struct {
	Kind string `json:"kind"` // "file" ou "folder"
	Path string `json:"path"`
}

// bulkDeleteHandler move múltiplos arquivos e/ou pastas para a lixeira.
func bulkDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	var req BulkDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if len(req.Items) == 0 {
		http.Error(w, "no items provided", http.StatusBadRequest)
		return
	}

	var firstErr error
	items := append([]BulkItem(nil), req.Items...)
	itemRank := func(kind string) int {
		switch kind {
		case "file":
			return 0
		case "folder":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return itemRank(items[i].Kind) < itemRank(items[j].Kind)
	})
	for _, item := range items {
		var err error
		switch item.Kind {
		case "file":
			err = moveToTrash("file", item.Path)
		case "folder":
			err = moveToTrash("folder", item.Path)
		default:
			err = fmt.Errorf("invalid item kind: %s", item.Kind)
		}

		if err != nil && firstErr == nil {
			firstErr = err
			log.Printf("bulk delete error for %s:%s: %v", item.Kind, item.Path, err)
		}
	}

	if firstErr != nil {
		if errors.Is(firstErr, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "unable to delete some items", http.StatusInternalServerError)
		return
	}

	invalidateStats()
	w.WriteHeader(http.StatusNoContent)
}

// BulkDownloadRequest representa uma requisição de download em lote.
type BulkDownloadRequest struct {
	Items []BulkItem `json:"items"`
}

// bulkDownloadHandler cria um arquivo ZIP com os itens selecionados.
func bulkDownloadHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	var req BulkDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if len(req.Items) == 0 {
		http.Error(w, "no items provided", http.StatusBadRequest)
		return
	}

	// Define headers para download do ZIP
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="gollery-selecao.zip"`)

	zw := zip.NewWriter(w)
	defer zw.Close()

	// Buffer para copiar arquivos
	buf := make([]byte, 32*1024)

	for _, item := range req.Items {
		switch item.Kind {
		case "file":
			file, err := findFileByReference(item.Path)
			if err != nil {
				log.Printf("bulk download: file not found %s: %v", item.Path, err)
				continue
			}
			if err := addFileToZip(zw, file, buf); err != nil {
				log.Printf("bulk download: error adding file %s: %v", file.Name, err)
			}
		case "folder":
			if err := addFolderToZip(zw, item.Path, buf); err != nil {
				log.Printf("bulk download: error adding folder %s: %v", item.Path, err)
			}
		}
	}
}

// addFileToZip adiciona um único arquivo ao ZIP.
func addFileToZip(zw *zip.Writer, file File, buf []byte) error {
	src, err := os.Open(fullPath(file.Path))
	if err != nil {
		return err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = file.Name
	header.Method = zip.Deflate

	entry, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.CopyBuffer(entry, src, buf)
	return err
}

// addFolderToZip adiciona uma pasta (recursivamente) ao ZIP.
func addFolderToZip(zw *zip.Writer, relPath string, buf []byte) error {
	full := fullPath(relPath)

	return filepath.WalkDir(full, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Pula a própria pasta raiz
		if path == full {
			return nil
		}

		// Obtém o caminho relativo dentro da pasta
		rel, err := filepath.Rel(full, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		info, err := entry.Info()
		if err != nil {
			return err
		}

		if entry.IsDir() {
			// Adiciona entrada de diretório
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = rel + "/"
			header.Method = zip.Store
			_, err = zw.CreateHeader(header)
			return err
		}

		// Para arquivos, tenta obter o nome original (sem UUID)
		name := entry.Name()
		_, originalName, ok := parseStoredFilename(name)
		if !ok {
			originalName = name
		}

		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = originalName
		header.Method = zip.Deflate

		zipEntry, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		_, err = io.CopyBuffer(zipEntry, src, buf)
		return err
	})
}

// BulkMoveRequest representa uma requisição de mover itens em lote.
type BulkMoveRequest struct {
	Items      []BulkItem `json:"items"`
	TargetPath string     `json:"targetPath"`
}

// bulkMoveHandler move múltiplos arquivos e/ou pastas para uma pasta de destino.
func bulkMoveHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	var req BulkMoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if len(req.Items) == 0 {
		http.Error(w, "no items provided", http.StatusBadRequest)
		return
	}

	// Valida se o destino existe e é uma pasta
	if !dirExists(req.TargetPath) {
		http.NotFound(w, r)
		return
	}

	var firstErr error
	for _, item := range req.Items {
		var err error
		switch item.Kind {
		case "file":
			err = moveFile(item.Path, req.TargetPath)
		case "folder":
			err = moveFolder(item.Path, req.TargetPath)
		default:
			err = fmt.Errorf("invalid item kind: %s", item.Kind)
		}

		if err != nil && firstErr == nil {
			firstErr = err
			log.Printf("bulk move error for %s:%s: %v", item.Kind, item.Path, err)
		}
	}

	if firstErr != nil {
		if errors.Is(firstErr, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(firstErr, os.ErrExist) {
			http.Error(w, "item already exists in destination", http.StatusConflict)
			return
		}
		http.Error(w, "unable to move some items", http.StatusInternalServerError)
		return
	}

	invalidateStats()
	w.WriteHeader(http.StatusNoContent)
}

// moveFile move um arquivo para uma pasta de destino.
func moveFile(filePath, targetDir string) error {
	file, err := findFileByReference(filePath)
	if err != nil {
		return err
	}

	// Constrói o novo caminho mantendo o nome do servidor (com UUID)
	newPath := joinRel(targetDir, file.ServerName)

	// Verifica se já existe
	if _, err := os.Lstat(fullPath(newPath)); err == nil {
		return os.ErrExist
	}

	// Move o arquivo (os.Rename funciona dentro do mesmo filesystem)
	return os.Rename(fullPath(file.Path), fullPath(newPath))
}

// moveFolder move uma pasta (recursivamente) para uma pasta de destino.
func moveFolder(folderPath, targetDir string) error {
	rel, ok := cleanRelPath(folderPath)
	if !ok || rel == "" {
		return os.ErrNotExist
	}

	// Não pode mover para si mesmo ou para um subdiretório
	if rel == targetDir || strings.HasPrefix(targetDir, rel+"/") {
		return errors.New("cannot move folder into itself")
	}

	src := fullPath(rel)
	dst := fullPath(joinRel(targetDir, filepath.Base(rel)))

	// Verifica se destino já existe
	if _, err := os.Lstat(dst); err == nil {
		return os.ErrExist
	}

	// Move a pasta
	if err := os.Rename(src, dst); err != nil {
		return err
	}

	newPath := joinRel(targetDir, filepath.Base(rel))
	if err := rebaseFolderMetadata(rel, newPath); err != nil {
		if rollbackErr := os.Rename(dst, src); rollbackErr != nil {
			log.Printf("folder move rollback error: %v", rollbackErr)
		}
		return fmt.Errorf("update folder metadata after move: %w", err)
	}

	return nil
}

func mediaHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodHead) {
		return
	}

	file, err := findFileByPath(strings.TrimPrefix(r.URL.Path, "/media/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	serveStoredFile(w, r, file, r.URL.Query().Get("download") == "1")
}

func trashPreviewHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodHead) {
		return
	}

	id := strings.ToLower(r.URL.Query().Get("id"))
	if !isUUID(id) {
		http.NotFound(w, r)
		return
	}

	persistentState.RLock()
	var item trashEntry
	found := false
	for _, candidate := range persistentState.trash {
		if candidate.ID == id && candidate.Kind == "file" {
			item = candidate
			found = true
			break
		}
	}
	persistentState.RUnlock()
	if !found {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(fullPath(item.TrashPath))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("trash preview: unable to stat %s: %v", item.TrashPath, err)
			http.Error(w, "unable to read trashed file", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
		return
	}

	file, ok := fileFromInfo(parentRel(item.TrashPath), info)
	if !ok || file.Kind != "image" {
		http.NotFound(w, r)
		return
	}
	file.Path = item.TrashPath
	serveStoredFile(w, r, file, false)
}

func serveStoredFile(w http.ResponseWriter, r *http.Request, file File, attachment bool) {
	src, err := os.Open(fullPath(file.Path))
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

// cleanupTemporaryFiles remove uploads interrompidos em qualquer pasta.
func cleanupTemporaryFiles() {
	err := filepath.WalkDir(mediaDirectory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() && path != mediaDirectory {
				return filepath.SkipDir
			}

			return err
		}

		if entry.IsDir() {
			if filepath.Clean(path) == filepath.Clean(fullPath(trashDirectory)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(entry.Name(), ".upload-") {
			return nil
		}

		if err := os.Remove(path); err != nil {
			log.Printf("temporary file removal error: %v", err)
		}

		return nil
	})

	if err != nil {
		log.Printf("temporary file cleanup error: %v", err)
	}
}
