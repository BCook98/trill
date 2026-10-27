package sidecar

import (
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var errFolderUnavailable = errors.New("folder unavailable; choose an existing folder on your computer")

type macFolder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type macFolderListing struct {
	Path       string      `json:"path"`
	ParentPath string      `json:"parentPath,omitempty"`
	Folders    []macFolder `json:"folders"`
	Truncated  bool        `json:"truncated"`
}

func validMacFolderPath(directory string) bool {
	return filepath.IsAbs(directory) && filepath.Clean(directory) == directory && validText(directory, 4096)
}

// Production browsing exposes directory names, never file contents. Synthetic
// demos and owned integration labs stay confined to their existing private roots.
func (service *Service) folderLocations() (rootDirectory, initialDirectory string, err error) {
	if service.demo != nil {
		rootDirectory = filepath.Join(service.demo.rootPath, "fixtures")
	} else if service.config.HerdrLabRoot != "" {
		rootDirectory = service.config.HerdrLabRoot
	}
	if rootDirectory != "" {
		rootDirectory, err = filepath.EvalSymlinks(rootDirectory)
		return rootDirectory, rootDirectory, err
	}
	initialDirectory, err = os.UserHomeDir()
	return string(filepath.Separator), initialDirectory, err
}

func (service *Service) openMacFolder(directory string) (*os.Root, string, string, error) {
	rootDirectory, initialDirectory, err := service.folderLocations()
	if err != nil {
		return nil, "", "", errFolderUnavailable
	}
	if directory == "" {
		directory = initialDirectory
	}
	if !validMacFolderPath(directory) {
		return nil, "", "", errFolderUnavailable
	}
	relativeDirectory, err := filepath.Rel(rootDirectory, directory)
	if err != nil || !filepath.IsLocal(relativeDirectory) {
		return nil, "", "", errFolderUnavailable
	}
	root, err := os.OpenRoot(rootDirectory)
	if err != nil {
		return nil, "", "", errFolderUnavailable
	}
	file, err := openExplorerFile(root, relativeDirectory, true)
	if err != nil {
		root.Close()
		return nil, "", "", errFolderUnavailable
	}
	file.Close()
	return root, relativeDirectory, directory, nil
}

func (service *Service) validateTerminalDirectory(directory string) error {
	if directory == "" {
		return nil
	}
	root, _, _, err := service.openMacFolder(directory)
	if err != nil {
		return err
	}
	return root.Close()
}

func (handler *HTTP) folders(writer http.ResponseWriter, request *http.Request) {
	if !handler.control(writer) {
		return
	}
	if err := handler.filesCheckOpen(); err != nil {
		handler.fail(writer, err)
		return
	}
	directoryPath, err := parseFolderQuery(request)
	if err != nil {
		writeError(writer, 400, "invalid folder path")
		return
	}
	root, relativeDirectory, directory, err := handler.service.openMacFolder(directoryPath)
	if err != nil {
		handler.fail(writer, err)
		return
	}
	defer root.Close()
	listing, err := readFileDirectory(request.Context(), root, relativeDirectory)
	if err != nil {
		if request.Context().Err() != nil {
			handler.fail(writer, request.Context().Err())
		} else {
			handler.fail(writer, errFolderUnavailable)
		}
		return
	}
	result := macFolderListing{Path: directory, Folders: []macFolder{}, Truncated: listing.Truncated}
	rootDirectory, _, err := handler.service.folderLocations()
	if err != nil {
		handler.fail(writer, errFolderUnavailable)
		return
	}
	if directory != rootDirectory {
		result.ParentPath = filepath.Dir(directory)
	}
	for _, entry := range listing.Entries {
		if entry.Kind == "directory" && !strings.HasPrefix(entry.Name, ".") {
			result.Folders = append(result.Folders, macFolder{Name: entry.Name, Path: filepath.Join(rootDirectory, filepath.FromSlash(entry.Path))})
		}
	}
	if request.Context().Err() != nil {
		handler.fail(writer, request.Context().Err())
		return
	}
	writeJSON(writer, 200, result)
}

func parseFolderQuery(request *http.Request) (string, error) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return "", err
	}
	for key, values := range query {
		if key != "path" || len(values) != 1 {
			return "", fs.ErrInvalid
		}
	}
	directory := query.Get("path")
	if directory != "" && !validMacFolderPath(directory) {
		return "", fs.ErrInvalid
	}
	return directory, nil
}
