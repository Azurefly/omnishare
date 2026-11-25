package api

import (
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const maxLocalSelection = 100

func localWorkbench(r *http.Request) bool {
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	origin := r.Header.Get("Origin")
	return net.ParseIP(clientIP(r)).IsLoopback() &&
		(strings.EqualFold(host, "localhost") || net.ParseIP(strings.Trim(host, "[]")).IsLoopback()) &&
		(origin == "" || origin == requestOrigin(r)) && r.Header.Get("X-OmniShare-Peer") == "" &&
		r.Header.Get("Sec-Fetch-Site") != "cross-site" &&
		r.Header.Get("Forwarded") == "" && r.Header.Get("X-Forwarded-For") == ""
}

var errLocalFile = errors.New("请输入当前设备上可访问的文件或文件夹的完整路径")
var errPrivateFile = errors.New("应用数据目录内的文件请通过文件列表使用")

func localPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if len(path) >= 2 && (path[0] == '"' || path[0] == '\'') && path[len(path)-1] == path[0] {
		path = path[1 : len(path)-1]
	}
	if strings.HasPrefix(strings.ToLower(path), "file:") {
		u, err := url.Parse(path)
		if err != nil || (u.Host != "" && !strings.EqualFold(u.Host, "localhost")) ||
			u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
			return "", errLocalFile
		}
		path = u.Path
		if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
			path = path[1:]
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errLocalFile
		}
		path = filepath.Join(home, path[2:])
	}
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) || (runtime.GOOS == "windows" && strings.HasPrefix(path, "\\\\")) {
		return "", errLocalFile
	}
	return filepath.Clean(path), nil
}

func (s *Server) externalPath(raw string) (string, os.FileInfo, error) {
	path, err := localPath(raw)
	if err != nil {
		return "", nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, errShortcutMissing
	}
	root, err := filepath.Abs(s.dataDir)
	if err != nil {
		return "", nil, errPrivateFile
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, errPrivateFile
	}
	if rel, err := filepath.Rel(root, path); err == nil && filepath.IsLocal(rel) {
		return "", nil, errPrivateFile
	}
	info, err := os.Stat(path)
	if err != nil || !(info.IsDir() || info.Mode().IsRegular()) {
		return "", nil, errLocalFile
	}
	return path, info, nil
}

// A native drop supplies paths. Resolve sizes and directory structure before
// queuing. Each eventual transfer rechecks the source before opening it.
type localSelectionFile struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	RelativeFolder string `json:"relative_folder"`
}

func (s *Server) selectLocalFiles(w http.ResponseWriter, r *http.Request) {
	if !localWorkbench(r) {
		respond(w, 403, 403, "请在源文件所在设备打开本机工作台", nil)
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 || len(req.Paths) > maxLocalSelection {
		respond(w, 400, 400, "每次可选择 1 至 100 个文件或文件夹", nil)
		return
	}
	files := []localSelectionFile{}
	visited := 0
	for _, raw := range req.Paths {
		path, info, err := s.externalPath(raw)
		if err == nil {
			err = filepath.WalkDir(path, func(child string, entry fs.DirEntry, walkErr error) error {
				visited++
				if visited > 10000 {
					return errors.New("所选目录包含的项目过多，请选择更小的子目录")
				}
				if walkErr != nil || r.Context().Err() != nil {
					return errLocalFile
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return nil
				}
				// Never descend into the workspace when its parent is dropped.
				_, stat, err := s.externalPath(child)
				if errors.Is(err, errPrivateFile) && entry.IsDir() {
					return filepath.SkipDir
				}
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				if len(files) >= maxLocalSelection {
					return errors.New("文件夹超过 100 个文件，请分批导入")
				}
				var folder string
				if info.IsDir() {
					rel, err := filepath.Rel(path, filepath.Dir(child))
					if err != nil {
						return errLocalFile
					}
					folder = filepath.ToSlash(filepath.Join(filepath.Base(path), rel))
				}
				files = append(files, localSelectionFile{stat.Name(), child, stat.Size(), folder})
				return nil
			})
		}
		if err != nil {
			respond(w, 400, 400, err.Error(), nil)
			return
		}
	}
	respond(w, 200, 0, "selected", files)
}

// Only the local workbench may substitute a disk file for a multipart file.
// The upload and restore handlers still apply their normal limits and checks.
func (s *Server) openLocalPart(r *http.Request, part *multipart.Part) (*os.File, error) {
	if !localWorkbench(r) {
		return nil, errors.New("请在源文件所在设备打开本机工作台")
	}
	raw, err := io.ReadAll(io.LimitReader(part, 32<<10))
	if err != nil || len(raw) == 32<<10 {
		return nil, errLocalFile
	}
	path, info, err := s.externalPath(string(raw))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("请选择常规文件")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errLocalFile
	}
	stat, err := file.Stat()
	if err != nil || !os.SameFile(info, stat) {
		file.Close()
		return nil, errLocalFile
	}
	return file, nil
}
