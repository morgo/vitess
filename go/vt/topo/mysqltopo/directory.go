/*
Copyright 2025 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mysqltopo

import (
	"context"
	"database/sql"
	"strings"

	"vitess.io/vitess/go/vt/topo"
)

// ListDir is part of the topo.Conn interface.
func (s *Server) ListDir(ctx context.Context, dirPath string, full bool) ([]topo.DirEntry, error) {
	// Normalize the path
	if dirPath == "" {
		dirPath = "/"
	}

	// Prepare the query to get all direct children
	dirPrefix := dirPath
	if dirPath != "/" {
		dirPrefix = dirPath + "/"
	}

	// Get files in this directory
	fileQuery := `
		SELECT SUBSTRING_INDEX(SUBSTRING(path, LENGTH(?) + 1), '/', 1) AS name,
		       COUNT(*) AS count
		FROM topo_files
		WHERE path LIKE ? AND path != ?
		GROUP BY name
	`
	fileRows, err := s.db.QueryContext(ctx, fileQuery, dirPrefix, dirPrefix+"%", dirPath)
	if err != nil {
		return nil, convertError(err, dirPath)
	}
	defer fileRows.Close()

	// Get subdirectories
	dirQuery := `
		SELECT SUBSTRING_INDEX(SUBSTRING(path, LENGTH(?) + 1), '/', 1) AS name,
		       COUNT(*) AS count
		FROM topo_directories
		WHERE path LIKE ? AND path != ?
		GROUP BY name
	`
	dirRows, err := s.db.QueryContext(ctx, dirQuery, dirPrefix, dirPrefix+"%", dirPath)
	if err != nil {
		return nil, convertError(err, dirPath)
	}
	defer dirRows.Close()

	// Combine results
	entries := make(map[string]topo.DirEntry)

	// Process file results
	for fileRows.Next() {
		var name string
		var count int
		if err := fileRows.Scan(&name, &count); err != nil {
			return nil, convertError(err, dirPath)
		}
		
		// Skip if empty or contains a slash (not a direct child)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		
		entries[name] = topo.DirEntry{
			Name:      name,
			Type:      topo.TypeFile,
			Ephemeral: false,
		}
	}
	if err := fileRows.Err(); err != nil {
		return nil, convertError(err, dirPath)
	}

	// Process directory results
	for dirRows.Next() {
		var name string
		var count int
		if err := dirRows.Scan(&name, &count); err != nil {
			return nil, convertError(err, dirPath)
		}
		
		// Skip if empty or contains a slash (not a direct child)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		
		entries[name] = topo.DirEntry{
			Name:      name,
			Type:      topo.TypeDirectory,
			Ephemeral: false,
		}
	}
	if err := dirRows.Err(); err != nil {
		return nil, convertError(err, dirPath)
	}

	// Check if the directory exists
	if len(entries) == 0 {
		// Check if the directory itself exists
		if dirPath != "/" {
			var exists bool
			err := s.db.QueryRowContext(ctx, "SELECT 1 FROM topo_directories WHERE path = ?", dirPath).Scan(&exists)
			if err == sql.ErrNoRows {
				return nil, topo.NewError(topo.NoNode, dirPath)
			} else if err != nil {
				return nil, convertError(err, dirPath)
			}
		}
	}

	// Convert map to slice
	result := make([]topo.DirEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}

	// Sort the results
	topo.DirEntriesSortByName(result)
	return result, nil
}