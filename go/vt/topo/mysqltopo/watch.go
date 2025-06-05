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
	"time"

	"github.com/google/uuid"
	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/topo"
)

// Watch is part of the topo.Conn interface.
func (s *Server) Watch(ctx context.Context, filePath string) (current *topo.WatchData, changes <-chan *topo.WatchData, err error) {
	// First get the current value
	data, version, err := s.Get(ctx, filePath)
	if err != nil {
		return nil, nil, err
	}
	
	// Create a channel for changes
	watchChan := make(chan *topo.WatchData, 10)
	
	// Create a unique watcher ID
	watcherID := uuid.New().String()
	
	// Register the watcher
	_, err = s.db.ExecContext(ctx, "INSERT INTO topo_watch (path, watcher_id) VALUES (?, ?)", filePath, watcherID)
	if err != nil {
		return nil, nil, convertError(err, filePath)
	}

	// Create a channel to receive replication events
	replicationChan := make(chan interface{}, 10)
	
	// Register with the replication watcher if available
	if s.replicationWatcher != nil && s.replicationWatcher.running {
		s.replicationWatcher.RegisterWatch(filePath, replicationChan)
	}
	
	// Start a goroutine to process changes
	go func() {
		defer func() {
			// Unregister the watcher when done
			_, err := s.db.Exec("DELETE FROM topo_watch WHERE path = ? AND watcher_id = ?", filePath, watcherID)
			if err != nil {
				log.Errorf("Failed to unregister watcher: %v", err)
			}
			
			// Unregister from the replication watcher
			if s.replicationWatcher != nil && s.replicationWatcher.running {
				s.replicationWatcher.UnregisterWatch(filePath, replicationChan)
			}
			
			close(watchChan)
		}()
		
		// If replication watcher is not available, fall back to polling
		var ticker *time.Ticker
		if s.replicationWatcher == nil || !s.replicationWatcher.running {
			ticker = time.NewTicker(1 * time.Second)
			defer ticker.Stop()
		}
		
		currentVersion := version
		
		for {
			select {
			case <-ctx.Done():
				// Context canceled, send error and exit
				watchChan <- &topo.WatchData{
					Err: topo.NewError(topo.Interrupted, "watch canceled"),
				}
				return
				
			case event := <-replicationChan:
				// Process replication event
				if watchData, ok := event.(*topo.WatchData); ok {
					// Send the watch data
					watchChan <- watchData
					// Update the current version
					if watchData.Version != nil {
						currentVersion = watchData.Version
					}
				}
				
			default:
				// If replication watcher is not available, use polling
				if ticker != nil {
					select {
					case <-ctx.Done():
						// Context canceled, send error and exit
						watchChan <- &topo.WatchData{
							Err: topo.NewError(topo.Interrupted, "watch canceled"),
						}
						return
					case <-ticker.C:
						// Poll for changes
						data, newVersion, err := s.Get(context.Background(), filePath)
						if err != nil {
							if topo.IsErrType(err, topo.NoNode) {
								// Node was deleted
								watchChan <- &topo.WatchData{
									Err: topo.NewError(topo.NoNode, filePath),
								}
								return
							}
							
							// Other error, retry
							log.Warningf("Error watching %v: %v", filePath, err)
							continue
						}
						
						// Check if the version changed
						if newVersion.String() != currentVersion.String() {
							// Version changed, send update
							watchChan <- &topo.WatchData{
								Contents: data,
								Version:  newVersion,
							}
							currentVersion = newVersion
						}
					default:
						// Just a non-blocking check to avoid busy waiting
						time.Sleep(10 * time.Millisecond)
					}
				} else {
					// If using replication watcher, just wait a bit
					time.Sleep(10 * time.Millisecond)
				}
			}
		}
	}()
	
	return &topo.WatchData{
		Contents: data,
		Version:  version,
	}, watchChan, nil
}

