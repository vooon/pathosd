package checks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

func newMockSQLChecker(t *testing.T, cfg *config.SQLCheckConfig) (*SQLChecker, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	// sqlmock serves exactly one connection; keep it instead of reconnecting.
	kept := *cfg
	kept.KeepAlive = true
	c, err := newSQLChecker(&kept, db)
	require.NoError(t, err)
	c.target = "mock"
	return c, mock
}

func sqlDetails(t *testing.T, r Result) *model.SQLDetails {
	t.Helper()
	d, ok := r.Details.(*model.SQLDetails)
	require.True(t, ok, "SQLDetails expected")
	return d
}

func TestSQLChecker_DefaultQuery(t *testing.T) {
	c, mock := newMockSQLChecker(t, &config.SQLCheckConfig{Driver: "mysql"})
	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	r := c.Check(context.Background())
	assert.True(t, r.Success, r.Detail)
	assert.Equal(t, "mock: 1 queries ok", r.Detail)
	d := sqlDetails(t, r)
	require.Len(t, d.Queries, 1)
	assert.Equal(t, "q1", d.Queries[0].Name)
	assert.Equal(t, 1, d.Queries[0].Rows)
	assert.True(t, d.Queries[0].Passed)
	assert.Equal(t, "sql", c.Type())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSQLChecker_ResultJQ(t *testing.T) {
	cfg := &config.SQLCheckConfig{Driver: "mysql", Queries: []config.SQLQueryConfig{
		{Name: "ro", SQL: "SELECT @@read_only AS read_only", ResultJQ: ".[0].read_only == 0"},
		{Name: "lag", SQL: "SHOW REPLICA STATUS", ResultJQ: ".[0].Seconds_Behind_Source < 60"},
	}}

	t.Run("text protocol numbers are parsed by column type", func(t *testing.T) {
		c, mock := newMockSQLChecker(t, cfg)
		mock.ExpectQuery("SELECT @@read_only AS read_only").WillReturnRows(
			sqlmock.NewRowsWithColumnDefinition(sqlmock.NewColumn("read_only").OfType("BIGINT", nil)).AddRow([]byte("0")))
		mock.ExpectQuery("SHOW REPLICA STATUS").WillReturnRows(
			sqlmock.NewRowsWithColumnDefinition(
				sqlmock.NewColumn("Seconds_Behind_Source").OfType("UNSIGNED BIGINT", nil),
				sqlmock.NewColumn("Replica_IO_Running").OfType("VARCHAR", nil),
			).AddRow([]byte("12"), []byte("Yes")))
		r := c.Check(context.Background())
		assert.True(t, r.Success, r.Detail)
		assert.Equal(t, []string{"ro", "lag"}, []string{sqlDetails(t, r).Queries[0].Name, sqlDetails(t, r).Queries[1].Name})
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("expression false fails and stops", func(t *testing.T) {
		c, mock := newMockSQLChecker(t, cfg)
		mock.ExpectQuery("SELECT @@read_only AS read_only").WillReturnRows(
			sqlmock.NewRowsWithColumnDefinition(sqlmock.NewColumn("read_only").OfType("BIGINT", nil)).AddRow([]byte("1")))
		r := c.Check(context.Background())
		assert.False(t, r.Success)
		assert.Equal(t, "mock: query ro: result_jq is not true: false", r.Detail)
		d := sqlDetails(t, r)
		require.Len(t, d.Queries, 1, "second query not run")
		assert.False(t, d.Queries[0].Passed)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no rows makes the expression null", func(t *testing.T) {
		c, mock := newMockSQLChecker(t, cfg)
		mock.ExpectQuery("SELECT @@read_only AS read_only").WillReturnRows(sqlmock.NewRows([]string{"read_only"}))
		r := c.Check(context.Background())
		assert.False(t, r.Success)
		assert.Contains(t, r.Detail, "result_jq is not true")
	})
}

func TestSQLChecker_Errors(t *testing.T) {
	cfg := &config.SQLCheckConfig{Driver: "postgres", Queries: []config.SQLQueryConfig{{SQL: "SELECT now()"}}}

	t.Run("query error", func(t *testing.T) {
		c, mock := newMockSQLChecker(t, cfg)
		mock.ExpectQuery("SELECT now()").WillReturnError(errors.New("permission denied for table"))
		r := c.Check(context.Background())
		assert.False(t, r.Success)
		assert.Equal(t, "mock: query q1: permission denied for table", r.Detail)
		assert.False(t, r.TimedOut)
	})

	t.Run("connect error", func(t *testing.T) {
		db, _, err := sqlmock.New()
		require.NoError(t, err)
		c, err := newSQLChecker(cfg, db)
		require.NoError(t, err)
		c.target = "mock"
		require.NoError(t, db.Close())
		r := c.Check(context.Background())
		assert.False(t, r.Success)
		assert.Contains(t, r.Detail, "mock: connect:")
	})

	t.Run("deadline exceeded is reported as timeout", func(t *testing.T) {
		c, mock := newMockSQLChecker(t, cfg)
		mock.ExpectQuery("SELECT now()").WillDelayFor(200 * time.Millisecond).WillReturnRows(sqlmock.NewRows([]string{"now"}))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		r := c.Check(ctx)
		assert.False(t, r.Success)
		assert.True(t, r.TimedOut)
	})

	t.Run("invalid jq is rejected at construction", func(t *testing.T) {
		db, _, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		_, err = newSQLChecker(&config.SQLCheckConfig{Queries: []config.SQLQueryConfig{{SQL: "SELECT 1", ResultJQ: ".[0] =="}}}, db)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "query q1: invalid result_jq")
	})
}

func TestSQLChecker_MaxRows(t *testing.T) {
	cfg := &config.SQLCheckConfig{Driver: "mysql", MaxRows: 2, Queries: []config.SQLQueryConfig{{SQL: "SELECT id FROM t", ResultJQ: "length == 2"}}}
	c, mock := newMockSQLChecker(t, cfg)
	mock.ExpectQuery("SELECT id FROM t").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2).AddRow(3))
	r := c.Check(context.Background())
	assert.True(t, r.Success, r.Detail)
	assert.Equal(t, 2, sqlDetails(t, r).Queries[0].Rows)
}

