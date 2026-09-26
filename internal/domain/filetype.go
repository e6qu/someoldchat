package domain

import (
	"path/filepath"
	"strings"
)

// fileKind is what a file name's extension says about the bytes behind it:
// the media type a client should be told, and Slack's filetype and
// pretty_type for it.
type fileKind struct {
	mimeType   string
	fileType   string
	prettyType string
}

// fileKinds is deliberately a fixed table rather than mime.TypeByExtension,
// whose answer depends on the host's /etc/mime.types: the same upload must not
// be labelled differently by two replicas of this server.
var fileKinds = map[string]fileKind{
	"txt":  {"text/plain", "text", "Plain Text"},
	"text": {"text/plain", "text", "Plain Text"},
	"log":  {"text/plain", "text", "Plain Text"},
	"csv":  {"text/csv", "csv", "CSV"},
	"tsv":  {"text/tab-separated-values", "tsv", "TSV"},
	"md":   {"text/markdown", "markdown", "Markdown (raw)"},
	"html": {"text/html", "html", "HTML"},
	"htm":  {"text/html", "html", "HTML"},
	"css":  {"text/css", "css", "CSS"},
	"js":   {"text/javascript", "javascript", "JavaScript"},
	"json": {"application/json", "json", "JSON"},
	"xml":  {"application/xml", "xml", "XML"},
	"yaml": {"application/yaml", "yaml", "YAML"},
	"yml":  {"application/yaml", "yaml", "YAML"},
	"pdf":  {"application/pdf", "pdf", "PDF"},
	"zip":  {"application/zip", "zip", "Zip"},
	"gz":   {"application/gzip", "gzip", "GZip"},
	"tar":  {"application/x-tar", "tar", "Tar"},
	"png":  {"image/png", "png", "PNG"},
	"jpg":  {"image/jpeg", "jpg", "JPEG"},
	"jpeg": {"image/jpeg", "jpg", "JPEG"},
	"gif":  {"image/gif", "gif", "GIF"},
	"webp": {"image/webp", "webp", "WebP"},
	"bmp":  {"image/bmp", "bmp", "BMP"},
	"svg":  {"image/svg+xml", "svg", "SVG"},
	"ico":  {"image/vnd.microsoft.icon", "ico", "ICO"},
	"heic": {"image/heic", "heic", "HEIC"},
	"mp3":  {"audio/mpeg", "mp3", "MP3"},
	"wav":  {"audio/wav", "wav", "WAV"},
	"ogg":  {"audio/ogg", "ogg", "Ogg"},
	"m4a":  {"audio/mp4", "m4a", "M4A"},
	"mp4":  {"video/mp4", "mp4", "MPEG 4 Video"},
	"mov":  {"video/quicktime", "mov", "QuickTime Movie"},
	"webm": {"video/webm", "webm", "WebM"},
	"doc":  {"application/msword", "doc", "Word Document"},
	"docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx", "Word Document"},
	"xls":  {"application/vnd.ms-excel", "xls", "Excel Spreadsheet"},
	"xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx", "Excel Spreadsheet"},
	"ppt":  {"application/vnd.ms-powerpoint", "ppt", "PowerPoint Presentation"},
	"pptx": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", "pptx", "PowerPoint Presentation"},
}

// DefaultMIMEType is what a file of unknown kind is declared as.
const DefaultMIMEType = "application/octet-stream"

func fileExtension(name string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.TrimSpace(name))), ".")
}

// InferMIMEType names the media type of a file from its name, the way Slack
// does when an upload does not declare one. An unknown extension is
// application/octet-stream.
func InferMIMEType(name string) string {
	if kind, ok := fileKinds[fileExtension(name)]; ok {
		return kind.mimeType
	}
	return DefaultMIMEType
}

// FileTypes returns Slack's filetype and pretty_type for this file. A snippet's
// type is the syntax its author chose; a hosted file's is read from its name.
// A name with no extension is "binary".
func (f File) FileTypes() (string, string) {
	if f.IsSnippet() {
		fileType := strings.TrimSpace(f.FileType)
		if kind, ok := fileKinds[fileType]; ok && kind.fileType == fileType {
			return fileType, kind.prettyType
		}
		return fileType, strings.ToUpper(fileType)
	}
	extension := fileExtension(f.Name)
	if kind, ok := fileKinds[extension]; ok {
		return kind.fileType, kind.prettyType
	}
	if extension == "" {
		return "binary", "Binary"
	}
	return extension, strings.ToUpper(extension)
}
