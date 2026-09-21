//go:build cgo

package rqlite

import (
	"context"
	"database/sql/driver"
	"strings"
	"errors"
	"fmt"
	"io"
	"time"

	proto "github.com/rqlite/rqlite/v10/command/proto"
)

// rqliteConn is one database/sql connection bound to the embedded store.
// RQLite has no client-side prepared statements, so Stmt just records the
// SQL and executes it on use (ADR-0004 D2).
type rqliteConn struct {
	store  *EmbeddedStore
	closed bool
}

var (
	_ driver.Conn                 = (*rqliteConn)(nil)
	_ driver.ConnPrepareContext   = (*rqliteConn)(nil)
	_ driver.ConnBeginTx          = (*rqliteConn)(nil)
	_ driver.Pinger               = (*rqliteConn)(nil)
)

func (c *rqliteConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *rqliteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if c.closed {
		return nil, driver.ErrBadConn
	}
	return &rqliteStmt{conn: c, query: query}, nil
}

func (c *rqliteConn) Close() error {
	c.closed = true
	return nil
}

func (c *rqliteConn) Ping(ctx context.Context) error {
	if c.closed {
		return driver.ErrBadConn
	}
	if _, _, err := c.store.execute(ctx, "SELECT 1", nil, false); err != nil {
		return err
	}
	return nil
}

func (c *rqliteConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *rqliteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.closed {
		return nil, driver.ErrBadConn
	}
	return &rqliteTx{conn: c}, nil
}

// rqliteTx: BEGIN/COMMIT/ROLLBACK are issued by gorm as plain statements
// and are a no-op at the transaction handle level (each RQLite request is
// atomic per statement batch).
type rqliteTx struct{ conn *rqliteConn }

func (t *rqliteTx) Commit() error   { return nil }
func (t *rqliteTx) Rollback() error { return nil }

// rqliteStmt is a pseudo-prepared statement (ADR-0004 D2).
type rqliteStmt struct {
	conn  *rqliteConn
	query string
}

func (s *rqliteStmt) Close() error { return nil }

func (s *rqliteStmt) NumInput() int {
	return -1
}

func (s *rqliteStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.conn.ExecContext(context.Background(), s.query, toNamedValues(args))
}

func (s *rqliteStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.QueryContext(context.Background(), s.query, toNamedValues(args))
}

func (s *rqliteStmt) ExecContext(ctx context.Context, args []driver.Value) (driver.Result, error) {
	return s.conn.ExecContext(ctx, s.query, toNamedValues(args))
}

func (s *rqliteStmt) QueryContext(ctx context.Context, args []driver.Value) (driver.Rows, error) {
	return s.conn.QueryContext(ctx, s.query, toNamedValues(args))
}

func toNamedValues(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, a := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: a}
	}
	return out
}

func (c *rqliteConn) execArgs(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}

func (c *rqliteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	argsV := c.execArgs(args)
	if c.closed {
		return nil, driver.ErrBadConn
	}
	r, isQuery, err := c.run(ctx, query, argsV, false)
	if err != nil {
		return nil, err
	}
	if isQuery {
		return driver.RowsAffected(0), nil
	}
	res := r.GetE()
	if res == nil {
		return driver.RowsAffected(0), nil
	}
	return &execResult{lastInsertID: res.GetLastInsertId(), rowsAffected: res.GetRowsAffected()}, nil
}

func (c *rqliteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	argsV := c.execArgs(args)
	if c.closed {
		return nil, driver.ErrBadConn
	}
	r, isQuery, err := c.run(ctx, query, argsV, true)
	if err != nil {
		return nil, err
	}
	if !isQuery {
		return nil, errors.New("rqlite: statement did not return rows")
	}
	qr := r.GetQ()
	if qr == nil {
		return nil, errors.New("rqlite: internal: missing query result")
	}
	return &queryResult{
		cols: qr.GetColumns(),
		vals: toAnyRows(qr.GetValues()),
	}, nil
}

