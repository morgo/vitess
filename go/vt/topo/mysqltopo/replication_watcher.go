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
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	mysqldriver "github.com/go-sql-driver/mysql"
	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/topo"
)

// ReplicationWatcher watches MySQL replication events for topo changes.
type ReplicationWatcher struct {
	s              *Server
	ctx            context.Context
	cancel         context.CancelFunc
	watchCallbacks map[string][]watchCallback
	mu             sync.Mutex
	syncer         *replication.BinlogSyncer
	streamer       *replication.BinlogStreamer
	running        bool
	dsn            string
}

// watchCallback is a callback function for watch events.
type watchCallback struct {
	path    string
	channel chan<- interface{}
}

// NewReplicationWatcher creates a new ReplicationWatcher.
func NewReplicationWatcher(s *Server) *ReplicationWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &ReplicationWatcher{
		s:              s,
		ctx:            ctx,
		cancel:         cancel,
		watchCallbacks: make(map[string][]watchCallback),
		running:        false,
	}
}

// Start starts the replication watcher.
func (w *ReplicationWatcher) Start() error {
	if w.running {
		return nil
	}

	// Parse the DSN from the server
	config := w.getDSNConfig()
	w.dsn = config.FormatDSN()

	// Create a unique server ID for this replication client
	serverID := uint32(rand.Intn(100000) + 1000000)

	// Create the binlog syncer
	syncerConfig := replication.BinlogSyncerConfig{
		ServerID: serverID,
		Flavor:   "mysql",
		Host:     config.Addr,
		Port:     3306, // Default MySQL port
		User:     config.User,
		Password: config.Passwd,
	}

	// Extract port from address if it contains a colon
	if strings.Contains(config.Addr, ":") {
		parts := strings.Split(config.Addr, ":")
		syncerConfig.Host = parts[0]
		if port, err := strconv.Atoi(parts[1]); err == nil {
			syncerConfig.Port = uint16(port)
		}
	}

	w.syncer = replication.NewBinlogSyncer(syncerConfig)

	// Get the current binlog position
	position, err := w.getCurrentPosition()
	if err != nil {
		log.Warningf("Failed to get current binlog position: %v", err)
		// Continue without replication watcher - we'll fall back to polling
		return err
	}

	// Start streaming from the current position
	w.streamer, err = w.syncer.StartSync(position)
	if err != nil {
		log.Warningf("Failed to start binlog sync: %v", err)
		// Continue without replication watcher - we'll fall back to polling
		return err
	}

	// Start a goroutine to process binlog events
	go w.processBinlogEvents()

	w.running = true
	return nil
}

// Stop stops the replication watcher.
func (w *ReplicationWatcher) Stop() {
	if !w.running {
		return
	}

	w.cancel()
	if w.syncer != nil {
		w.syncer.Close()
	}
	w.running = false
}

// RegisterWatch registers a watch for a path.
func (w *ReplicationWatcher) RegisterWatch(path string, ch chan<- interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	callbacks, ok := w.watchCallbacks[path]
	if !ok {
		callbacks = make([]watchCallback, 0)
	}
	
	callbacks = append(callbacks, watchCallback{
		path:    path,
		channel: ch,
	})
	
	w.watchCallbacks[path] = callbacks
}

// UnregisterWatch unregisters a watch for a path.
func (w *ReplicationWatcher) UnregisterWatch(path string, ch chan<- interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	callbacks, ok := w.watchCallbacks[path]
	if !ok {
		return
	}
	
	newCallbacks := make([]watchCallback, 0, len(callbacks))
	for _, callback := range callbacks {
		if callback.channel != ch {
			newCallbacks = append(newCallbacks, callback)
		}
	}
	
	if len(newCallbacks) == 0 {
		delete(w.watchCallbacks, path)
	} else {
		w.watchCallbacks[path] = newCallbacks
	}
}

// getDSNConfig extracts the MySQL connection parameters from the server.
func (w *ReplicationWatcher) getDSNConfig() *mysqldriver.Config {
	// Create a copy of the config
	config := mysqldriver.NewConfig()
	
	// Set default values
	config.User = "root"
	config.Net = "tcp"
	config.Addr = "localhost:3306"
	config.DBName = w.s.schema
	
	// Try to get the actual DSN from the database connection
	// Note: This is a simplified approach and might not work in all cases
	// In a real implementation, you would want to store the connection parameters
	// when creating the Server
	
	return config
}

