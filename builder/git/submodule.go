package git

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	ini "github.com/vaughan0/go-ini"
)

func PrepSubmodules(
	gitDir, checkoutDir, mainRev string,
) error {

	gitModules := filepath.Join(checkoutDir, ".gitmodules")

	submodules, err := ParseSubmodules(gitModules)
	if err != nil {
		if os.IsNotExist(err) {
			// No .gitmodules available.
			return nil
		}
		return err
	}

	log.Printf("Prep %v submodules", len(submodules))

	GetSubmoduleRevs(gitDir, mainRev, submodules)

	errs := make(chan error, len(submodules))

	go func() {
		defer close(errs)

		var wg sync.WaitGroup
		defer wg.Wait()

		// Run only NumCPU in parallel
		semaphore := make(chan struct{}, runtime.NumCPU())

		for _, submodule := range submodules {

			wg.Add(1)
			go func(submodule Submodule) {
				defer wg.Done()
				defer func() { <-semaphore }()
				semaphore <- struct{}{}

				err := prepSubmodule(gitDir, checkoutDir, submodule)
				if err != nil {
					err = fmt.Errorf("processing %v: %v", submodule.Path, err)
				}
				errs <- err
			}(submodule)
		}
	}()

	// errs chan has buffer length len(submodules)
	err = MultipleErrors(errs)
	if err != nil {
		return err
	}
	return nil
}

type ErrMultiple struct {
	errs []error
}

func (em *ErrMultiple) Error() string {
	var s []string
	for _, e := range em.errs {
		s = append(s, e.Error())
	}
	return fmt.Sprint("multiple errors:\n", strings.Join(s, "\n"))
}

// Read errors out of a channel, counting only the non-nil ones.
// If there are zero non-nil errs, nil is returned.
func MultipleErrors(errs <-chan error) error {
	var em ErrMultiple
	for e := range errs {
		if e == nil {
			continue
		}
		em.errs = append(em.errs, e)
	}
	if len(em.errs) == 0 {
		return nil
	}
	return &em
}

// Checkout the working directory of a given submodule.
func prepSubmodule(
	mainGitDir, mainCheckoutDir string,
	submodule Submodule,
) error {

	subGitDir := filepath.Join(mainGitDir, "modules", submodule.Path)

	err := LocalMirror(submodule.URL, subGitDir, submodule.Rev, os.Stderr)
	if err != nil {
		return err
	}

	subCheckoutPath := filepath.Join(mainCheckoutDir, submodule.Path)

	// Note: checkout may recurse onto prepSubmodules.
	err = recursiveCheckout(subGitDir, subCheckoutPath, submodule.Rev)
	if err != nil {
		return err
	}
	return err
}

type Submodule struct {
	Path, URL string
	Rev       string // populated by GetSubmoduleRevs
}

func ParseSubmodules(filename string) (submodules []Submodule, err error) {
	config, err := ini.LoadFile(filename)
	if err != nil {
		return nil, err
	}

	for section := range config {
		if !strings.HasPrefix(section, "submodule") {
			continue
		}
		submodules = append(submodules, Submodule{
			Path: config.Section(section)["path"],
			URL:  config.Section(section)["url"],
		})
	}
	return submodules, nil
}

func GetSubmoduleRevs(gitDir, mainRev string, submodules []Submodule) error {
	for i := range submodules {
		rev, err := GetSubmoduleRev(gitDir, submodules[i].Path, mainRev)
		if err != nil {
			return err
		}
		submodules[i].Rev = rev
	}
	return nil
}

func GetSubmoduleRev(gitDir, submodulePath, mainRev string) (string, error) {
	cmd := Command(gitDir, "git", "ls-tree", mainRev, "--", submodulePath)
	cmd.Stdout = nil

	parts, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.Fields(string(parts))[2], nil
}
