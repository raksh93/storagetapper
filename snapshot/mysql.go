// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package snapshot

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/raksh93/storagetapper/db"
	"github.com/raksh93/storagetapper/encoder"
	"github.com/raksh93/storagetapper/log"
	"github.com/raksh93/storagetapper/types"
)

//MySQLmysqlReader is a snapshot reader structure
type mysqlReader struct {
	conn    *sql.DB
	trx     *sql.Tx
	rows    *sql.Rows
	log     log.Logger
	nrecs   uint64
	ndone   uint64
	encoder encoder.Encoder
	outMsg  []byte
	key     string
	err     error
}

func init() {
	registerPlugin("mysql", createMySQLReader)
}

func createMySQLReader() (Reader, error) {
	return &mysqlReader{}, nil
}

//PrepareFromTx starts snapshot from given tx
func (s *mysqlReader) StartFromTx(svc string, dbs string, table string, enc encoder.Encoder, tx *sql.Tx) (lastGtid string, err error) {
	s.log = log.WithFields(log.Fields{"service": svc, "db": dbs, "table": table})

	s.encoder = enc
	s.trx = tx

	/* Get GTID which is earlier in time then any row we will read during
	* snapshot */
	err = s.trx.QueryRow("SELECT @@global.gtid_executed").Scan(&lastGtid)
	if log.EL(s.log, err) {
		return
	}
	/* Use approximate row count, so as it's for reporting progress only */

	err = s.trx.QueryRow("SELECT table_rows FROM information_schema.tables WHERE table_schema=? AND table_name=?", dbs, table).Scan(&s.nrecs)
	//	err = s.trx.QueryRow("SELECT COUNT(*) FROM `" + table + "`").Scan(&s.nrecs)
	if log.EL(s.log, err) {
		return
	}
	s.rows, err = s.trx.Query("SELECT * FROM `" + table + "`")
	if log.EL(s.log, err) {
		return
	}

	s.ndone = 0

	s.log.Infof("Snapshot reader started, will stream %v records", s.nrecs)

	return
}

//Prepare connects to the db and starts snapshot for the table
func (s *mysqlReader) Start(cluster string, svc string, dbs string, table string, enc encoder.Encoder) (lastGtid string, err error) {
	ci := db.GetInfo(&db.Loc{Cluster: cluster, Service: svc, Name: dbs}, db.Slave)
	if ci == nil {
		return "", errors.New("No db info received")
	}

	s.conn, err = db.Open(ci)
	if log.E(err) {
		return
	}

	/*Do we need a transaction at all? We can use seqno to separate snapshot and
	* binlog data. Binlog is always newer. */
	/* If we need it, we need to rely on MySQL instance transactioin isolation
	* level or uncomment later if we have go1.8 */
	/*BeginTx since go1.8 */
	/*
		s.trx, err = s.conn.BeginTx(shutdown.Context, sql.TxOptions{sql.LevelRepeatableRead, true})
	*/
	s.trx, err = s.conn.Begin()
	if log.E(err) {
		return "", err
	}

	return s.StartFromTx(svc, dbs, table, enc, s.trx)
}

//EndFromTx deinitializes reader started by PrepareFromTx
func (s *mysqlReader) EndFromTx() {
	if s.rows != nil {
		log.EL(s.log, s.rows.Close())
	}
	s.log.Infof("Snapshot reader finished")
}

//End deinitializes snapshot reader
func (s *mysqlReader) End() {
	s.EndFromTx()

	if s.trx != nil {
		log.EL(s.log, s.trx.Rollback())
	}
	if s.conn != nil {
		log.EL(s.log, s.conn.Close())
	}

	s.log.Infof("Snapshot reader finished")
}

/*FIXME: Use sql.ColumnType.DatabaseType instead if this function if go1.8 is
* used */
func mySQLToDriverType(p *interface{}, mysql string) {
	switch mysql {
	case "int", "integer", "tinyint", "smallint", "mediumint":
		*p = new(sql.NullInt64)
	case "bigint", "bit", "year":
		*p = new(sql.NullInt64)
	case "float", "double", "decimal", "numeric":
		*p = new(sql.NullFloat64)
	case "char", "varchar":
		*p = new(sql.NullString)
	case "text", "tinytext", "mediumtext", "longtext", "blob", "tinyblob", "mediumblob", "longblob":
		*p = new(sql.RawBytes)
	case "date", "datetime", "timestamp", "time":
		*p = new(sql.NullString)
	default: // "binary", "varbinary" and others
		*p = new(sql.RawBytes)
	}
}

func driverTypeToGoType(p []interface{}, schema *types.TableSchema) []interface{} {
	v := make([]interface{}, len(p))

	for i := 0; i < len(p); i++ {
		v[i] = nil
		switch f := p[i].(type) {
		case *sql.NullInt64:
			if f.Valid {
				if schema.Columns[i].DataType != "bigint" {
					v[i] = int32(f.Int64)
				} else {
					v[i] = f.Int64
				}
			}
		case *sql.NullString:
			if f.Valid {
				v[i] = f.String
			}
		case *sql.NullFloat64:
			if f.Valid {
				if schema.Columns[i].DataType == "float" {
					v[i] = float32(f.Float64)
				} else {
					v[i] = f.Float64
				}
			}
		case *sql.RawBytes:
			if f != nil {
				v[i] = []byte(*f)
			}
		}
	}

	return v
}

//GetNext pops record fetched by HasNext
func (s *mysqlReader) GetNext() (string, []byte, error) {
	return s.key, s.outMsg, s.err
}

//HasNext fetches the record from MySQL and encodes using encoder provided when
//reader created
func (s *mysqlReader) HasNext() bool {
	if !s.rows.Next() {
		if s.err = s.rows.Err(); log.EL(s.log, s.err) {
			return true
		}
		if s.ndone == s.nrecs {
			s.log.Infof("Finished. Done %v(%v%%) of %v", s.ndone, 100, s.nrecs)
		}
		return false
	}

	var c []string
	c, s.err = s.rows.Columns()
	if log.EL(s.log, s.err) {
		return true
	}

	schema := s.encoder.Schema()

	if len(c) != len(schema.Columns) {
		s.err = fmt.Errorf("Rows column count(%v) should be equal to schema's column count(%v)", len(c), len(schema.Columns))
		return true
	}

	p := make([]interface{}, len(c))
	for i := 0; i < len(c); i++ {
		mySQLToDriverType(&p[i], schema.Columns[i].DataType)
	}

	s.err = s.rows.Scan(p...)
	if log.EL(s.log, s.err) {
		return true
	}

	v := driverTypeToGoType(p, schema)

	s.outMsg, s.err = s.encoder.Row(types.Insert, &v, 0)
	if log.EL(s.log, s.err) {
		return true
	}

	s.key = encoder.GetRowKey(s.encoder.Schema(), &v)

	//Statistics maybe inaccurate so we can have some rows even if we got 0 when
	//read rows count
	if s.nrecs == 0 {
		s.nrecs = 1
	}
	pctdone := s.ndone * 100 / s.nrecs
	var o uint64
	if s.nrecs%10 != 0 {
		o = 1
	}
	if s.ndone%(s.nrecs/10+o) == 0 {
		s.log.Infof("Snapshotting... Done %v(%v%%) of %v", s.ndone, pctdone, s.nrecs)
	}
	s.ndone++

	return true
}
