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
	"time"

	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/topo"
)

// lockDescriptor implements topo.LockDescriptor.
type lockDescriptor struct {
	s       *Server
	dirPath string
	lockID  string
}

// Lock is part of the topo.Conn interface.
func (s *Server) Lock(ctx context.Context, dirPath, contents string) (topo.LockDescriptor, error) {
	return s.LockWithTTL(ctx, dirPath, contents, *mysqlTopoLockTTL)
}

// LockWithTTL is part of the topo.Conn interface.
func (s *Server) LockWithTTL(ctx context.Context, dirPath, contents string, ttl time.Duration) (topo.LockDescriptor, error) {
	// Check if the directory exists
	if dirPath != "/" {
		var exists bool
		err := s.db.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM (
					SELECT 1 FROM topo_files WHERE path LIKE ?
					UNION
					SELECT 1 FROM topo_directories WHERE path = ?
				) as subquery
			)
		`, dirPath+"/%", dirPath).Scan(&exists)
		
		if err != nil {
			return nil, convertError(err, dirPath)
		}
		
		if !exists {
			return nil, topo.NewError(topo.NoNode, dirPath)
		}
	}

	// Create a unique lock ID
	lockID := fmt.Sprintf("%v", time.Now().UnixNano())
	
	// Calculate expiration time
	expiration := time.Now().Add(ttl)
	
	// Try to acquire the lock
	for {
		select {
		case <-ctx.Done():
			return nil, convertError(ctx.Err(), dirPath)
		default:
		}

		// Try to insert the lock
		result, err := s.db.ExecContext(ctx, `
			INSERT INTO topo_locks (path, owner, contents, expiration)
			SELECT ?, ?, ?, ?
			FROM dual
			WHERE NOT EXISTS (
				SELECT 1 FROM topo_locks 
				WHERE path = ? AND expiration > NOW()
			)
		`, dirPath, lockID, contents, expiration, dirPath)
		
		if err != nil {
			// Check if it's a duplicate key error
			return nil, convertError(err, dirPath)
		}
		
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return nil, convertError(err, dirPath)
		}
		
		if rowsAffected > 0 {
			// Lock acquired
			return &lockDescriptor{
				s:       s,
				dirPath: dirPath,
				lockID:  lockID,
			}, nil
		}
		
		// Lock not acquired, check if we should wait or fail
		var currentOwner string
		var currentExpiration time.Time
		err = s.db.QueryRowContext(ctx, "SELECT owner, expiration FROM topo_locks WHERE path = ?", dirPath).Scan(&currentOwner, &currentExpiration)
		if err == sql.ErrNoRows {
			// Lock was released, try again
			continue
		} else if err != nil {
			return nil, convertError(err, dirPath)
		}
		
		// Check if the lock has expired
		if currentExpiration.Before(time.Now()) {
			// Lock has expired, delete it and try again
			_, err := s.db.ExecContext(ctx, "DELETE FROM topo_locks WHERE path = ? AND owner = ?", dirPath, currentOwner)
			if err != nil {
				log.Warningf("Failed to delete expired lock: %v", err)
			}
			continue
		}
		
		// Wait a bit before trying again
		select {
		case <-ctx.Done():
			return nil, convertError(ctx.Err(), dirPath)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// LockName is part of the topo.Conn interface.
func (s *Server) LockName(ctx context.Context, dirPath, contents string) (topo.LockDescriptor, error) {
	// Named locks don't require the path to exist
	// Use a 24-hour TTL as specified in the interface
	return s.LockWithTTL(ctx, dirPath, contents, 24*time.Hour)
}

// TryLock is part of the topo.Conn interface.
func (s *Server) TryLock(ctx context.Context, dirPath, contents string) (topo.LockDescriptor, error) {
	// Check if the directory exists
	if dirPath != "/" {
		var exists bool
		err := s.db.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM (
					SELECT 1 FROM topo_files WHERE path LIKE ?
					UNION
					SELECT 1 FROM topo_directories WHERE path = ?
				) as subquery
			)
		`, dirPath+"/%", dirPath).Scan(&exists)
		
		if err != nil {
			return nil, convertError(err, dirPath)
		}
		
		if !exists {
			return nil, topo.NewError(topo.NoNode, dirPath)
		}
	}

	// Check if the lock is already held
	var currentOwner string
	var currentExpiration time.Time
	err := s.db.QueryRowContext(ctx, "SELECT owner, expiration FROM topo_locks WHERE path = ?", dirPath).Scan(&currentOwner, &currentExpiration)
	if err != sql.ErrNoRows {
		if err != nil {
			return nil, convertError(err, dirPath)
		}
		
		// Check if the lock has expired
		if currentExpiration.After(time.Now()) {
			// Lock is still valid
			return nil, fmt.Errorf("lock already exists")
		}
		
		// Lock has expired, delete it
		_, err := s.db.ExecContext(ctx, "DELETE FROM topo_locks WHERE path = ? AND owner = ?", dirPath, currentOwner)
		if err != nil {
			log.Warningf("Failed to delete expired lock: %v", err)
		}
	}

	// Try to acquire the lock
	lockID := fmt.Sprintf("%v", time.Now().UnixNano())
	expiration := time.Now().Add(*mysqlTopoLockTTL)
	
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO topo_locks (path, owner, contents, expiration)
		VALUES (?, ?, ?, ?)
	`, dirPath, lockID, contents, expiration)
	
	if err != nil {
		return nil, convertError(err, dirPath)
	}
	
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, convertError(err, dirPath)
	}
	
	if rowsAffected > 0 {
		// Lock acquired
		return &lockDescriptor{
			s:       s,
			dirPath: dirPath,
			lockID:  lockID,
		}, nil
	}
	
	return nil, fmt.Errorf("failed to acquire lock")
}

// Check is part of the topo.LockDescriptor interface.
func (ld *lockDescriptor) Check(ctx context.Context) error {
	var owner string
	var expiration time.Time
	err := ld.s.db.QueryRowContext(ctx, "SELECT owner, expiration FROM topo_locks WHERE path = ?", ld.dirPath).Scan(&owner, &expiration)
	if err == sql.ErrNoRows {
		return fmt.Errorf("lock lost")
	} else if err != nil {
		return err
	}
	
	if owner != ld.lockID {
		return fmt.Errorf("lock was lost")
	}
	
	if expiration.Before(time.Now()) {
		return fmt.Errorf("lock expired")
	}
	
	// Refresh the lock
	_, err = ld.s.db.ExecContext(ctx, "UPDATE topo_locks SET expiration = ? WHERE path = ? AND owner = ?", 
		time.Now().Add(*mysqlTopoLockTTL), ld.dirPath, ld.lockID)
	if err != nil {
		return err
	}
	
	return nil
}

// Unlock is part of the topo.LockDescriptor interface.
func (ld *lockDescriptor) Unlock(ctx context.Context) error {
	_, err := ld.s.db.ExecContext(ctx, "DELETE FROM topo_locks WHERE path = ? AND owner = ?", ld.dirPath, ld.lockID)
	return err
}