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
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/go-sql-driver/mysql"
	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/topo"
)

// Server is the implementation of topo.Server for MySQL.
type Server struct {
	// conn is the MySQL connection.
	db *sql.DB

	// mu protects the following fields.
	mu sync.Mutex

	// schema is the name of the schema to use.
	schema string

	// cells is a map of cell name to cell Server.
	cells map[string]*Server

	// root is the root path for this server.
	root string

	// replicationWatcher watches MySQL replication for changes
	replicationWatcher *ReplicationWatcher
}

// MySQLVersion implements topo.Version for MySQL.
type MySQLVersion int64

// String implements topo.Version.String.
func (v MySQLVersion) String() string {
	return fmt.Sprintf("%d", v)
}

// NewServer returns a new MySQL topo.Server.
func NewServer(serverAddr, root string) (*Server, error) {
	// Parse the server address to get connection parameters
	params := parseServerAddr(serverAddr)

	// Ensure the schema exists
	params.DBName = DefaultTopoSchema // Use the default topo schema
	if err := createSchemaIfNotExists(*params); err != nil {
		return nil, err
	}

	// Connect to MySQL
	db, err := sql.Open("mysql", params.FormatDSN())
	if err != nil {
		return nil, err
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	// Create the server
	server := &Server{
		db:     db,
		schema: params.DBName,
		cells:  make(map[string]*Server),
		root:   root,
	}

	// Create the required tables
	if err := server.createTablesIfNotExist(); err != nil {
		db.Close()
		return nil, err
	}

	// Create and start the replication watcher
	server.replicationWatcher = NewReplicationWatcher(server)
	if err := server.replicationWatcher.Start(); err != nil {
		log.Warningf("Failed to start replication watcher: %v", err)
		// Continue without replication watcher - we'll fall back to polling
	}

	return server, nil
}

// parseServerAddr parses the server address into MySQL connection parameters.
func parseServerAddr(serverAddr string) *mysql.Config {
	// Default configuration
	config := mysql.NewConfig()
	config.User = "root"
	config.Net = "tcp"
	config.Addr = "localhost:3306"
	config.DBName = DefaultTopoSchema
	config.ParseTime = true
	config.MultiStatements = true
	config.InterpolateParams = true

	// Parse the server address
	if serverAddr != "" {
		parts := strings.Split(serverAddr, "@")
		if len(parts) > 1 {
			// Format is user:pass@host:port/dbname
			userParts := strings.Split(parts[0], ":")
			config.User = userParts[0]
			if len(userParts) > 1 {
				config.Passwd = userParts[1]
			}

			hostParts := strings.Split(parts[1], "/")
			config.Addr = hostParts[0]
			if len(hostParts) > 1 {
				config.DBName = hostParts[1]
			}
		} else {
			// Format is host:port/dbname
			hostParts := strings.Split(serverAddr, "/")
			config.Addr = hostParts[0]
			if len(hostParts) > 1 {
				config.DBName = hostParts[1]
			}
		}
	}

	config.DBName = DefaultTopoSchema // hack, fix later.
	return config
}

// createSchemaIfNotExists creates the schema if it doesn't exist.
func createSchemaIfNotExists(config mysql.Config) error {
	// Save the original database name
	schemaName := config.DBName

	// Connect without specifying a database
	// just for this function.
	config.DBName = ""
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()

	// Create the schema if it doesn't exist
	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", schemaName))
	return err
}

// createTablesIfNotExist creates the required tables if they don't exist.
func (s *Server) createTablesIfNotExist() error {
	// Create the tables
	queries := []string{
		// topo_files table stores the file data
		`CREATE TABLE IF NOT EXISTS topo_files (
			path VARCHAR(512) NOT NULL PRIMARY KEY,
			data MEDIUMBLOB,
			version BIGINT NOT NULL AUTO_INCREMENT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			modified_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			KEY (version)
		) ENGINE=InnoDB`,

		// topo_locks table stores lock information
		`CREATE TABLE IF NOT EXISTS topo_locks (
			path VARCHAR(512) NOT NULL PRIMARY KEY,
			owner VARCHAR(255) NOT NULL,
			contents TEXT,
			expiration TIMESTAMP NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB`,

		// topo_directories table stores directory information
		`CREATE TABLE IF NOT EXISTS topo_directories (
			path VARCHAR(512) NOT NULL PRIMARY KEY,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB`,

		// topo_watch table stores watch information for change notifications
		`CREATE TABLE IF NOT EXISTS topo_watch (
			path VARCHAR(512) NOT NULL,
			watcher_id VARCHAR(64) NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (path, watcher_id)
		) ENGINE=InnoDB`,

		// topo_elections table stores election information
		`CREATE TABLE IF NOT EXISTS topo_elections (
			name VARCHAR(512) NOT NULL PRIMARY KEY,
			leader_id VARCHAR(255) NOT NULL,
			contents TEXT,
			expiration TIMESTAMP NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			modified_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB`,
	}

	// Execute each query
	for _, query := range queries {
		if _, err := s.db.Exec(query); err != nil {
			return err
		}
	}

	return nil
}

// Close is part of the topo.Server interface.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Stop the replication watcher if it's running
	if s.replicationWatcher != nil {
		s.replicationWatcher.Stop()
		s.replicationWatcher = nil
	}

	if s.db != nil {
		s.db.Close()
		s.db = nil
	}

	for _, cell := range s.cells {
		cell.Close()
	}
	s.cells = nil
}

// Factory is a factory for Server objects.
type Factory struct{}

// HasGlobalReadOnlyCell implements topo.Factory.
func (f Factory) HasGlobalReadOnlyCell(serverAddr, root string) bool {
	// MySQL topo doesn't support read-only cells
	return false
}

// Create implements topo.Factory.
func (f Factory) Create(cell, serverAddr, root string) (topo.Conn, error) {
	server, err := NewServer(serverAddr, root)
	if err != nil {
		return nil, err
	}
	return server, nil
}

func init() {
	topo.RegisterFactory("mysql", &Factory{})
}
