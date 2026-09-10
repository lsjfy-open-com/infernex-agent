// Package namedfiles separates readable file names from stable lookup IDs.
package namedfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Name preserves Chinese keywords and uses an explicit UTC timestamp. A bounded
// slug leaves room for extensions and collision suffixes on common filesystems.
func Name(subject string, created time.Time) string {
	var slug strings.Builder
	separator := false
	for _, r := range strings.ToLower(subject) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			separator = slug.Len() > 0
			continue
		}
		if slug.Len()+len(string(r))+1 > 96 {
			break
		}
		if separator {
			slug.WriteByte('-')
		}
		slug.WriteRune(r)
		separator = false
	}
	if slug.Len() == 0 {
		slug.WriteString("record")
	}
	return slug.String() + "_" + created.UTC().Format("2006-01-02_15-04-05Z")
}

func indexDirectory(root string) (string, error) {
	dir := filepath.Join(root, ".index")
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("index must be a real directory")
	}
	return dir, nil
}

func validPart(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\r\n\x00") && filepath.Base(value) == value
}

// Lookup performs a direct ID lookup. Index values are only basenames, never
// arbitrary paths. Callers can rebuild missing indexes from their source files.
func Lookup(root, id string) (string, error) {
	if !validPart(id) {
		return "", fmt.Errorf("invalid index id")
	}
	dir, err := indexDirectory(root)
	if err != nil {
		return "", err
	}
	index := filepath.Join(dir, id)
	info, err := os.Lstat(index)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 512 {
		return "", fmt.Errorf("invalid index file")
	}
	data, err := os.ReadFile(index)
	if err != nil {
		return "", err
	}
	name := strings.TrimSuffix(string(data), "\n")
	if !validPart(name) {
		return "", fmt.Errorf("invalid indexed filename")
	}
	target := filepath.Join(root, name)
	info, err = os.Lstat(target)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("indexed file must be regular")
	}
	return target, nil
}

// Index publishes a complete entry atomically without replacing an existing ID.
func Index(root, id, name string) error {
	if !validPart(id) || !validPart(name) {
		return fmt.Errorf("invalid index id or filename")
	}
	dir, err := indexDirectory(root)
	if err != nil {
		return err
	}
	temporary, err := stage(dir, []byte(name+"\n"))
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Link(temporary, filepath.Join(dir, id)); err != nil {
		if os.IsExist(err) {
			existing, lookupErr := Lookup(root, id)
			if lookupErr == nil && filepath.Base(existing) == name {
				return nil
			}
		}
		return err
	}
	return nil
}

// Create publishes complete contents under a readable name, adding a numeric
// suffix on collision. Neither a previous record nor its index is overwritten.
func Create(root, name, extension, id string, contents []byte) (string, error) {
	if !validPart(name) || (extension != ".json" && extension != ".md") {
		return "", fmt.Errorf("invalid record filename")
	}
	temporary, err := stage(root, contents)
	if err != nil {
		return "", err
	}
	defer os.Remove(temporary)
	for attempt := 1; ; attempt++ {
		candidate := name
		if attempt > 1 {
			candidate += fmt.Sprintf("-%d", attempt)
		}
		target := filepath.Join(root, candidate+extension)
		if err := os.Link(temporary, target); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", err
		}
		if err := Index(root, id, filepath.Base(target)); err != nil {
			_ = os.Remove(target)
			return "", err
		}
		return target, nil
	}
}

func stage(root string, contents []byte) (string, error) {
	file, err := os.CreateTemp(root, ".record-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if _, err = file.Write(contents); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// Replace atomically updates an existing record while keeping its name and ID.
func Replace(path string, contents []byte) error {
	temporary, err := stage(filepath.Dir(path), contents)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	return os.Rename(temporary, path)
}
