//go:build !windows

package downloadtask

import "os"

func replaceFile(temp, target string) error {
	return os.Rename(temp, target)
}
