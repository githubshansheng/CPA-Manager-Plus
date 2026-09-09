package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Metric struct {
	Available bool    `json:"available"`
	Value     float64 `json:"value,omitempty"`
	Error     string  `json:"error,omitempty"`
}

type PoolStatus struct {
	Open         int   `json:"open"`
	InUse        int   `json:"inUse"`
	Idle         int   `json:"idle"`
	WaitCount    int64 `json:"waitCount"`
	WaitDuration int64 `json:"waitDurationMs"`
}

type MonitorStatus struct {
	Connected       bool              `json:"connected"`
	Version         string            `json:"version,omitempty"`
	VersionComment  string            `json:"versionComment,omitempty"`
	PingLatencyMS   int64             `json:"pingLatencyMs,omitempty"`
	SampledAtMS     int64             `json:"sampledAtMs"`
	DatabaseBytes   Metric            `json:"databaseBytes"`
	TableBytes      Metric            `json:"tableBytes"`
	IndexBytes      Metric            `json:"indexBytes"`
	Pool            PoolStatus        `json:"pool"`
	Connections     Metric            `json:"connections"`
	MaxConnections  Metric            `json:"maxConnections"`
	UptimeSeconds   Metric            `json:"uptimeSeconds"`
	QPS             Metric            `json:"qps"`
	TPS             Metric            `json:"tps"`
	SlowQueries     Metric            `json:"slowQueries"`
	BufferPoolBytes Metric            `json:"bufferPoolBytes"`
	BufferPoolUsed  Metric            `json:"bufferPoolUsedBytes"`
	LockWaits       Metric            `json:"lockWaits"`
	Deadlocks       Metric            `json:"deadlocks"`
	Advanced        map[string]Metric `json:"advanced,omitempty"`
	Error           string            `json:"error,omitempty"`
}

type sampleCounters struct {
	at        time.Time
	questions float64
	commits   float64
	rollbacks float64
}

type Sampler struct {
	mu       sync.Mutex
	previous map[*sql.DB]sampleCounters
}

func NewSampler() *Sampler { return &Sampler{previous: map[*sql.DB]sampleCounters{}} }

var defaultSampler = NewSampler()

func Sample(ctx context.Context, db *sql.DB) MonitorStatus {
	return defaultSampler.Sample(ctx, db)
}

func (s *Sampler) Sample(ctx context.Context, db *sql.DB) MonitorStatus {
	now := time.Now()
	status := MonitorStatus{SampledAtMS: now.UnixMilli(), Advanced: map[string]Metric{}}
	stats := db.Stats()
	status.Pool = PoolStatus{Open: stats.OpenConnections, InUse: stats.InUse, Idle: stats.Idle,
		WaitCount: stats.WaitCount, WaitDuration: stats.WaitDuration.Milliseconds()}
	started := time.Now()
	if err := db.PingContext(ctx); err != nil {
		status.Error = err.Error()
		return status
	}
	status.Connected = true
	status.PingLatencyMS = time.Since(started).Milliseconds()
	if err := db.QueryRowContext(ctx, `select @@version, @@version_comment`).Scan(&status.Version, &status.VersionComment); err != nil {
		status.Error = "read server version: " + err.Error()
	}
	status.DatabaseBytes, status.TableBytes, status.IndexBytes = sampleSizes(ctx, db)
	serverStatus, serverStatusErr := readGlobalStatus(ctx, db, []string{
		"Threads_connected", "Uptime", "Questions", "Com_commit", "Com_rollback",
		"Slow_queries", "Innodb_buffer_pool_pages_data", "Innodb_buffer_pool_pages_total",
		"Innodb_page_size", "Innodb_row_lock_current_waits", "Innodb_deadlocks",
	})
	metric := func(name string) Metric {
		if serverStatusErr != nil {
			return unavailable(serverStatusErr)
		}
		value, ok := serverStatus[name]
		if !ok {
			return Metric{Error: "metric is not reported by MySQL"}
		}
		return Metric{Available: true, Value: value}
	}
	status.Connections = metric("Threads_connected")
	status.UptimeSeconds = metric("Uptime")
	status.SlowQueries = metric("Slow_queries")
	status.LockWaits = metric("Innodb_row_lock_current_waits")
	status.Deadlocks = metric("Innodb_deadlocks")
	status.MaxConnections = sampleVariable(ctx, db, "max_connections")
	status.BufferPoolBytes = sampleVariable(ctx, db, "innodb_buffer_pool_size")
	pagesData, pagesOK := serverStatus["Innodb_buffer_pool_pages_data"]
	pageSize, pageOK := serverStatus["Innodb_page_size"]
	if serverStatusErr != nil {
		status.BufferPoolUsed = unavailable(serverStatusErr)
	} else if pagesOK && pageOK {
		status.BufferPoolUsed = Metric{Available: true, Value: pagesData * pageSize}
	} else {
		status.BufferPoolUsed = Metric{Error: "buffer pool page metrics are not reported by MySQL"}
	}
	status.QPS, status.TPS = s.rates(db, now, serverStatus, serverStatusErr)
	status.Advanced["dataLockWaits"] = sampleScalar(ctx, db,
		`select count(*) from performance_schema.data_lock_waits`)
	status.Advanced["metadataLockWaits"] = sampleScalar(ctx, db,
		`select count(*) from performance_schema.metadata_locks where lock_status = 'PENDING'`)
	return status
}