// WatchRecursive is part of the topo.Conn interface.
func (s *Server) WatchRecursive(ctx context.Context, dirPath string) ([]*topo.WatchDataRecursive, <-chan *topo.WatchDataRecursive, error) {
	// Get all files under the directory
	kvs, err := s.List(ctx, dirPath)
	if err != nil {
		return nil, nil, err
	}
	
	// Create the initial result
	initial := make([]*topo.WatchDataRecursive, 0, len(kvs))
	for _, kv := range kvs {
		initial = append(initial, &topo.WatchDataRecursive{
			Path: string(kv.Key),
			WatchData: topo.WatchData{
				Contents: kv.Value,
				Version:  kv.Version,
			},
		})
	}
	
	// Create a channel for changes
	watchChan := make(chan *topo.WatchDataRecursive, 10)
	
	// Create a unique watcher ID
	watcherID := uuid.New().String()
	
	// Register the watcher
	_, err = s.db.ExecContext(ctx, "INSERT INTO topo_watch (path, watcher_id) VALUES (?, ?)", dirPath, watcherID)
	if err != nil {
		return nil, nil, convertError(err, dirPath)
	}
	
	// Create a channel to receive replication events
	replicationChan := make(chan interface{}, 10)
	
	// Register with the replication watcher if available
	if s.replicationWatcher != nil && s.replicationWatcher.running {
		s.replicationWatcher.RegisterWatch(dirPath, replicationChan)
	}
	
	// Start a goroutine to process changes
	go func() {
		defer func() {
			// Unregister the watcher when done
			_, err := s.db.Exec("DELETE FROM topo_watch WHERE path = ? AND watcher_id = ?", dirPath, watcherID)
			if err != nil {
				log.Errorf("Failed to unregister watcher: %v", err)
			}
			
			// Unregister from the replication watcher
			if s.replicationWatcher != nil && s.replicationWatcher.running {
				s.replicationWatcher.UnregisterWatch(dirPath, replicationChan)
			}
			
			close(watchChan)
		}()
		
		// Keep track of the current versions
		versionMap := make(map[string]string)
		for _, item := range initial {
			versionMap[item.Path] = item.Version.String()
		}
		
		// If replication watcher is not available, fall back to polling
		var ticker *time.Ticker
		if s.replicationWatcher == nil || !s.replicationWatcher.running {
			ticker = time.NewTicker(1 * time.Second)
			defer ticker.Stop()
		}
		
		for {
			select {
			case <-ctx.Done():
				// Context canceled, send error and exit
				watchChan <- &topo.WatchDataRecursive{
					Path: dirPath,
					WatchData: topo.WatchData{
						Err: topo.NewError(topo.Interrupted, "watch canceled"),
					},
				}
				return
				
			case event := <-replicationChan:
				// Process replication event
				if watchDataRecursive, ok := event.(*topo.WatchDataRecursive); ok {
					// Send the watch data
					watchChan <- watchDataRecursive
					// Update the version map
					if watchDataRecursive.Version != nil {
						versionMap[watchDataRecursive.Path] = watchDataRecursive.Version.String()
					} else if watchDataRecursive.Err != nil && topo.IsErrType(watchDataRecursive.Err, topo.NoNode) {
						// Node was deleted
						delete(versionMap, watchDataRecursive.Path)
					}
				}
				
			default:
				// If replication watcher is not available, use polling
				if ticker != nil {
					select {
					case <-ctx.Done():
						// Context canceled, send error and exit
						watchChan <- &topo.WatchDataRecursive{
							Path: dirPath,
							WatchData: topo.WatchData{
								Err: topo.NewError(topo.Interrupted, "watch canceled"),
							},
						}
						return
					case <-ticker.C:
						// Poll for changes
						newKvs, err := s.List(context.Background(), dirPath)
						if err != nil {
							if topo.IsErrType(err, topo.NoNode) {
								// Directory was deleted
								watchChan <- &topo.WatchDataRecursive{
									Path: dirPath,
									WatchData: topo.WatchData{
										Err: topo.NewError(topo.NoNode, dirPath),
									},
								}
								return
							}
							
							// Other error, retry
							log.Warningf("Error watching %v: %v", dirPath, err)
							continue
						}
						
						// Check for new or updated files
						newVersionMap := make(map[string]bool)
						for _, kv := range newKvs {
							path := string(kv.Key)
							newVersionMap[path] = true
							
							oldVersion, exists := versionMap[path]
							if !exists || oldVersion != kv.Version.String() {
								// New or updated file
								watchChan <- &topo.WatchDataRecursive{
									Path: path,
									WatchData: topo.WatchData{
										Contents: kv.Value,
										Version:  kv.Version,
									},
								}
								versionMap[path] = kv.Version.String()
							}
						}
						
						// Check for deleted files
						for path := range versionMap {
							if !newVersionMap[path] {
								// File was deleted
								watchChan <- &topo.WatchDataRecursive{
									Path: path,
									WatchData: topo.WatchData{
										Err: topo.NewError(topo.NoNode, path),
									},
								}
								delete(versionMap, path)
							}
						}
					default:
						// Just a non-blocking check to avoid busy waiting
						time.Sleep(10 * time.Millisecond)
					}
				} else {
					// If using replication watcher, just wait a bit
					time.Sleep(10 * time.Millisecond)
				}
			}
		}
	}()
	
	return initial, watchChan, nil
}