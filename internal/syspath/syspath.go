// Package syspath finds system tools (systemctl, …) in the fixed root search
// path Dirs. The PATH of the caller's environment is never consulted: the
// agent runs as root, and a directory planted in its PATH must not decide
// which binary it starts. The directories are searched in order, so the tool
// is found both on merged-/usr systems and on images without usrmerge, where
// systemctl lives only in /bin (Ubuntu 20.04 on Jetson).
package syspath

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Dirs is the search path for system tools and the PATH given to the tools
// the agent starts.
const Dirs = "/usr/sbin:/usr/bin:/sbin:/bin"

var dirs = filepath.SplitList(Dirs)

// Find returns the absolute path of the executable name in Dirs; an error
// wrapping exec.ErrNotFound when there is none. name is a bare file name.
func Find(name string) (string, error) { return find(name, dirs) }

func find(name string, dirs []string) (string, error) {
	if name == "" || strings.ContainsRune(name, '/') || name == "." || name == ".." {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		return p, nil
	}
	return "", &exec.Error{Name: name, Err: fmt.Errorf("%w in %s", exec.ErrNotFound, Dirs)}
}