func sampleSizes(ctx context.Context, db *sql.DB) (Metric, Metric, Metric) {
	var data, indexes sql.NullFloat64
	err := db.QueryRowContext(ctx, `select coalesce(sum(data_length), 0), coalesce(sum(index_length), 0)
		from information_schema.tables where table_schema = database()`).Scan(&data, &indexes)
	if err != nil {
		metric := unavailable(err)
		return metric, metric, metric
	}
	tableMetric := Metric{Available: true, Value: data.Float64}
	indexMetric := Metric{Available: true, Value: indexes.Float64}
	return Metric{Available: true, Value: data.Float64 + indexes.Float64}, tableMetric, indexMetric
}

func readGlobalStatus(ctx context.Context, db *sql.DB, names []string) (map[string]float64, error) {
	literals := make([]string, len(names))
	for i, name := range names {
		if !mysqlStatusNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid mysql status name %q", name)
		}
		literals[i] = "'" + name + "'"
	}
	rows, err := db.QueryContext(ctx, `show global status where variable_name in (`+strings.Join(literals, ",")+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]float64{}
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		values[name] = value
	}
	return values, rows.Err()
}

func sampleVariable(ctx context.Context, db *sql.DB, name string) Metric {
	var value float64
	var query string
	switch name {
	case "max_connections":
		query = `select @@global.max_connections`
	case "innodb_buffer_pool_size":
		query = `select @@global.innodb_buffer_pool_size`
	default:
		return Metric{Error: "unsupported server variable"}
	}
	err := db.QueryRowContext(ctx, query).Scan(&value)
	if err != nil {
		return unavailable(err)
	}
	return Metric{Available: true, Value: value}
}

func sampleScalar(ctx context.Context, db *sql.DB, query string) Metric {
	var value float64
	if err := db.QueryRowContext(ctx, query).Scan(&value); err != nil {
		return unavailable(err)
	}
	return Metric{Available: true, Value: value}
}

func unavailable(err error) Metric {
	return Metric{Error: fmt.Sprintf("unavailable: %v", err)}
}

func (s *Sampler) rates(db *sql.DB, now time.Time, values map[string]float64, sampleErr error) (Metric, Metric) {
	if sampleErr != nil {
		metric := unavailable(sampleErr)
		return metric, metric
	}
	questions, qOK := values["Questions"]
	commits, cOK := values["Com_commit"]
	rollbacks, rOK := values["Com_rollback"]
	if !qOK || !cOK || !rOK {
		metric := Metric{Error: "rate counters are not reported by MySQL"}
		return metric, metric
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.previous[db]
	s.previous[db] = sampleCounters{at: now, questions: questions, commits: commits, rollbacks: rollbacks}
	if !ok {
		metric := Metric{Error: "a second sample is required to calculate a rate"}
		return metric, metric
	}
	seconds := now.Sub(previous.at).Seconds()
	if seconds <= 0 || questions < previous.questions {
		metric := Metric{Error: "server counters were reset"}
		return metric, metric
	}
	qps := (questions - previous.questions) / seconds
	txNow, txPrevious := commits+rollbacks, previous.commits+previous.rollbacks
	if txNow < txPrevious {
		return Metric{Available: true, Value: qps}, Metric{Error: "transaction counters were reset"}
	}
	return Metric{Available: true, Value: qps}, Metric{Available: true, Value: (txNow - txPrevious) / seconds}
}
