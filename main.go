package main

import (
	"embed"
	"html/template"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type File struct {
	Id          string
	Name        string
	ServerName  string
	Ext         string
	CreatedDate time.Time
}

type FileData struct {
	Files []File
}

//go:embed web/*
var embeddedFiles embed.FS

var tmpl = template.Must(
	template.ParseFS(
		embeddedFiles,
		"web/index.html",
	),
)

func main() {
	os.MkdirAll("media", 0755)

	http.Handle(
		"/media/",
		http.StripPrefix(
			"/media/",
			http.FileServer(http.Dir("media")),
		),
	)

	staticFS, err := fs.Sub(embeddedFiles, "web/static")
	if err != nil {
		panic(err)
	}

	http.Handle(
		"/static/",
		http.StripPrefix(
			"/static/",
			http.FileServer(http.FS(staticFS)),
		),
	)

	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/upload", uploadHandler)
	http.HandleFunc("/delete", deleteHandler)

	http.ListenAndServe(":8080", nil)
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	renderAllFiles(w)
}

func renderAllFiles(w http.ResponseWriter) {
	files := getAllFiles()

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name > files[j].Name
	})

	tmpl.Execute(w, FileData{
		Files: files,
	})
}

func getAllFiles() []File {
	var files []File

	entries, _ := os.ReadDir("media")

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))

		files = append(files, File{
			Id:         name[:36],
			Name:       name[36:],
			ServerName: name,
			Ext:        ext,
		})
	}

	return files
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	err := r.ParseMultipartForm(100 << 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	saveReceivedFiles(r)

	w.WriteHeader(http.StatusOK)

	renderAllFilesList(w)
}

func renderAllFilesList(w http.ResponseWriter) {
	allFiles := getAllFiles()

	for _, file := range allFiles {
		tmpl.ExecuteTemplate(w, "file-card", file)
	}

	tmpl.ExecuteTemplate(w, "file-count", len(allFiles))
}

func saveReceivedFiles(r *http.Request) []*multipart.FileHeader {
	files := r.MultipartForm.File["Files"]

	for _, fh := range files {
		src, err := fh.Open()
		if err != nil {
			continue
		}

		uuid, err := GenerateUUID()
		filename := sanitizeFilename(fh)
		filenameInServer := uuid + filename

		dst, err := os.Create(filepath.Join("media", filenameInServer))
		if err != nil {
			src.Close()
			continue
		}

		io.Copy(dst, src)

		dst.Close()
		src.Close()
	}
	return files
}

func deleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filename := r.URL.Query().Get("file")
	if filename == "" {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}

	path := filepath.Join("media", filepath.Base(filename))

	if err := os.Remove(path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)

	// isso atualiza a tela toda
	//w.Header().Set("HX-Refresh", "true")

	//fmt.Fprintf(w, `<span id="file-count" hx-swap-oob="true"> %d fotos</span>`, len(files))

	files := getAllFiles()
	tmpl.ExecuteTemplate(w, "file-count", len(files))
}
