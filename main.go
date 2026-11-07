package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

const (
	mediaDirectory = "media"
	maxSearchHits  = 2000
	statsTTL       = 30 * time.Second
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
	Id          string
	Name        string
	ServerName  string // nome em disco: <uuid>__<nome>
	Path        string // caminho relativo a media/, com "/" (ex.: "viagens/2024/uuid__foto.jpg")
	Dir         string // pasta que contém o arquivo ("" = raiz)
	Located     bool   // mostrar a localização (resultados de pesquisa)
	Ext         string
	Kind        string // "image", "video" ou "other"
	MIMEType    string
	Size        int64
	CreatedDate time.Time
}

// URL é o endereço escapado do arquivo em /media/.
func (f File) URL() string {
	return (&url.URL{Path: "/media/" + f.Path}).EscapedPath()
}

func (f File) SizeLabel() string { return formatBytes(f.Size) }

func (f File) DateLabel() string { return f.CreatedDate.Local().Format("02/01/2006 15:04") }

func (f File) Location() string { return locationLabel(f.Dir) }

type Folder struct {
	Name        string
	Path        string
	Dir         string
	Located     bool
	CreatedDate time.Time
}

func (f Folder) Location() string { return locationLabel(f.Dir) }

func locationLabel(dir string) string {
	if dir == "" {
		return "Início"
	}

	return "Início / " + strings.ReplaceAll(dir, "/", " / ")
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
	Folders []Folder
	Files   []File
	Count   ViewCount
	Stats   StorageStats
	Path    string
	Crumbs  []Crumb
	Query   string
	Sort    string
	Order   string
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
	Opts  SortOptions
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
	mux.HandleFunc("/folders", foldersHandler)
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

	return viewRequest{
		Path:  path,
		Query: search,
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
			folders = append(folders, Folder{
				Name:        name,
				Path:        joinRel(dir, name),
				Dir:         dir,
				CreatedDate: info.ModTime(),
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
				folders = append(folders, Folder{
					Name:        name,
					Path:        rel,
					Dir:         dir,
					Located:     true,
					CreatedDate: info.ModTime(),
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

func loadView(request viewRequest) (View, error) {
	var (
		folders []Folder
		files   []File
		err     error
	)

	if request.Query != "" {
		folders, files, err = searchLibrary(request.Query)
	} else {
		folders, files, err = listDir(request.Path)
	}

	if err != nil {
		return View{}, err
	}

	sortFolders(folders, request.Opts)
	sortFiles(files, request.Opts)

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

// writeOOB escreve os fragmentos "out-of-band" (contador e armazenamento) que
// o HTMX aplica em elementos fora do alvo principal da requisição.
func writeOOB(output *bytes.Buffer, count ViewCount) error {
	if err := tmpl.ExecuteTemplate(output, "file-count", count); err != nil {
		return err
	}

	return tmpl.ExecuteTemplate(output, "storage-stats", buildStats())
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

	var output bytes.Buffer

	if err := tmpl.Execute(&output, FileData{
		Folders: view.Folders,
		Files:   view.Files,
		Count:   view.Count,
		Stats:   buildStats(),
		Path:    request.Path,
		Crumbs:  buildCrumbs(request.Path),
		Query:   request.Query,
		Sort:    request.Opts.By,
		Order:   request.Opts.Order,
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

	for _, file := range view.Files {
		if err := tmpl.ExecuteTemplate(&output, "file-card", file); err != nil {
			log.Printf("file template execution error: %v", err)
			http.Error(w, "unable to render files", http.StatusInternalServerError)
			return
		}
	}

	if len(view.Folders) == 0 && len(view.Files) == 0 {
		if err := tmpl.ExecuteTemplate(&output, "empty-state", request.Query); err != nil {
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

	return os.Remove(fullPath(file.Path))
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

	return os.RemoveAll(fullPath(rel))
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
		if err := removeFileByReference(id); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}

			log.Printf("file deletion error: %v", err)
			http.Error(w, "unable to delete file", http.StatusInternalServerError)
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

// deleteHandler remove um arquivo (?file=<caminho>) ou uma pasta com todo o seu
// conteúdo (?folder=<caminho>). Os demais parâmetros (path, q, sort, order)
// descrevem a visualização atual, usada para atualizar o contador.
func deleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodDelete) {
		return
	}

	query := r.URL.Query()

	var err error

	switch {
	case query.Get("folder") != "":
		err = removeFolder(query.Get("folder"))
	case query.Get("file") != "":
		err = removeFileByReference(query.Get("file"))
	default:
		http.Error(w, "missing file or folder", http.StatusBadRequest)
		return
	}

	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}

		log.Printf("deletion error: %v", err)
		http.Error(w, "unable to delete", http.StatusInternalServerError)
		return
	}

	invalidateStats()
	renderOOBOnly(w, r)
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

		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".upload-") {
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