// run executes one statement via the store's unified Request path
// (consensus for writes, read path for reads).
func (c *rqliteConn) run(ctx context.Context, query string, argsV []driver.Value, forceQuery bool) (*proto.ExecuteQueryResponse, bool, error) {
	params, err := toParameters(argsV)
	if err != nil {
		return nil, false, err
	}
	stmts := []*proto.Statement{{
		Sql:        query,
		Parameters: params,
		ForceQuery: forceQuery,
	}}
	req := &proto.Request{Statements: stmts}
	// Route pure reads through the store's fast read path (ro pool). The
	// unified Request path has a rqlite v10 bug where some reads return
	// empty results. Writes (and anything not classified read-only) go
	// through consensus.
	if !forceQuery && isReadOnlySQL(query) {
		rows, err := c.store.query(ctx, req)
		if err != nil {
			return nil, false, err
		}
		if len(rows) == 0 {
			return nil, false, errors.New("rqlite: empty response")
		}
		return &proto.ExecuteQueryResponse{
			Result: &proto.ExecuteQueryResponse_Q{Q: rows[0]},
		}, true, nil
	}
	resp, err := c.store.request(ctx, req)
	if err != nil {
		return nil, false, err
	}
	if len(resp) == 0 {
		return nil, false, errors.New("rqlite: empty response")
	}
	return resp[0], resp[0].GetQ() != nil, nil
}

// isReadOnlySQL conservatively reports whether query is a read statement
// (SELECT/PRAGMA/EXPLAIN/WITH). Anything else is treated as a write and
// goes through consensus (correct for both).
func isReadOnlySQL(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	return strings.HasPrefix(q, "select") ||
		strings.HasPrefix(q, "pragma") ||
		strings.HasPrefix(q, "explain") ||
		strings.HasPrefix(q, "with")
}

type execResult struct {
	lastInsertID int64
	rowsAffected int64
}

func (r *execResult) LastInsertId() (int64, error) { return r.lastInsertID, nil }
func (r *execResult) RowsAffected() (int64, error)  { return r.rowsAffected, nil }

type queryResult struct {
	cols []string
	vals [][]driver.Value
	pos  int
}

func (r *queryResult) Columns() []string { return r.cols }

func (r *queryResult) Next(dest []driver.Value) error {
	if r.pos >= len(r.vals) {
		return io.EOF
	}
	row := r.vals[r.pos]
	r.pos++
	for i := range dest {
		if i >= len(row) {
			break
		}
		dest[i] = row[i]
	}
	return nil
}

func (r *queryResult) Close() error { return nil }

// toAnyRows converts proto row values to driver.Value rows.
func toAnyRows(vals []*proto.Values) [][]driver.Value {
	out := make([][]driver.Value, 0, len(vals))
	for _, v := range vals {
		row := make([]driver.Value, 0, len(v.GetParameters()))
		for _, p := range v.GetParameters() {
			row = append(row, fromParameter(p))
		}
		out = append(out, row)
	}
	return out
}

func fromParameter(p *proto.Parameter) driver.Value {
	switch v := p.GetValue().(type) {
	case *proto.Parameter_I:
		return v.I
	case *proto.Parameter_D:
		return v.D
	case *proto.Parameter_B:
		return v.B
	case *proto.Parameter_S:
		return v.S
	case *proto.Parameter_Y:
		if v.Y == nil {
			return nil
		}
		return v.Y
	default:
		return nil
	}
}

// toParameters converts driver.Value args to RQLite proto parameters.
func toParameters(argsV []driver.Value) ([]*proto.Parameter, error) {
	if argsV == nil {
		return nil, nil
	}
	out := make([]*proto.Parameter, 0, len(argsV))
	for i, a := range argsV {
		p, err := toParameter(a)
		if err != nil {
			return nil, fmt.Errorf("rqlite: argument %d: %w", i, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func toParameter(a driver.Value) (*proto.Parameter, error) {
	switch v := a.(type) {
	case nil:
		return &proto.Parameter{Value: &proto.Parameter_Y{Y: nil}}, nil
	case bool:
		return &proto.Parameter{Value: &proto.Parameter_B{B: v}}, nil
	case int64:
		return &proto.Parameter{Value: &proto.Parameter_I{I: v}}, nil
	case float64:
		return &proto.Parameter{Value: &proto.Parameter_D{D: v}}, nil
	case []byte:
		return &proto.Parameter{Value: &proto.Parameter_Y{Y: v}}, nil
	case string:
		return &proto.Parameter{Value: &proto.Parameter_S{S: v}}, nil
	case time.Time:
		// Our schema stores timestamps as unixepoch integers (bigint).
		return &proto.Parameter{Value: &proto.Parameter_I{I: v.Unix()}}, nil
	default:
		return nil, fmt.Errorf("unsupported argument type %T", a)
	}
}
