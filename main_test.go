package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPersistentDataRoundTrip(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "data.json")
	want := persistentData{
		Starred: []starredEntry{{
			Kind: "file",
			ID:   "123e4567-e89b-12d3-a456-426614174000",
		}, {
			Kind: "folder",
			Path: "trips/2024",
		}},
		Trash: []trashEntry{{
			ID:           "123e4567-e89b-12d3-a456-426614174001",
			Kind:         "file",
			OriginalPath: "trips/photo.jpg",
			TrashPath:    ".trash/123e4567-e89b-12d3-a456-426614174001/photo.jpg",
			DeletedAt:    time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
		}},
	}

	if err := savePersistentData(filename, want); err != nil {
		t.Fatalf("savePersistentData() error = %v", err)
	}

	got, err := readPersistentData(filename)
	if err != nil {
		t.Fatalf("readPersistentData() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readPersistentData() = %#v, want %#v", got, want)
	}
}

func TestFilterRecentFiles(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	files := []File{
		{Name: "older.jpg", CreatedDate: now.Add(-recentWindow - time.Second)},
		{Name: "week-old.jpg", CreatedDate: now.Add(-recentWindow)},
		{Name: "newest.jpg", CreatedDate: now.Add(-time.Hour)},
		{Name: "within-week.jpg", CreatedDate: now.Add(-recentWindow + time.Second)},
		{Name: "future.jpg", CreatedDate: now.Add(time.Second)},
	}

	got := filterRecentFiles(files, now)
	if len(got) != 2 {
		t.Fatalf("filterRecentFiles() returned %d files, want 2", len(got))
	}
	if got[0].Name != "newest.jpg" || got[1].Name != "within-week.jpg" {
		t.Fatalf("filterRecentFiles() names = %q, %q", got[0].Name, got[1].Name)
	}
}

