package git

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/vaughan0/go-ini"
)

func PrepSubmodules(gitDir, checkoutDir, mainRev string) error {

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

	for _, submodule := range submodules {
		err := prepSubmodule(gitDir, checkoutDir, submodule)
		if err != nil {
			return err
		}
	}

	return nil
}

// Checkout the working directory of a given submodule.
func prepSubmodule(
	mainGitDir, mainCheckoutDir string,
	submodule Submodule,
) error {

	log.Printf("prepSubmodule(%v, %v)", submodule.Path, submodule.URL)

	subGitDir := filepath.Join(mainGitDir, "modules", submodule.Path)

	err := gitLocalMirror(submodule.URL, subGitDir, submodule.Rev, os.Stderr)
	if err != nil {
		return err
	}

	subCheckoutPath := filepath.Join(mainCheckoutDir, submodule.Path)

	// Note: checkout may recurse onto prepSubmodules.
	err = checkout(subGitDir, subCheckoutPath, submodule.Rev)
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
