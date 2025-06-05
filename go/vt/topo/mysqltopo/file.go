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
	"fmt"
	"path"
	"strings"

	"vitess.io/vitess/go/vt/topo"
)

// Create is part of the topo.Conn interface.
func (s *Server) Create(ctx context.Context, filePath string, contents []byte) (topo.Version, error) {
	// Check if the file already exists
	var version int64
	err := s.db.QueryRowContext(ctx, "SELECT version FROM topo_files WHERE path = ?", filePath).Scan(&version)
	if err == nil {
		// File exists
		return nil, topo.NewError(topo.NodeExists, filePath)
	} else if err != sql.ErrNoRows {
		// Unexpected error
		return nil, convertError(err, filePath)
	}

	// Create parent directories if needed
	if err := s.createParentDirectories(ctx, filePath); err != nil {
		return nil, err
	}

	// Create the file
	result, err := s.db.ExecContext(ctx, "INSERT INTO topo_files (path, data) VALUES (?, ?)", filePath, contents)
	if err != nil {
		return nil, convertError(err, filePath)
	}

	// Get the version (auto-increment ID)
	id, err := result.LastInsertId()
	if err != nil {
		return nil, convertError(err, filePath)
	}

	// Notify watchers
	if err := s.notifyWatchers(ctx, filePath); err != nil {
		// Log error but don't fail the operation
		// The watch mechanism will eventually catch up
	}

	return MySQLVersion(id), nil
}

// Update is part of the topo.Conn interface.
func (s *Server) Update(ctx context.Context, filePath string, contents []byte, version topo.Version) (topo.Version, error) {
	var query string
	var args []interface{}

	if version != nil {
		// Conditional update
		mysqlVersion, ok := version.(MySQLVersion)
		if !ok {
			return nil, fmt.Errorf("bad version type %T, expected MySQLVersion", version)
		}
		query = "UPDATE topo_files SET data = ?, version = version + 1 WHERE path = ? AND version = ?"
		args = []interface{}{contents, filePath, int64(mysqlVersion)}
	} else {
		// Unconditional update or create
		query = "INSERT INTO topo_files (path, data) VALUES (?, ?) ON DUPLICATE KEY UPDATE data = VALUES(data), version = version + 1"
		args = []interface{}{filePath, contents}
	}

	// Create parent directories if needed
	if err := s.createParentDirectories(ctx, filePath); err != nil {
		return nil, err
	}

	// Execute the update
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, convertError(err, filePath)
	}

	// Check if the update was successful
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, convertError(err, filePath)
	}
	if rowsAffected == 0 && version != nil {
		// No rows were affected, which means the version didn't match
		return nil, topo.NewError(topo.BadVersion, filePath)
	}

	// Get the new version
	var newVersion int64
	err = s.db.QueryRowContext(ctx, "SELECT version FROM topo_files WHERE path = ?", filePath).Scan(&newVersion)
	if err != nil {
		return nil, convertError(err, filePath)
	}

	// Notify watchers
	if err := s.notifyWatchers(ctx, filePath); err != nil {
		// Log error but don't fail the operation
	}

	return MySQLVersion(newVersion), nil
}

// Get is part of the topo.Conn interface.
func (s *Server) Get(ctx context.Context, filePath string) ([]byte, topo.Version, error) {
	var data []byte
	var version int64

	err := s.db.QueryRowContext(ctx, "SELECT data, version FROM topo_files WHERE path = ?", filePath).Scan(&data, &version)
	if err == sql.ErrNoRows {
		return nil, nil, topo.NewError(topo.NoNode, filePath)
	} else if err != nil {
		return nil, nil, convertError(err, filePath)
	}

	return data, MySQLVersion(version), nil
}

// GetVersion is part of the topo.Conn interface.
func (s *Server) GetVersion(ctx context.Context, filePath string, version int64) ([]byte, error) {
	var data []byte

	// Query for the specific version
	err := s.db.QueryRowContext(ctx, "SELECT data FROM topo_files WHERE path = ? AND version = ?", filePath, version).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, topo.NewError(topo.NoNode, filePath)
	} else if err != nil {
		return nil, convertError(err, filePath)
	}

	return data, nil
}