func TestPhotoFilesFiltersImagesAndSortsNewestFirst(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	if err := os.Mkdir(mediaDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	olderTime := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	newerTime := olderTime.Add(time.Hour)
	for name, modified := range map[string]time.Time{
		"123e4567-e89b-12d3-a456-426614174000__older.jpg": olderTime,
		"123e4567-e89b-12d3-a456-426614174001__newer.png": newerTime,
		"123e4567-e89b-12d3-a456-426614174002__notes.txt": newerTime.Add(time.Hour),
	} {
		path := filepath.Join(mediaDirectory, name)
		if err := os.WriteFile(path, []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}

	files, err := photoFiles()
	if err != nil {
		t.Fatalf("photoFiles() error = %v", err)
	}
	if len(files) != 2 || files[0].Name != "newer.png" || files[1].Name != "older.jpg" {
		t.Fatalf("photoFiles() = %#v, want newest image first and no non-image files", files)
	}
}

func TestStarHandlerPersistsFileStar(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}

	persistentState.Lock()
	previousStars := persistentState.starred
	previousTrash := persistentState.trash
	persistentState.starred = make(map[string]starredEntry)
	persistentState.trash = nil
	persistentState.Unlock()
	t.Cleanup(func() {
		if err := os.Chdir(workingDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
		persistentState.Lock()
		persistentState.starred = previousStars
		persistentState.trash = previousTrash
		persistentState.Unlock()
	})

	if err := os.Mkdir(mediaDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	path := "123e4567-e89b-12d3-a456-426614174000__photo.jpg"
	if err := os.WriteFile(fullPath(path), []byte("photo"), 0644); err != nil {
		t.Fatal(err)
	}

	sendStarRequest := func(starred bool) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(starRequest{Kind: "file", Path: path, Starred: starred})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/star", bytes.NewReader(payload))
		response := httptest.NewRecorder()
		starHandler(response, request)
		return response
	}

	response := sendStarRequest(true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"starred":true`) {
		t.Fatalf("star response = %d %q", response.Code, response.Body.String())
	}
	items, err := readPersistentData(dataFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Starred) != 1 || items.Starred[0].ID != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("starred file was not persisted: %#v", items.Starred)
	}

	response = sendStarRequest(false)
	if response.Code != http.StatusOK {
		t.Fatalf("unstar response = %d %q", response.Code, response.Body.String())
	}
	items, err = readPersistentData(dataFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Starred) != 0 {
		t.Fatalf("unstarred file remains persisted: %#v", items.Starred)
	}
}

func useTemporaryMedia(t *testing.T) {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}

	persistentState.Lock()
	previousStars := persistentState.starred
	previousTrash := append([]trashEntry(nil), persistentState.trash...)
	persistentState.starred = make(map[string]starredEntry)
	persistentState.trash = nil
	persistentState.Unlock()

	t.Cleanup(func() {
		if err := os.Chdir(workingDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
		persistentState.Lock()
		persistentState.starred = previousStars
		persistentState.trash = previousTrash
		persistentState.Unlock()
	})
	if err := os.Mkdir(mediaDirectory, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestRenameFilePreservesID(t *testing.T) {
	useTemporaryMedia(t)
	id := "123e4567-e89b-12d3-a456-426614174000"
	oldPath := id + "__before.jpg"
	if err := os.WriteFile(fullPath(oldPath), []byte("photo"), 0644); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(renameRequest{Kind: "file", Path: oldPath, Name: "after.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/rename", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	renameHandler(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("rename response = %d %q", response.Code, response.Body.String())
	}

	file, err := findFileByReference(id)
	if err != nil {
		t.Fatal(err)
	}
	if file.Id != id || file.Name != "after.jpg" {
		t.Fatalf("renamed file = %#v", file)
	}
	if _, err := os.Stat(fullPath(oldPath)); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed: %v", err)
	}
}

func TestFolderRenameRebasesTrashAndStarredPaths(t *testing.T) {
	useTemporaryMedia(t)
	if err := os.Mkdir(fullPath("old"), 0755); err != nil {
		t.Fatal(err)
	}
	id := "123e4567-e89b-12d3-a456-426614174000"
	filePath := "old/" + id + "__photo.jpg"
	if err := os.WriteFile(fullPath(filePath), []byte("photo"), 0644); err != nil {
		t.Fatal(err)
	}

	persistentState.Lock()
	persistentState.starred["folder:old"] = starredEntry{Kind: "folder", Path: "old"}
	persistentState.Unlock()
	if err := moveToTrash("file", filePath); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(renameRequest{Kind: "folder", Path: "old", Name: "new"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/rename", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	renameHandler(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("folder rename response = %d %q", response.Code, response.Body.String())
	}

	items, err := readPersistentData(dataFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Starred) != 1 || items.Starred[0].Path != "new" {
		t.Fatalf("starred folder path was not rebased: %#v", items.Starred)
	}
	if len(items.Trash) != 1 || items.Trash[0].OriginalPath != "new/"+id+"__photo.jpg" {
		t.Fatalf("trash restore path was not rebased: %#v", items.Trash)
	}
	if err := restoreTrashItem(items.Trash[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fullPath("new/" + id + "__photo.jpg")); err != nil {
		t.Fatalf("restored file is missing: %v", err)
	}
}

func TestRestoreTrashItemDoesNotOverwriteExistingPath(t *testing.T) {
	useTemporaryMedia(t)
	id := "123e4567-e89b-12d3-a456-426614174000"
	filePath := id + "__photo.jpg"
	if err := os.WriteFile(fullPath(filePath), []byte("trashed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := moveToTrash("file", filePath); err != nil {
		t.Fatal(err)
	}
	persistentState.RLock()
	trashID := persistentState.trash[0].ID
	trashPath := persistentState.trash[0].TrashPath
	persistentState.RUnlock()
	if err := os.WriteFile(fullPath(filePath), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := restoreTrashItem(trashID); !errors.Is(err, os.ErrExist) {
		t.Fatalf("restoreTrashItem() error = %v, want os.ErrExist", err)
	}
	content, err := os.ReadFile(fullPath(filePath))
	if err != nil || string(content) != "existing" {
		t.Fatalf("existing item was overwritten: content %q, error %v", content, err)
	}
	if _, err := os.Stat(fullPath(trashPath)); err != nil {
		t.Fatalf("trash copy was lost: %v", err)
	}
}

func TestTrashFolderRestorePreservesContents(t *testing.T) {
	useTemporaryMedia(t)
	folder := fullPath("album")
	if err := os.Mkdir(folder, 0755); err != nil {
		t.Fatal(err)
	}
	filePath := "album/123e4567-e89b-12d3-a456-426614174000__photo.jpg"
	if err := os.WriteFile(fullPath(filePath), []byte("photo"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := moveToTrash("folder", "album"); err != nil {
		t.Fatal(err)
	}
	folders, files, err := trashedItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Name != "album" || len(files) != 0 {
		t.Fatalf("trashed folder listing = %#v, %#v", folders, files)
	}
	if err := restoreTrashItem(folders[0].TrashID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fullPath(filePath)); err != nil {
		t.Fatalf("folder contents were not restored: %v", err)
	}
}

func TestTrashRestorePermanentDeleteAndExpiry(t *testing.T) {
	useTemporaryMedia(t)
	id := "123e4567-e89b-12d3-a456-426614174000"
	filePath := id + "__photo.jpg"
	if err := os.WriteFile(fullPath(filePath), []byte("photo"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := moveToTrash("file", filePath); err != nil {
		t.Fatal(err)
	}
	persistentState.RLock()
	trashID := persistentState.trash[0].ID
	persistentState.RUnlock()
	trashedFolders, trashedFiles, err := trashedItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(trashedFolders) != 0 || len(trashedFiles) != 1 || trashedFiles[0].Name != "photo.jpg" || !trashedFiles[0].Trashed {
		t.Fatalf("trashedItems() = %#v, %#v", trashedFolders, trashedFiles)
	}
	if err := restoreTrashItem(trashID); err != nil {
		t.Fatalf("restoreTrashItem() error = %v", err)
	}
	if _, err := os.Stat(fullPath(filePath)); err != nil {
		t.Fatalf("restored file is missing: %v", err)
	}

	if err := moveToTrash("file", filePath); err != nil {
		t.Fatal(err)
	}
	persistentState.RLock()
	trashID = persistentState.trash[0].ID
	trashPath := persistentState.trash[0].TrashPath
	persistentState.RUnlock()
	if err := permanentlyDeleteTrashItem(trashID); err != nil {
		t.Fatalf("permanentlyDeleteTrashItem() error = %v", err)
	}
	if _, err := os.Stat(fullPath(trashPath)); !os.IsNotExist(err) {
		t.Fatalf("permanently deleted file still exists or stat failed: %v", err)
	}

	expiredID := "123e4567-e89b-12d3-a456-426614174001"
	expiredPath := trashDirectory + "/" + expiredID + "/" + id + "__expired.jpg"
	if err := os.MkdirAll(filepath.Dir(fullPath(expiredPath)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath(expiredPath), []byte("expired"), 0644); err != nil {
		t.Fatal(err)
	}
	expired := trashEntry{
		ID:           expiredID,
		Kind:         "file",
		OriginalPath: "expired.jpg",
		TrashPath:    expiredPath,
		DeletedAt:    time.Now().Add(-trashRetention - time.Second),
	}
	persistentState.Lock()
	persistentState.trash = []trashEntry{expired}
	persistentState.Unlock()
	if err := savePersistentData(dataFilePath, persistentData{Trash: []trashEntry{expired}}); err != nil {
		t.Fatal(err)
	}

	cleanupExpiredTrash()
	if _, err := os.Stat(fullPath(expiredPath)); !os.IsNotExist(err) {
		t.Fatalf("expired file still exists or stat failed: %v", err)
	}
	persistentState.RLock()
	remaining := len(persistentState.trash)
	persistentState.RUnlock()
	if remaining != 0 {
		t.Fatalf("expired trash metadata remains: %d entries", remaining)
	}
}

func TestBulkMoveHandlerMovesSelectedFiles(t *testing.T) {
	useTemporaryMedia(t)
	if err := os.Mkdir(fullPath("destination"), 0755); err != nil {
		t.Fatal(err)
	}
	items := []BulkItem{
		{Kind: "file", Path: "123e4567-e89b-12d3-a456-426614174000__one.jpg"},
		{Kind: "file", Path: "123e4567-e89b-12d3-a456-426614174001__two.jpg"},
	}
	for _, item := range items {
		if err := os.WriteFile(fullPath(item.Path), []byte(item.Path), 0644); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := json.Marshal(BulkMoveRequest{Items: items, TargetPath: "destination"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/bulk-move", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	bulkMoveHandler(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("bulk move response = %d %q", response.Code, response.Body.String())
	}

	for _, item := range items {
		movedPath := filepath.Join("destination", filepath.Base(item.Path))
		if _, err := os.Stat(fullPath(movedPath)); err != nil {
			t.Errorf("moved file %q is missing: %v", movedPath, err)
		}
		if _, err := os.Stat(fullPath(item.Path)); !os.IsNotExist(err) {
			t.Errorf("source file %q still exists or stat failed: %v", item.Path, err)
		}
	}
}

func TestPageTemplateRendersRecentViewAndHomeBreadcrumb(t *testing.T) {
	var output bytes.Buffer
	if err := tmpl.Execute(&output, FileData{
		View:  "recent",
		Count: ViewCount{},
		Stats: StorageStats{},
	}); err != nil {
		t.Fatalf("render recent page: %v", err)
	}
	if !strings.Contains(output.String(), "Nenhum arquivo recente enviado na última semana.") {
		t.Fatal("recent empty-state message was not rendered")
	}
	if !strings.Contains(output.String(), `data-drop-name="Início"`) ||
		!strings.Contains(output.String(), ">Início</span>") {
		t.Fatal("home breadcrumb label is missing")
	}
}

func TestStorageCardOnlyRendersInStorageSection(t *testing.T) {
	for _, view := range []string{"storage", "recent"} {
		t.Run(view, func(t *testing.T) {
			var output bytes.Buffer
			if err := tmpl.Execute(&output, FileData{View: view, Stats: StorageStats{}}); err != nil {
				t.Fatalf("render %s page: %v", view, err)
			}
			containsCard := strings.Contains(output.String(), `id="storage-stats"`)
			if containsCard != (view == "storage") {
				t.Fatalf("storage card rendered for view %q = %t", view, containsCard)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/?view=storage", nil)
	if parsed, ok := viewRequestFrom(request); !ok || parsed.View != "storage" {
		t.Fatalf("viewRequestFrom(storage) = %#v, %t", parsed, ok)
	}
}

func TestPhotoCardTemplateRendersViewerData(t *testing.T) {
	var output bytes.Buffer
	if err := tmpl.ExecuteTemplate(&output, "photo-card", File{
		Name: "photo.jpg",
		Path: "album/photo.jpg",
		Kind: "image",
	}); err != nil {
		t.Fatalf("render photo card: %v", err)
	}
	for _, expected := range []string{
		`class="select-item"`,
		`data-kind="file"`,
		`class="photo-open"`,
		`data-path="album/photo.jpg"`,
		`data-name="photo.jpg"`,
		`src="/media/album/photo.jpg"`,
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("photo card does not include %q", expected)
		}
	}
}

func TestLocationLabelOmitsInicio(t *testing.T) {
	if got := locationLabel(""); got != "Arquivos" {
		t.Fatalf("locationLabel(root) = %q, want Arquivos", got)
	}
	if got := locationLabel("photos/2026"); got != "photos / 2026" {
		t.Fatalf("locationLabel(path) = %q", got)
	}
}

func TestMobileItemMenusRenderActions(t *testing.T) {
	for name, data := range map[string]any{
		"file":   File{Kind: "image", Name: "photo.jpg", Path: "photo.jpg"},
		"folder": Folder{Name: "photos", Path: "photos"},
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			if err := tmpl.ExecuteTemplate(&output, name+"-card", data); err != nil {
				t.Fatalf("render %s card: %v", name, err)
			}
			for _, expected := range []string{
				"mobile-menu-toggle",
				"mobile-action-menu",
				"Adicionar estrela",
				"Renomear",
				">Excluir</span>",
			} {
				if !strings.Contains(output.String(), expected) {
					t.Errorf("%s card does not include %q", name, expected)
				}
			}
			downloadAction := "Baixar"
			if name == "folder" {
				downloadAction = "mobile-folder-download"
			}
			if !strings.Contains(output.String(), downloadAction) {
				t.Errorf("%s card does not include its download action", name)
			}
		})
	}
}

func TestTrashCardTemplatesRenderRestoreActions(t *testing.T) {
	for name, data := range map[string]any{
		"file":   File{Trashed: true, TrashID: "123e4567-e89b-12d3-a456-426614174000", Name: "photo.jpg"},
		"folder": Folder{Trashed: true, TrashID: "123e4567-e89b-12d3-a456-426614174001", Name: "photos"},
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			templateName := name + "-card"
			if err := tmpl.ExecuteTemplate(&output, templateName, data); err != nil {
				t.Fatalf("render %s trash card: %v", name, err)
			}
			if !strings.Contains(output.String(), "/trash/restore?id=") ||
				!strings.Contains(output.String(), "/trash?id=") {
				t.Fatalf("%s trash card is missing restore or permanent-delete action: %s", name, output.String())
			}
		})
	}
}
