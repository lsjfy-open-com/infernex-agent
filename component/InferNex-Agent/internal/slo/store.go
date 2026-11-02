/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 * See the Mulan PSL v2 for more details.
 */

package slo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

type FileStore struct{ root string }

var runIDPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,128}$`)

func NewFileStore(root string) (*FileStore, error) {
	if root == "" {
		return nil, fmt.Errorf("SLO evidence directory is required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}
func (s *FileStore) Begin(request RunRequest) error {
	id := request.RunID
	if !runIDPattern.MatchString(id) {
		return fmt.Errorf("invalid SLO run ID")
	}
	dir := filepath.Join(s.root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "intent.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, encodeErr := json.Marshal(request)
	if encodeErr != nil {
		return encodeErr
	}
	_, err = f.Write(data)
	if err != nil {
		return err
	}
	return f.Sync()
}
func (s *FileStore) Complete(id string, samples []Sample, result Result) (string, error) {
	if !runIDPattern.MatchString(id) || len(samples) > 400 {
		return "", fmt.Errorf("invalid SLO evidence")
	}
	data, err := json.Marshal(struct {
		Samples []Sample `json:"samples"`
		Result  Result   `json:"result"`
	}{samples, result})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	path := filepath.Join(s.root, id, "evidence.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash[:]), nil
}
