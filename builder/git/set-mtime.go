package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func SetMTimes(gitDir, checkoutDir, ref string) error {

	commitTimes, err := CommitTimes(gitDir, ref)
	if err != nil {
		return err
	}

	lsFiles := Command(gitDir, "git", "ls-tree", "-r", "--name-only", "-z", ref)
	lsFiles.Stdout = nil
	out, err := lsFiles.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %v", err)
	}

	dirMTimes := map[string]time.Time{}

	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	for _, file := range files {
		mTime, ok := commitTimes[file]
		if !ok {
			return fmt.Errorf("failed to find file in history: %q", file)
		}

		// Loop over each directory in the path to `file`, updating `dirMTimes`
		// to take the most recent time seen.
		dir := filepath.Dir(file)
		for {
			if other, ok := dirMTimes[dir]; ok {
				if mTime.After(other) {
					// file mTime is more recent than previous seen for 'dir'
					dirMTimes[dir] = mTime
				}
			} else {
				// first occurrence of dir
				dirMTimes[dir] = mTime
			}

			// Remove one directory from the path until it isn't changed anymore
			if dir == filepath.Dir(dir) {
				break
			}
			dir = filepath.Dir(dir)
		}

		err = os.Chtimes(filepath.Join(checkoutDir, file), mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}
	}

	for dir, mTime := range dirMTimes {
		err = os.Chtimes(filepath.Join(checkoutDir, dir), mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}
	}
	return nil
}
