/*
Copyright 2024 The Vitess Authors.

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

package vreplication

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vitess.io/vitess/go/mysql/capabilities"
	"vitess.io/vitess/go/sqltypes"
	"vitess.io/vitess/go/vt/binlog/binlogplayer"
	"vitess.io/vitess/go/vt/discovery"
	"vitess.io/vitess/go/vt/mysqlctl"
	"vitess.io/vitess/go/vt/topo/memorytopo"

	binlogdatapb "vitess.io/vitess/go/vt/proto/binlogdata"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
)

// TestVirtualKeyspaceReplicationPerformance tests replication throughput with virtual keyspaces
func TestVirtualKeyspaceReplicationPerformance(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Create keyspaces
	err := ts.CreateKeyspace(ctx, "source", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "source", "0")
	require.NoError(t, err)

	err = ts.CreateKeyspace(ctx, "main", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "main", "0")
	require.NoError(t, err)

	// Performance tracking
	var operationCounts = make(map[string]*int64)
	var operationTimes = make(map[string]*[]time.Duration)
	var perfMutex sync.Mutex

	dbClientFactory := func() binlogplayer.DBClient {
		return &performanceTrackingDBClient{
			dbName:          "vt_main",
			operationCounts: operationCounts,
			operationTimes:  operationTimes,
			mutex:           &perfMutex,
		}
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	err = vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Add multiple virtual keyspaces for performance testing
	virtualKeyspaces := []struct {
		name   string
		schema string
	}{
		{"commerce", "vt_commerce_0"},
		{"customer", "vt_customer_0"},
		{"inventory", "vt_inventory_0"},
		{"analytics", "vt_analytics_0"},
		{"reporting", "vt_reporting_0"},
	}

	for _, vks := range virtualKeyspaces {
		err = vre.AddVirtualKeyspace(vks.name, vks.schema)
		require.NoError(t, err)
	}

	vre.Open(ctx)

	// Test Case 1: Single virtual keyspace performance baseline
	t.Run("SingleVirtualKeyspacePerformance", func(t *testing.T) {
		perfMutex.Lock()
		operationCounts = make(map[string]*int64)
		operationTimes = make(map[string]*[]time.Duration)
		perfMutex.Unlock()

		params := map[string]string{
			"id":              "1",
			"workflow":        "perf_single",
			"source":          `keyspace:"source" shard:"0" filter:{rules:{match:"products" filter:"select * from products"}}`,
			"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
			"target_keyspace": "commerce",
			"db_name":         "vt_commerce_0",
			"options":         "{}",
		}

		controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
		require.NoError(t, err)
		defer controller.Stop()

		factory := controller.getSchemaSpecificDBClientFactory()
		client := factory()

		// Perform operations and measure performance
		operationCount := 1000
		startTime := time.Now()

		for i := 0; i < operationCount; i++ {
			_, err := client.ExecuteFetch(fmt.Sprintf("INSERT INTO products VALUES (%d, 'Product %d', 10.00)", i, i), 1000)
			require.NoError(t, err)
		}

		duration := time.Since(startTime)
		throughput := float64(operationCount) / duration.Seconds()

		t.Logf("Single virtual keyspace performance:")
		t.Logf("  Operations: %d", operationCount)
		t.Logf("  Duration: %v", duration)
		t.Logf("  Throughput: %.2f ops/sec", throughput)

		// Verify performance metrics
		perfMutex.Lock()
		defer perfMutex.Unlock()

		commerceCount := operationCounts["vt_commerce_0"]
		require.NotNil(t, commerceCount, "Should have operation count for commerce schema")
		assert.Equal(t, int64(operationCount), atomic.LoadInt64(commerceCount), "Should have correct operation count")

		// Performance assertion - should be reasonably fast
		assert.Greater(t, throughput, 100.0, "Should achieve at least 100 ops/sec for single virtual keyspace")
	})

	// Test Case 2: Multiple virtual keyspaces performance comparison
	t.Run("MultipleVirtualKeyspacesPerformance", func(t *testing.T) {
		perfMutex.Lock()
		operationCounts = make(map[string]*int64)
		operationTimes = make(map[string]*[]time.Duration)
		perfMutex.Unlock()

		// Create controllers for multiple virtual keyspaces
		controllers := []*controller{}

		for i, vks := range virtualKeyspaces {
			params := map[string]string{
				"id":              fmt.Sprintf("%d", i+10),
				"workflow":        fmt.Sprintf("perf_multi_%s", vks.name),
				"source":          fmt.Sprintf(`keyspace:"source" shard:"0" filter:{rules:{match:"%s_table" filter:"select * from %s_table"}}`, vks.name, vks.name),
				"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
				"target_keyspace": vks.name,
				"db_name":         vks.schema,
				"options":         "{}",
			}

			controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
			require.NoError(t, err)
			controllers = append(controllers, controller)
		}

		// Perform concurrent operations on all virtual keyspaces
		operationCount := 500
		var wg sync.WaitGroup
		startTime := time.Now()

		for i, vks := range virtualKeyspaces {
			wg.Add(1)
			go func(idx int, keyspace struct {
				name   string
				schema string
			}) {
				defer wg.Done()

				factory := controllers[idx].getSchemaSpecificDBClientFactory()
				client := factory()

				for j := 0; j < operationCount; j++ {
					_, err := client.ExecuteFetch(fmt.Sprintf("INSERT INTO %s_table VALUES (%d, 'Data %d')", keyspace.name, j, j), 1000)
					require.NoError(t, err)
				}
			}(i, vks)
		}

		wg.Wait()
		duration := time.Since(startTime)
		totalOperations := len(virtualKeyspaces) * operationCount
		throughput := float64(totalOperations) / duration.Seconds()

		t.Logf("Multiple virtual keyspaces performance:")
		t.Logf("  Virtual keyspaces: %d", len(virtualKeyspaces))
		t.Logf("  Operations per keyspace: %d", operationCount)
		t.Logf("  Total operations: %d", totalOperations)
		t.Logf("  Duration: %v", duration)
		t.Logf("  Throughput: %.2f ops/sec", throughput)

		// Verify all keyspaces processed operations
		perfMutex.Lock()
		defer perfMutex.Unlock()

		for _, vks := range virtualKeyspaces {
			count := operationCounts[vks.schema]
			require.NotNil(t, count, "Should have operation count for %s", vks.schema)
			assert.Equal(t, int64(operationCount), atomic.LoadInt64(count), "Should have correct operation count for %s", vks.schema)
		}

		// Performance assertion - should scale reasonably well
		assert.Greater(t, throughput, 200.0, "Should achieve at least 200 ops/sec for multiple virtual keyspaces")

		// Clean up
		for _, controller := range controllers {
			controller.Stop()
		}
	})

	// Test Case 3: Schema lookup performance
	t.Run("SchemaLookupPerformance", func(t *testing.T) {
		lookupCount := 10000
		startTime := time.Now()

		// Test schema lookup performance
		for i := 0; i < lookupCount; i++ {
			for _, vks := range virtualKeyspaces {
				schema, err := vre.GetSchemaForKeyspace(vks.name)
				require.NoError(t, err)
				assert.Equal(t, vks.schema, schema)
			}
		}

		duration := time.Since(startTime)
		totalLookups := lookupCount * len(virtualKeyspaces)
		lookupThroughput := float64(totalLookups) / duration.Seconds()

		t.Logf("Schema lookup performance:")
		t.Logf("  Lookups per keyspace: %d", lookupCount)
		t.Logf("  Total lookups: %d", totalLookups)
		t.Logf("  Duration: %v", duration)
		t.Logf("  Lookup throughput: %.2f lookups/sec", lookupThroughput)

		// Performance assertion - schema lookups should be very fast
		assert.Greater(t, lookupThroughput, 10000.0, "Schema lookups should achieve at least 10,000 lookups/sec")
	})
}

// TestSchemaLookupPerformance tests schema lookup latency and throughput
func TestSchemaLookupPerformance(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	dbClientFactory := func() binlogplayer.DBClient {
		return &performanceTrackingDBClient{
			dbName:          "vt_main",
			operationCounts: make(map[string]*int64),
			operationTimes:  make(map[string]*[]time.Duration),
			mutex:           &sync.Mutex{},
		}
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	err := vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Test Case 1: Baseline performance with few virtual keyspaces
	t.Run("BaselineSchemaLookupPerformance", func(t *testing.T) {
		// Add a few virtual keyspaces
		for i := 0; i < 5; i++ {
			err = vre.AddVirtualKeyspace(fmt.Sprintf("keyspace_%d", i), fmt.Sprintf("vt_keyspace_%d", i))
			require.NoError(t, err)
		}

		// Measure lookup performance
		lookupCount := 1000
		startTime := time.Now()

		for i := 0; i < lookupCount; i++ {
			schema, err := vre.GetSchemaForKeyspace("keyspace_2")
			require.NoError(t, err)
			assert.Equal(t, "vt_keyspace_2", schema)
		}

		duration := time.Since(startTime)
		avgLatency := duration / time.Duration(lookupCount)
		throughput := float64(lookupCount) / duration.Seconds()

		t.Logf("Baseline schema lookup performance (5 keyspaces):")
		t.Logf("  Lookups: %d", lookupCount)
		t.Logf("  Duration: %v", duration)
		t.Logf("  Average latency: %v", avgLatency)
		t.Logf("  Throughput: %.2f lookups/sec", throughput)

		// Performance assertions
		assert.Less(t, avgLatency, 100*time.Microsecond, "Average lookup latency should be under 100μs")
		assert.Greater(t, throughput, 10000.0, "Should achieve at least 10,000 lookups/sec")
	})

	// Test Case 2: Performance with many virtual keyspaces
	t.Run("ScaledSchemaLookupPerformance", func(t *testing.T) {
		// Add many more virtual keyspaces
		for i := 5; i < 100; i++ {
			err = vre.AddVirtualKeyspace(fmt.Sprintf("keyspace_%d", i), fmt.Sprintf("vt_keyspace_%d", i))
			require.NoError(t, err)
		}

		// Measure lookup performance with many keyspaces
		lookupCount := 1000
		startTime := time.Now()

		for i := 0; i < lookupCount; i++ {
			schema, err := vre.GetSchemaForKeyspace("keyspace_50")
			require.NoError(t, err)
			assert.Equal(t, "vt_keyspace_50", schema)
		}

		duration := time.Since(startTime)
		avgLatency := duration / time.Duration(lookupCount)
		throughput := float64(lookupCount) / duration.Seconds()

		t.Logf("Scaled schema lookup performance (100 keyspaces):")
		t.Logf("  Lookups: %d", lookupCount)
		t.Logf("  Duration: %v", duration)
		t.Logf("  Average latency: %v", avgLatency)
		t.Logf("  Throughput: %.2f lookups/sec", throughput)

		// Performance assertions - should not degrade significantly
		assert.Less(t, avgLatency, 500*time.Microsecond, "Average lookup latency should be under 500μs even with many keyspaces")
		assert.Greater(t, throughput, 2000.0, "Should achieve at least 2,000 lookups/sec even with many keyspaces")
	})

	// Test Case 3: Concurrent schema lookup performance
	t.Run("ConcurrentSchemaLookupPerformance", func(t *testing.T) {
		lookupCount := 1000
		goroutineCount := 10
		var wg sync.WaitGroup

		startTime := time.Now()

		for g := 0; g < goroutineCount; g++ {
			wg.Add(1)
			go func(goroutineID int) {
				defer wg.Done()

				for i := 0; i < lookupCount; i++ {
					keyspace := fmt.Sprintf("keyspace_%d", (goroutineID*lookupCount+i)%100)
					expectedSchema := fmt.Sprintf("vt_keyspace_%d", (goroutineID*lookupCount+i)%100)

					schema, err := vre.GetSchemaForKeyspace(keyspace)
					require.NoError(t, err)
					assert.Equal(t, expectedSchema, schema)
				}
			}(g)
		}

		wg.Wait()
		duration := time.Since(startTime)
		totalLookups := lookupCount * goroutineCount
		throughput := float64(totalLookups) / duration.Seconds()

		t.Logf("Concurrent schema lookup performance:")
		t.Logf("  Goroutines: %d", goroutineCount)
		t.Logf("  Lookups per goroutine: %d", lookupCount)
		t.Logf("  Total lookups: %d", totalLookups)
		t.Logf("  Duration: %v", duration)
		t.Logf("  Throughput: %.2f lookups/sec", throughput)

		// Performance assertion - concurrent lookups should be efficient
		assert.Greater(t, throughput, 5000.0, "Concurrent lookups should achieve at least 5,000 lookups/sec")
	})
}

// TestConnectionPoolEfficiency tests database connection pool utilization
func TestConnectionPoolEfficiency(t *testing.T) {
	ctx := context.Background()
	ts := memorytopo.NewServer(ctx, "cell1")
	defer ts.Close()

	// Create keyspaces
	err := ts.CreateKeyspace(ctx, "source", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "source", "0")
	require.NoError(t, err)

	err = ts.CreateKeyspace(ctx, "main", &topodatapb.Keyspace{})
	require.NoError(t, err)
	err = ts.CreateShard(ctx, "main", "0")
	require.NoError(t, err)

	// Connection tracking
	var connectionCounts = make(map[string]*int64)
	var activeConnections = make(map[string]*int64)
	var connMutex sync.Mutex

	dbClientFactory := func() binlogplayer.DBClient {
		return &connectionTrackingDBClient{
			dbName:            "vt_main",
			connectionCounts:  connectionCounts,
			activeConnections: activeConnections,
			mutex:             &connMutex,
		}
	}

	mysqld := &mysqlctl.FakeMysqlDaemon{}
	vre := NewTestEngine(ts, "cell1", mysqld, dbClientFactory, dbClientFactory, "vt_main", nil)
	defer vre.Close()

	err = vre.InitDBConfigWithKeyspace("main")
	require.NoError(t, err)

	// Add virtual keyspaces
	virtualKeyspaces := []string{"commerce", "customer", "inventory", "analytics"}
	for i, vks := range virtualKeyspaces {
		err = vre.AddVirtualKeyspace(vks, fmt.Sprintf("vt_%s_%d", vks, i))
		require.NoError(t, err)
	}

	vre.Open(ctx)

	// Test Case 1: Connection pool utilization
	t.Run("ConnectionPoolUtilization", func(t *testing.T) {
		connMutex.Lock()
		connectionCounts = make(map[string]*int64)
		activeConnections = make(map[string]*int64)
		connMutex.Unlock()

		// Create controllers for each virtual keyspace
		controllers := []*controller{}

		for i, vks := range virtualKeyspaces {
			params := map[string]string{
				"id":              fmt.Sprintf("%d", i+1),
				"workflow":        fmt.Sprintf("conn_test_%s", vks),
				"source":          fmt.Sprintf(`keyspace:"source" shard:"0" filter:{rules:{match:"%s_table" filter:"select * from %s_table"}}`, vks, vks),
				"state":           binlogdatapb.VReplicationWorkflowState_Stopped.String(),
				"target_keyspace": vks,
				"db_name":         fmt.Sprintf("vt_%s_%d", vks, i),
				"options":         "{}",
			}

			controller, err := newController(ctx, params, dbClientFactory, mysqld, ts, "cell1", nil, vre, discovery.TabletPickerOptions{})
			require.NoError(t, err)
			controllers = append(controllers, controller)
		}

		// Perform operations to test connection pooling
		var wg sync.WaitGroup
		operationCount := 100

		for i, vks := range virtualKeyspaces {
			wg.Add(1)
			go func(idx int, keyspace string) {
				defer wg.Done()

				factory := controllers[idx].getSchemaSpecificDBClientFactory()

				// Create multiple clients to test pooling
				clients := make([]binlogplayer.DBClient, 5)
				for j := 0; j < 5; j++ {
					clients[j] = factory()
					clients[j].Connect()
				}

				// Perform operations
				for op := 0; op < operationCount; op++ {
					client := clients[op%5]
					_, err := client.ExecuteFetch(fmt.Sprintf("SELECT * FROM %s_table WHERE id = %d", keyspace, op), 1000)
					require.NoError(t, err)
				}

				// Close clients
				for j := 0; j < 5; j++ {
					clients[j].Close()
				}
			}(i, vks)
		}

		wg.Wait()

		// Analyze connection usage
		connMutex.Lock()
		defer connMutex.Unlock()

		totalConnections := int64(0)
		maxActiveConnections := int64(0)

		for schema, count := range connectionCounts {
			connCount := atomic.LoadInt64(count)
			activeCount := atomic.LoadInt64(activeConnections[schema])

			totalConnections += connCount
			if activeCount > maxActiveConnections {
				maxActiveConnections = activeCount
			}

			t.Logf("Schema %s: Total connections: %d, Max active: %d", schema, connCount, activeCount)
		}

		t.Logf("Connection pool efficiency:")
		t.Logf("  Total connections created: %d", totalConnections)
		t.Logf("  Max concurrent active connections: %d", maxActiveConnections)
		t.Logf("  Virtual keyspaces: %d", len(virtualKeyspaces))

		// Efficiency assertions
		assert.Greater(t, totalConnections, int64(0), "Should have created connections")
		assert.LessOrEqual(t, maxActiveConnections, int64(len(virtualKeyspaces)*5), "Should not exceed expected max active connections")

		// Clean up
		for _, controller := range controllers {
			controller.Stop()
		}
	})
}

// PerformanceMetrics holds performance measurement data
type PerformanceMetrics struct {
	OperationCount int64
	Duration       time.Duration
	Throughput     float64
	AvgLatency     time.Duration
}

// performanceTrackingDBClient implements binlogplayer.DBClient for performance tracking
type performanceTrackingDBClient struct {
	dbName          string
	operationCounts map[string]*int64
	operationTimes  map[string]*[]time.Duration
	mutex           *sync.Mutex
}

func (m *performanceTrackingDBClient) DBName() string {
	return m.dbName
}

func (m *performanceTrackingDBClient) Connect() error {
	return nil
}

func (m *performanceTrackingDBClient) Begin() error {
	return nil
}

func (m *performanceTrackingDBClient) Commit() error {
	return nil
}

func (m *performanceTrackingDBClient) Rollback() error {
	return nil
}

func (m *performanceTrackingDBClient) Close() {
}

func (m *performanceTrackingDBClient) IsClosed() bool {
	return false
}

func (m *performanceTrackingDBClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	startTime := time.Now()

	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Initialize counters if needed
	if m.operationCounts[m.dbName] == nil {
		count := int64(0)
		m.operationCounts[m.dbName] = &count
	}
	if m.operationTimes[m.dbName] == nil {
		times := make([]time.Duration, 0)
		m.operationTimes[m.dbName] = &times
	}

	// Record operation
	atomic.AddInt64(m.operationCounts[m.dbName], 1)

	duration := time.Since(startTime)
	*m.operationTimes[m.dbName] = append(*m.operationTimes[m.dbName], duration)

	return &sqltypes.Result{}, nil
}

func (m *performanceTrackingDBClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	_, err := m.ExecuteFetch(query, maxrows)
	return []*sqltypes.Result{{}}, err
}

func (m *performanceTrackingDBClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (m *performanceTrackingDBClient) SetDBName(dbName string) {
	m.dbName = dbName
}

// connectionTrackingDBClient implements binlogplayer.DBClient for connection tracking
type connectionTrackingDBClient struct {
	dbName            string
	connectionCounts  map[string]*int64
	activeConnections map[string]*int64
	mutex             *sync.Mutex
	connected         bool
}

func (m *connectionTrackingDBClient) DBName() string {
	return m.dbName
}

func (m *connectionTrackingDBClient) Connect() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Initialize counters if needed
	if m.connectionCounts[m.dbName] == nil {
		count := int64(0)
		m.connectionCounts[m.dbName] = &count
	}
	if m.activeConnections[m.dbName] == nil {
		active := int64(0)
		m.activeConnections[m.dbName] = &active
	}

	// Record connection
	atomic.AddInt64(m.connectionCounts[m.dbName], 1)
	atomic.AddInt64(m.activeConnections[m.dbName], 1)
	m.connected = true

	return nil
}

func (m *connectionTrackingDBClient) Begin() error {
	return nil
}

func (m *connectionTrackingDBClient) Commit() error {
	return nil
}

func (m *connectionTrackingDBClient) Rollback() error {
	return nil
}

func (m *connectionTrackingDBClient) Close() {
	if m.connected {
		m.mutex.Lock()
		defer m.mutex.Unlock()

		if m.activeConnections[m.dbName] != nil {
			atomic.AddInt64(m.activeConnections[m.dbName], -1)
		}
		m.connected = false
	}
}

func (m *connectionTrackingDBClient) IsClosed() bool {
	return !m.connected
}

func (m *connectionTrackingDBClient) ExecuteFetch(query string, maxrows int) (*sqltypes.Result, error) {
	return &sqltypes.Result{}, nil
}

func (m *connectionTrackingDBClient) ExecuteFetchMulti(query string, maxrows int) ([]*sqltypes.Result, error) {
	return []*sqltypes.Result{{}}, nil
}

func (m *connectionTrackingDBClient) SupportsCapability(capability capabilities.FlavorCapability) (bool, error) {
	return false, nil
}

func (m *connectionTrackingDBClient) SetDBName(dbName string) {
	m.dbName = dbName
}