// getCurrentPosition gets the current binlog position.
func (w *ReplicationWatcher) getCurrentPosition() (mysql.Position, error) {
	// Connect directly to MySQL using database/sql
	db, err := w.s.db.Conn(w.ctx)
	if err != nil {
		return mysql.Position{}, err
	}
	defer db.Close()
	
	// Execute SHOW MASTER STATUS
	var file string
	var position uint32
	var binlogDoDB, binlogIgnoreDB string
	var executed_gtid_set string
	
	err = db.QueryRowContext(w.ctx, "SHOW MASTER STATUS").Scan(&file, &position, &binlogDoDB, &binlogIgnoreDB, &executed_gtid_set)
	if err != nil {
		return mysql.Position{}, err
	}
	
	return mysql.Position{
		Name: file,
		Pos:  position,
	}, nil
}

// processBinlogEvents processes binlog events from MySQL.
func (w *ReplicationWatcher) processBinlogEvents() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("Recovered from panic in processBinlogEvents: %v", r)
			// Try to restart the replication watcher after a short delay
			time.Sleep(5 * time.Second)
			if err := w.Start(); err != nil {
				log.Errorf("Failed to restart replication watcher: %v", err)
			}
		}
	}()
	
	for {
		select {
		case <-w.ctx.Done():
			return
		default:
			// Get the next binlog event
			ev, err := w.streamer.GetEvent(w.ctx)
			if err != nil {
				if err == context.Canceled {
					return
				}
				log.Errorf("Error getting binlog event: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}
			
			// Process the event
			w.processEvent(ev)
		}
	}
}

// processEvent processes a single binlog event.
func (w *ReplicationWatcher) processEvent(ev *replication.BinlogEvent) {
	// We're only interested in row events
	switch e := ev.Event.(type) {
	case *replication.RowsEvent:
		// Check if this event is for our schema
		if string(e.Table.Schema) != w.s.schema {
			return
		}
		
		// Process based on table name
		tableName := string(e.Table.Table)
		switch tableName {
		case "topo_files":
			w.processFileEvent(e, ev.Header.EventType)
		case "topo_directories":
			w.processDirectoryEvent(e, ev.Header.EventType)
		case "topo_locks":
			w.processLockEvent(e, ev.Header.EventType)
		case "topo_elections":
			w.processElectionEvent(e, ev.Header.EventType)
		}
	}
}

// processFileEvent processes events for the topo_files table.
func (w *ReplicationWatcher) processFileEvent(e *replication.RowsEvent, eventType replication.EventType) {
	switch eventType {
	case replication.WRITE_ROWS_EVENTv1, replication.WRITE_ROWS_EVENTv2:
		// New file created
		for _, row := range e.Rows {
			if len(row) < 2 {
				continue
			}
			
			// Extract path from the row (first column)
			path, ok := row[0].(string)
			if !ok {
				continue
			}
			
			// Notify watchers about the new file
			w.notifyFileChange(path)
		}
		
	case replication.UPDATE_ROWS_EVENTv1, replication.UPDATE_ROWS_EVENTv2:
		// File updated
		for i := 0; i < len(e.Rows); i += 2 {
			if i+1 >= len(e.Rows) || len(e.Rows[i]) < 1 || len(e.Rows[i+1]) < 1 {
				continue
			}
			
			// Extract path from the row (first column)
			path, ok := e.Rows[i][0].(string)
			if !ok {
				continue
			}
			
			// Notify watchers about the updated file
			w.notifyFileChange(path)
		}
		
	case replication.DELETE_ROWS_EVENTv1, replication.DELETE_ROWS_EVENTv2:
		// File deleted
		for _, row := range e.Rows {
			if len(row) < 1 {
				continue
			}
			
			// Extract path from the row (first column)
			path, ok := row[0].(string)
			if !ok {
				continue
			}
			
			// Notify watchers about the deleted file
			w.notifyFileChange(path)
		}
	}
}

// processDirectoryEvent processes events for the topo_directories table.
func (w *ReplicationWatcher) processDirectoryEvent(e *replication.RowsEvent, eventType replication.EventType) {
	switch eventType {
	case replication.WRITE_ROWS_EVENTv1, replication.WRITE_ROWS_EVENTv2:
		// New directory created
		for _, row := range e.Rows {
			if len(row) < 1 {
				continue
			}
			
			// Extract path from the row (first column)
			path, ok := row[0].(string)
			if !ok {
				continue
			}
			
			// Notify watchers about the new directory
			w.notifyDirectoryChange(path)
		}
		
	case replication.DELETE_ROWS_EVENTv1, replication.DELETE_ROWS_EVENTv2:
		// Directory deleted
		for _, row := range e.Rows {
			if len(row) < 1 {
				continue
			}
			
			// Extract path from the row (first column)
			path, ok := row[0].(string)
			if !ok {
				continue
			}
			
			// Notify watchers about the deleted directory
			w.notifyDirectoryChange(path)
		}
	}
}

// processLockEvent processes events for the topo_locks table.
func (w *ReplicationWatcher) processLockEvent(e *replication.RowsEvent, eventType replication.EventType) {
	// For locks, we don't need to notify watchers directly
	// as the lock operations are handled separately
}

// processElectionEvent processes events for the topo_elections table.
func (w *ReplicationWatcher) processElectionEvent(e *replication.RowsEvent, eventType replication.EventType) {
	// For elections, we don't need to notify watchers directly
	// as the election operations are handled separately
}

// notifyFileChange notifies watchers about a change to a file.
func (w *ReplicationWatcher) notifyFileChange(path string) {
	// Get the current value of the file
	data, version, err := w.s.Get(context.Background(), path)
	
	// Prepare the watch data
	var watchData *topo.WatchData
	if err != nil {
		if topo.IsErrType(err, topo.NoNode) {
			// File was deleted
			watchData = &topo.WatchData{
				Err: topo.NewError(topo.NoNode, path),
			}
		} else {
			// Other error
			watchData = &topo.WatchData{
				Err: err,
			}
		}
	} else {
		// File exists
		watchData = &topo.WatchData{
			Contents: data,
			Version:  version,
		}
	}
	
	// Notify all watchers for this file
	w.notifyWatchers(path, watchData)
	
	// Also notify watchers for parent directories
	dirPath := path
	for {
		dirPath = getParentPath(dirPath)
		if dirPath == "" {
			break
		}
		
		// Create a recursive watch data
		watchDataRecursive := &topo.WatchDataRecursive{
			Path:      path,
			WatchData: *watchData,
		}
		
		// Notify directory watchers
		w.notifyWatchers(dirPath, watchDataRecursive)
	}
}

// notifyDirectoryChange notifies watchers about a change to a directory.
func (w *ReplicationWatcher) notifyDirectoryChange(path string) {
	// For directory changes, we notify watchers of the directory
	// and all parent directories
	
	// Check if the directory still exists
	_, err := w.s.ListDir(context.Background(), path, false)
	
	// Prepare the watch data
	var watchData *topo.WatchData
	if err != nil {
		if topo.IsErrType(err, topo.NoNode) {
			// Directory was deleted
			watchData = &topo.WatchData{
				Err: topo.NewError(topo.NoNode, path),
			}
		} else {
			// Other error
			watchData = &topo.WatchData{
				Err: err,
			}
		}
	} else {
		// Directory exists, but we don't have content for directories
		watchData = &topo.WatchData{
			Contents: nil,
			Version:  nil,
		}
	}
	
	// Create a recursive watch data
	watchDataRecursive := &topo.WatchDataRecursive{
		Path:      path,
		WatchData: *watchData,
	}
	
	// Notify watchers for this directory
	w.notifyWatchers(path, watchDataRecursive)
	
	// Also notify watchers for parent directories
	dirPath := path
	for {
		dirPath = getParentPath(dirPath)
		if dirPath == "" {
			break
		}
		
		// Notify directory watchers
		w.notifyWatchers(dirPath, watchDataRecursive)
	}
}

// notifyWatchers notifies all watchers of a change to a path.
func (w *ReplicationWatcher) notifyWatchers(path string, value interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	// Find all callbacks that match the path
	for watchPath, callbacks := range w.watchCallbacks {
		if path == watchPath || (path != "/" && path != "" && watchPath != "/" && watchPath != "" && (path == watchPath || path+"/" == watchPath || path == watchPath+"/")) {
			for _, callback := range callbacks {
				select {
				case callback.channel <- value:
					// Successfully sent the notification
				default:
					// Channel is full, log a warning
					log.Warningf("Failed to send watch notification for path %s: channel is full", path)
				}
			}
		}
	}
}

// getParentPath returns the parent path of a given path.
func getParentPath(path string) string {
	if path == "/" || path == "" {
		return ""
	}
	
	// Remove trailing slash if present
	if path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	
	// Find the last slash
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash <= 0 {
		return "/"
	}
	
	return path[:lastSlash]
}