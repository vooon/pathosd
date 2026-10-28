package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/itchyny/gojq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

// SQLChecker connects to a MySQL/MariaDB or PostgreSQL server and runs the
// configured queries in order on one connection. Each query must succeed;
// when it carries a result_jq expression the rows (as a JSON array of
// column→value objects) must make the expression true. Replication lag,
// read-only state or any other condition is expressed as a query plus jq,
// so nothing about the schema is hard-coded.
type SQLChecker struct {
	cfg     config.SQLCheckConfig
	db      *sql.DB
	queries []sqlQuery
	target  string
}

type sqlQuery struct {
	name string
	sql  string
	jq   *gojq.Query
}

// sqlMaxRowsDefault bounds the rows fetched per query when max_rows is unset.
const sqlMaxRowsDefault = 100

func NewSQLChecker(cfg *config.SQLCheckConfig) (*SQLChecker, error) {
	connector, target, err := sqlConnector(cfg)
	if err != nil {
		return nil, err
	}
	c, err := newSQLChecker(cfg, sql.OpenDB(connector))
	if err != nil {
		return nil, err
	}
	c.target = target
	return c, nil
}

// newSQLChecker wires an opened database handle; tests use it with a mock.
func newSQLChecker(cfg *config.SQLCheckConfig, db *sql.DB) (*SQLChecker, error) {
	c := &SQLChecker{cfg: *cfg, db: db}
	queries := cfg.Queries
	if len(queries) == 0 {
		queries = []config.SQLQueryConfig{{SQL: "SELECT 1"}}
	}
	for i, q := range queries {
		cq := sqlQuery{name: q.EffectiveName(i), sql: q.SQL}
		if q.ResultJQ != "" {
			parsed, err := gojq.Parse(q.ResultJQ)
			if err != nil {
				return nil, fmt.Errorf("query %s: invalid result_jq: %w", cq.name, err)
			}
			cq.jq = parsed
		}
		c.queries = append(c.queries, cq)
	}
	// One connection; dropped after each check unless keep_alive, so every
	// run exercises connect and authentication.
	db.SetMaxOpenConns(1)
	if cfg.KeepAlive {
		db.SetMaxIdleConns(1)
		db.SetConnMaxIdleTime(90 * time.Second)
	} else {
		db.SetMaxIdleConns(0)
	}
	return c, nil
}