// List is part of the topo.Conn interface.
func (s *Server) List(ctx context.Context, filePathPrefix string) ([]topo.KVInfo, error) {
	// Query for all files with the given prefix
	rows, err := s.db.QueryContext(ctx, "SELECT path, data, version FROM topo_files WHERE path LIKE ?", filePathPrefix+"%")
	if err != nil {
		return nil, convertError(err, filePathPrefix)
	}
	defer rows.Close()

	var results []topo.KVInfo
	for rows.Next() {
		var path string
		var data []byte
		var version int64

		if err := rows.Scan(&path, &data, &version); err != nil {
			return nil, convertError(err, filePathPrefix)
		}

		results = append(results, topo.KVInfo{
			Key:     []byte(path),
			Value:   data,
			Version: MySQLVersion(version),
		})
	}

	if err := rows.Err(); err != nil {
		return nil, convertError(err, filePathPrefix)
	}

	if len(results) == 0 {
		return nil, topo.NewError(topo.NoNode, filePathPrefix)
	}

	return results, nil
}

// Delete is part of the topo.Conn interface.
func (s *Server) Delete(ctx context.Context, filePath string, version topo.Version) error {
	var query string
	var args []interface{}

	if version != nil {
		// Conditional delete
		mysqlVersion, ok := version.(MySQLVersion)
		if !ok {
			return fmt.Errorf("bad version type %T, expected MySQLVersion", version)
		}
		query = "DELETE FROM topo_files WHERE path = ? AND version = ?"
		args = []interface{}{filePath, int64(mysqlVersion)}
	} else {
		// Unconditional delete
		query = "DELETE FROM topo_files WHERE path = ?"
		args = []interface{}{filePath}
	}

	// Execute the delete
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return convertError(err, filePath)
	}

	// Check if the delete was successful
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return convertError(err, filePath)
	}
	if rowsAffected == 0 {
		if version != nil {
			// No rows were affected, check if the file exists
			var exists bool
			err := s.db.QueryRowContext(ctx, "SELECT 1 FROM topo_files WHERE path = ?", filePath).Scan(&exists)
			if err == sql.ErrNoRows {
				return topo.NewError(topo.NoNode, filePath)
			} else if err != nil {
				return convertError(err, filePath)
			}
			return topo.NewError(topo.BadVersion, filePath)
		}
		return topo.NewError(topo.NoNode, filePath)
	}

	// Clean up empty parent directories
	if err := s.cleanupEmptyDirectories(ctx, filePath); err != nil {
		// Log error but don't fail the operation
	}

	// Notify watchers
	if err := s.notifyWatchers(ctx, filePath); err != nil {
		// Log error but don't fail the operation
	}

	return nil
}

// createParentDirectories creates all parent directories for a given file path.
func (s *Server) createParentDirectories(ctx context.Context, filePath string) error {
	// Get the directory path
	dirPath := path.Dir(filePath)
	if dirPath == "/" || dirPath == "." {
		return nil
	}

	// Split the path into components
	components := strings.Split(dirPath, "/")
	currentPath := ""

	// Create each directory component
	for _, component := range components {
		if component == "" {
			continue
		}

		if currentPath == "" {
			currentPath = component
		} else {
			currentPath = currentPath + "/" + component
		}

		// Insert the directory if it doesn't exist
		_, err := s.db.ExecContext(ctx, "INSERT IGNORE INTO topo_directories (path) VALUES (?)", currentPath)
		if err != nil {
			return convertError(err, currentPath)
		}
	}

	return nil
}

// cleanupEmptyDirectories removes empty directories after a file is deleted.
func (s *Server) cleanupEmptyDirectories(ctx context.Context, filePath string) error {
	// Get the directory path
	dirPath := path.Dir(filePath)
	if dirPath == "/" || dirPath == "." {
		return nil
	}
	// Check if the directory is empty
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT 1 FROM topo_files WHERE path LIKE ?
			UNION ALL
			SELECT 1 FROM topo_directories WHERE path LIKE ? AND path != ?
		) AS t
	`, dirPath+"/%", dirPath+"/%", dirPath).Scan(&count)

	if err != nil {
		return convertError(err, dirPath)
	}

	if count == 0 {
		// Directory is empty, delete it
		_, err := s.db.ExecContext(ctx, "DELETE FROM topo_directories WHERE path = ?", dirPath)
		if err != nil {
			return convertError(err, dirPath)
		}

		// Recursively cleanup parent directories
		return s.cleanupEmptyDirectories(ctx, dirPath)
	}

	return nil
}

// notifyWatchers notifies all watchers of a change to a file.
func (s *Server) notifyWatchers(ctx context.Context, filePath string) error {
	// This is a placeholder for the actual implementation
	// In a real implementation, we would use MySQL replication to notify watchers
	return nil
}

// convertError converts a MySQL error to a topo error.
func convertError(err error, path string) error {
	if err == sql.ErrNoRows {
		return topo.NewError(topo.NoNode, path)
	}
	return err
}
