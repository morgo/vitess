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
	"sync"
	"time"

	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/topo"
)

// MySQLLeaderParticipation implements topo.LeaderParticipation.
type MySQLLeaderParticipation struct {
	s      *Server
	name   string
	id     string
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	// waiters are waiting for a new leader
	waiters []chan string
}

// NewLeaderParticipation is part of the topo.Conn interface.
func (s *Server) NewLeaderParticipation(name, id string) (topo.LeaderParticipation, error) {
	return &MySQLLeaderParticipation{
		s:       s,
		name:    name,
		id:      id,
		done:    make(chan struct{}),
		waiters: make([]chan string, 0),
	}, nil
}

// WaitForLeadership is part of the topo.LeaderParticipation interface.
func (mp *MySQLLeaderParticipation) WaitForLeadership() (context.Context, error) {
	mp.mu.Lock()
	if mp.cancel != nil {
		mp.mu.Unlock()
		return nil, topo.NewError(topo.Interrupted, "leadership election canceled")
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	mp.cancel = cancel
	mp.mu.Unlock()
	
	// Try to become the leader
	go func() {
		defer close(mp.done)
		
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			
			// Check if we're already the leader
			currentLeader, err := mp.GetCurrentLeaderID(ctx)
			if err != nil && !topo.IsErrType(err, topo.NoNode) {
				log.Warningf("Error checking leadership: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}
			
			if currentLeader == mp.id {
				// We're already the leader, just refresh the expiration
				_, err := mp.s.db.ExecContext(ctx, 
					"UPDATE topo_elections SET expiration = ? WHERE name = ? AND leader_id = ?",
					time.Now().Add(10*time.Second), mp.name, mp.id)
				if err != nil {
					log.Warningf("Error refreshing leadership: %v", err)
				}
				time.Sleep(3 * time.Second)
				continue
			}
			
			// Try to become the leader
			result, err := mp.s.db.ExecContext(ctx, `
				INSERT INTO topo_elections (name, leader_id, contents, expiration)
				VALUES (?, ?, ?, ?)
				ON DUPLICATE KEY UPDATE leader_id = VALUES(leader_id), contents = VALUES(contents), expiration = VALUES(expiration)
				WHERE expiration < NOW()
			`, mp.name, mp.id, "", time.Now().Add(10*time.Second))
			
			if err != nil {
				log.Warningf("Error in leadership election: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}
			
			rowsAffected, err := result.RowsAffected()
			if err != nil {
				log.Warningf("Error checking rows affected: %v", err)
				time.Sleep(1 * time.Second)
				continue
			}
			
			if rowsAffected > 0 {
				// We became the leader, notify waiters
				mp.notifyWaiters(mp.id)
				
				// Keep refreshing our leadership
				for {
					select {
					case <-ctx.Done():
						return
					case <-time.After(3 * time.Second):
						// Refresh our leadership
						result, err := mp.s.db.ExecContext(ctx, 
							"UPDATE topo_elections SET expiration = ? WHERE name = ? AND leader_id = ?",
							time.Now().Add(10*time.Second), mp.name, mp.id)
						if err != nil {
							log.Warningf("Error refreshing leadership: %v", err)
							continue
						}
						
						rowsAffected, err := result.RowsAffected()
						if err != nil {
							log.Warningf("Error checking rows affected: %v", err)
							continue
						}
						
						if rowsAffected == 0 {
							// We lost leadership
							log.Warningf("Lost leadership for %v", mp.name)
							cancel()
							return
						}
					}
				}
			}
			
			// Wait before trying again
			time.Sleep(1 * time.Second)
		}
	}()
	
	return ctx, nil
}

// Stop is part of the topo.LeaderParticipation interface.
func (mp *MySQLLeaderParticipation) Stop() {
	mp.mu.Lock()
	if mp.cancel != nil {
		mp.cancel()
		mp.cancel = nil
	}
	mp.mu.Unlock()
	
	// Wait for the leadership goroutine to exit
	<-mp.done
	
	// If we're the leader, release leadership
	currentLeader, err := mp.GetCurrentLeaderID(context.Background())
	if err == nil && currentLeader == mp.id {
		_, err := mp.s.db.Exec("DELETE FROM topo_elections WHERE name = ? AND leader_id = ?", mp.name, mp.id)
		if err != nil {
			log.Warningf("Error releasing leadership: %v", err)
		}
	}
}

// GetCurrentLeaderID is part of the topo.LeaderParticipation interface.
func (mp *MySQLLeaderParticipation) GetCurrentLeaderID(ctx context.Context) (string, error) {
	var leaderID string
	var expiration time.Time
	
	err := mp.s.db.QueryRowContext(ctx, "SELECT leader_id, expiration FROM topo_elections WHERE name = ?", mp.name).Scan(&leaderID, &expiration)
	if err == sql.ErrNoRows {
		return "", topo.NewError(topo.NoNode, mp.name)
	} else if err != nil {
		return "", err
	}
	
	// Check if the leadership has expired
	if expiration.Before(time.Now()) {
		// Leadership has expired, delete it
		_, err := mp.s.db.ExecContext(ctx, "DELETE FROM topo_elections WHERE name = ? AND leader_id = ?", mp.name, leaderID)
		if err != nil {
			log.Warningf("Error deleting expired leadership: %v", err)
		}
		return "", topo.NewError(topo.NoNode, mp.name)
	}
	
	return leaderID, nil
}

// WaitForNewLeader is part of the topo.LeaderParticipation interface.
func (mp *MySQLLeaderParticipation) WaitForNewLeader(ctx context.Context) (<-chan string, error) {
	// Create a channel to receive leader updates
	leaderChan := make(chan string, 5)
	
	// Get the current leader
	currentLeader, err := mp.GetCurrentLeaderID(ctx)
	if err != nil && !topo.IsErrType(err, topo.NoNode) {
		return nil, err
	}
	
	// Send the current leader if there is one
	if currentLeader != "" {
		leaderChan <- currentLeader
	}
	
	// Register this channel to receive updates
	mp.mu.Lock()
	mp.waiters = append(mp.waiters, leaderChan)
	mp.mu.Unlock()
	
	// Start a goroutine to poll for changes
	go func() {
		defer func() {
			// Unregister the channel when done
			mp.mu.Lock()
			for i, ch := range mp.waiters {
				if ch == leaderChan {
					mp.waiters = append(mp.waiters[:i], mp.waiters[i+1:]...)
					break
				}
			}
			mp.mu.Unlock()
			close(leaderChan)
		}()
		
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		
		lastLeader := currentLeader
		
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				newLeader, err := mp.GetCurrentLeaderID(ctx)
				if err != nil && !topo.IsErrType(err, topo.NoNode) {
					log.Warningf("Error checking leadership: %v", err)
					continue
				}
				
				if newLeader == "" && topo.IsErrType(err, topo.NoNode) {
					// No leader
					if lastLeader != "" {
						// Leader was lost
						leaderChan <- ""
						lastLeader = ""
					}
				} else if newLeader != lastLeader {
					// Leader changed
					leaderChan <- newLeader
					lastLeader = newLeader
				}
			}
		}
	}()
	
	return leaderChan, nil
}

// notifyWaiters notifies all waiters of a new leader
func (mp *MySQLLeaderParticipation) notifyWaiters(leaderID string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	
	for _, ch := range mp.waiters {
		select {
		case ch <- leaderID:
		default:
			// Channel is full, skip
		}
	}
}