// sqlConnector builds the driver connector for cfg.Driver. Config structs are
// used instead of DSN strings so passwords never need escaping or logging.
func sqlConnector(cfg *config.SQLCheckConfig) (driver.Connector, string, error) {
	tlsCfg, err := sqlTLSConfig(cfg)
	if err != nil {
		return nil, "", err
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	switch cfg.Driver {
	case config.SQLDriverMySQL:
		mc := mysql.NewConfig()
		mc.User = cfg.User
		mc.Passwd = cfg.Password
		mc.Net = "tcp"
		mc.Addr = addr
		mc.DBName = cfg.Database
		mc.Timeout = 5 * time.Second // bounded by the check context as well
		mc.TLS = tlsCfg
		mc.ParseTime = true // DATETIME/TIMESTAMP as time.Time, rendered RFC 3339 like pgx
		conn, err := mysql.NewConnector(mc)
		if err != nil {
			return nil, "", fmt.Errorf("mysql connector: %w", err)
		}
		return conn, "mysql " + addr, nil
	case config.SQLDriverPostgres:
		pc, err := pgx.ParseConfig("")
		if err != nil {
			return nil, "", fmt.Errorf("postgres config: %w", err)
		}
		pc.Host = cfg.Host
		pc.Port = cfg.Port
		pc.User = cfg.User
		pc.Password = cfg.Password
		pc.Database = cfg.Database
		pc.TLSConfig = tlsCfg
		pc.ConnectTimeout = 5 * time.Second
		return stdlib.GetConnector(*pc), "postgres " + addr, nil
	default:
		return nil, "", fmt.Errorf("unsupported sql driver %q", cfg.Driver)
	}
}

func sqlTLSConfig(cfg *config.SQLCheckConfig) (*tls.Config, error) {
	if !cfg.TLS {
		return nil, nil
	}
	tc := &tls.Config{InsecureSkipVerify: cfg.TLSInsecure, ServerName: cfg.TLSServerName} //nolint:gosec // operator opt-in, validated as exclusive with tls_ca_cert
	if tc.ServerName == "" {
		tc.ServerName = cfg.Host
	}
	if cfg.TLSCACert != "" {
		pem, err := os.ReadFile(cfg.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("reading tls_ca_cert: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("parsing tls_ca_cert %q: no certificates found", cfg.TLSCACert)
		}
		tc.RootCAs = pool
	}
	return tc, nil
}

func (c *SQLChecker) Type() string { return "sql" }

func (c *SQLChecker) Check(ctx context.Context) Result {
	start := time.Now()
	details := &model.SQLDetails{}
	fail := func(err error, detail string) Result {
		return Result{Duration: time.Since(start), Err: err, Detail: detail, Details: details, TimedOut: ctx.Err() != nil}
	}

	conn, err := c.db.Conn(ctx)
	if err != nil {
		return fail(err, fmt.Sprintf("%s: connect: %v", c.target, err))
	}
	defer func() { _ = conn.Close() }()

	maxRows := c.cfg.MaxRows
	if maxRows <= 0 {
		maxRows = sqlMaxRowsDefault
	}
	for _, q := range c.queries {
		qStart := time.Now()
		rows, err := c.runQuery(ctx, conn, q.sql, maxRows)
		qd := model.SQLQueryDetails{Name: q.name, Rows: len(rows), Duration: time.Since(qStart)}
		if err != nil {
			details.Queries = append(details.Queries, qd)
			return fail(err, fmt.Sprintf("%s: query %s: %v", c.target, q.name, err))
		}
		if q.jq != nil {
			if err := evalJQ(ctx, q.jq, rows); err != nil {
				details.Queries = append(details.Queries, qd)
				return fail(err, fmt.Sprintf("%s: query %s: %v", c.target, q.name, err))
			}
		}
		qd.Passed = true
		details.Queries = append(details.Queries, qd)
	}

	return Result{
		Success:  true,
		Duration: time.Since(start),
		Detail:   fmt.Sprintf("%s: %d queries ok", c.target, len(c.queries)),
		Details:  details,
	}
}

// runQuery executes one statement and returns up to maxRows rows as
// column→value maps with driver values normalised for jq (numbers, strings,
// bools, null).
func (c *SQLChecker) runQuery(ctx context.Context, conn *sql.Conn, query string, maxRows int) ([]map[string]any, error) {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	numeric := make([]bool, len(cols))
	for i, t := range types {
		numeric[i] = isNumericSQLType(t.DatabaseTypeName())
	}

	var out []map[string]any
	for rows.Next() && len(out) < maxRows {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = normalizeSQLValue(raw[i], numeric[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// isNumericSQLType reports whether a driver column type name denotes a number.
// MySQL's text protocol returns every value as []byte, so the column type
// decides whether to parse it.
func isNumericSQLType(name string) bool {
	name = strings.ToUpper(strings.TrimPrefix(name, "UNSIGNED "))
	switch name {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT",
		"DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL",
		"INT2", "INT4", "INT8", "FLOAT4", "FLOAT8", "OID", "YEAR":
		return true
	}
	return false
}

// normalizeSQLValue converts a driver value into the JSON-like types gojq
// understands.
func normalizeSQLValue(v any, numeric bool) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return normalizeSQLString(string(x), numeric)
	case string:
		// pgx returns NUMERIC/DECIMAL as text to avoid precision loss.
		return normalizeSQLString(x, numeric)
	case int64:
		return int(x)
	case int32:
		return int(x)
	case int:
		return x
	case float32:
		return float64(x)
	case float64:
		return x
	case bool:
		return x
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	default:
		// pgx may hand back its own types (numeric, arrays); round-trip
		// through JSON for a portable representation.
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		var out any
		if err := json.Unmarshal(b, &out); err != nil {
			return string(b)
		}
		return out
	}
}

func normalizeSQLString(s string, numeric bool) any {
	if numeric {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return int(i)
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return s
}

// evalJQ runs the compiled expression against rows and requires `true`.
func evalJQ(ctx context.Context, q *gojq.Query, rows []map[string]any) error {
	input := make([]any, len(rows))
	for i, r := range rows {
		input[i] = r
	}
	iter := q.RunWithContext(ctx, input)
	v, ok := iter.Next()
	if !ok {
		return errors.New("result_jq produced no output")
	}
	if err, isErr := v.(error); isErr {
		return fmt.Errorf("result_jq: %w", err)
	}
	if v != true {
		return fmt.Errorf("result_jq is not true: %v", v)
	}
	return nil
}
