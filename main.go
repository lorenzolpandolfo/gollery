package main

import (
	"embed"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Photo struct {
	Name string
	Ext string
}

type PageData struct {
	Photos []Photo
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
	entries, _ := os.ReadDir("media")

	var photos []Photo

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))

		photos = append(photos, Photo{
			Name: entry.Name(),
			Ext: ext,
		})
	}

	sort.Slice(photos, func(i, j int) bool {
		return photos[i].Name > photos[j].Name
	})

	tmpl.Execute(w, PageData{
		Photos: photos,
	})
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	err := r.ParseMultipartForm(100 << 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["photos"]

	for _, fh := range files {
		src, err := fh.Open()
		if err != nil {
			continue
		}

		dst, err := os.Create(filepath.Join("media", fh.Filename))
		if err != nil {
			src.Close()
			continue
		}

		io.Copy(dst, src)

		dst.Close()
		src.Close()
	}

	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)

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
}