func TestNormalizeSQLValue(t *testing.T) {
	ts := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in      any
		numeric bool
		want    any
	}{
		{nil, true, nil},
		{[]byte("42"), true, 42},
		{[]byte("4.5"), true, 4.5},
		{[]byte("42"), false, "42"},
		{[]byte("abc"), true, "abc"},
		{"1.25", true, 1.25}, // pgx renders NUMERIC as text
		{"1.25", false, "1.25"},
		{int64(7), false, 7},
		{int32(7), false, 7},
		{float32(1.5), false, 1.5},
		{true, false, true},
		{"x", false, "x"},
		{ts, false, "2026-10-28T12:00:00Z"},
		{[]int{1, 2}, false, []any{1.0, 2.0}},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, normalizeSQLValue(tc.in, tc.numeric), "%v", tc.in)
	}
	for _, n := range []string{"INT", "UNSIGNED BIGINT", "DECIMAL", "int8", "FLOAT8", "NUMERIC"} {
		assert.True(t, isNumericSQLType(n), n)
	}
	for _, n := range []string{"VARCHAR", "TEXT", "POINT", "BOOL", "TIMESTAMP", ""} {
		assert.False(t, isNumericSQLType(n), n)
	}
}

func TestSQLConnector(t *testing.T) {
	t.Run("mysql", func(t *testing.T) {
		conn, target, err := sqlConnector(&config.SQLCheckConfig{Driver: "mysql", Host: "10.0.0.1", Port: 3306, User: "u", Password: "p"})
		require.NoError(t, err)
		assert.NotNil(t, conn)
		assert.Equal(t, "mysql 10.0.0.1:3306", target)
	})
	t.Run("postgres with tls", func(t *testing.T) {
		conn, target, err := sqlConnector(&config.SQLCheckConfig{Driver: "postgres", Host: "db.example", Port: 5432, User: "u", TLS: true, TLSInsecure: true})
		require.NoError(t, err)
		assert.NotNil(t, conn)
		assert.Equal(t, "postgres db.example:5432", target)
	})
	t.Run("unsupported driver", func(t *testing.T) {
		_, _, err := sqlConnector(&config.SQLCheckConfig{Driver: "oracle"})
		require.Error(t, err)
	})
	t.Run("missing ca file", func(t *testing.T) {
		_, _, err := sqlConnector(&config.SQLCheckConfig{Driver: "mysql", Host: "h", Port: 1, TLS: true, TLSCACert: "/nonexistent.pem"})
		require.Error(t, err)
	})
	t.Run("NewSQLChecker never dials", func(t *testing.T) {
		c, err := NewSQLChecker(&config.SQLCheckConfig{Driver: "postgres", Host: "127.0.0.1", Port: 1, User: "u"})
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		r := c.Check(ctx)
		assert.False(t, r.Success)
		assert.Contains(t, r.Detail, "postgres 127.0.0.1:1: connect:")
	})
}
