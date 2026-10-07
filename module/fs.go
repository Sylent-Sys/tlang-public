package module

import "os"

// FileSystem is the file access Build performs.
type FileSystem interface {
	ReadFile(absPath string) ([]byte, error)
	IsFile(absPath string) bool
}

// BuildOptions configures BuildWith. The zero value reproduces Build exactly.
type BuildOptions struct {
	// FS overrides file access; nil means the operating system.
	FS FileSystem
	// RootDir bounds specifier resolution and is the base of module IDs;
	// "" means the directory of rootPath (Build's behavior). When set it must
	// be a directory containing rootPath (a root outside it is an E-IMPORT);
	// it is not checked to be a directory.
	RootDir string
}

// osFS is the default FileSystem: the operating system's files.
type osFS struct{}

// ReadFile implements FileSystem.
func (osFS) ReadFile(absPath string) ([]byte, error) { return os.ReadFile(absPath) }

// IsFile implements FileSystem: absPath exists and is not a directory.
func (osFS) IsFile(absPath string) bool {
	fi, err := os.Stat(absPath)
	return err == nil && !fi.IsDir()
}
